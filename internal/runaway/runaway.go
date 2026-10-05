// Package runaway implements the anti-loop sentinel (T3.2): a stateless
// detector that notices the agent repeating the same tool call and receiving
// the same result back, and returns a system-reminder body telling the model
// to change strategy. Detection is advisory only — it never terminates a run.
//
// The detector is a pure function over the message tail, fusing two prototype
// lineages (qwen-code loop detection + minimax-code runaway guard): a
// repeat-key over the canonical tool name and arguments identifies "the same
// call", and a sha256 fingerprint of the model-visible result text
// corroborates that the repetition produced no new information — identical
// calls whose results keep changing are productive polling and never fire.
// When the trailing streak reaches ReminderStreak the reminder body is
// returned; the caller injects it as an ephemeral system-reminder.
//
// Statelessness is the load-bearing choice: the streak window is re-derived
// from the message tail on every consultation, bounded by the nearest user
// turn, compaction checkpoint, or text-only assistant turn, so the signal
// survives resume / --continue / follow-up continuations without carrying
// state across run boundaries and resets cleanly when history is compacted.
package runaway

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strconv"

	"github.com/smallnest/pigo/internal/agentcore"
)

// ReminderStreak is the number of trailing steps repeating the same call with
// the same result tolerated before Detect fires. Three mirrors the minimax
// runaway guard's default (its floor) — low enough to catch a loop early,
// high enough that one deliberate re-check after a failure stays silent.
const ReminderStreak = 3

// maxScanSteps bounds the backward window. A streak longer than the window
// still fires (the window is the unit of corroboration, not a reset); the cap
// only bounds per-request work and the streak count reported in the body.
const maxScanSteps = 16

// Detect scans the trailing tool-call steps of msgs (back to the nearest user
// turn, compaction checkpoint, or text-only assistant turn) and returns the
// reminder body when the model is stuck repeating an unproductive call. It
// returns "" when the tail shows no runaway pattern: fewer than
// ReminderStreak steps, varied calls, results that changed across the streak,
// or incomplete result evidence (a call without its executed result fails
// open, so a wiring gap can never cause a false reminder).
func Detect(msgs agentcore.MessageList) string {
	steps := scanSteps(msgs, maxScanSteps)
	if len(steps) < ReminderStreak {
		return ""
	}
	// Precompute each step's action keys once; steps[0] is the most recent
	// step, so a trailing streak is a prefix run of steps containing the key.
	keys := make([][]string, len(steps))
	for i, st := range steps {
		ks := make([]string, 0, len(st.calls))
		for _, c := range st.calls {
			ks = append(ks, ActionKey(c.Name, c.Arguments))
		}
		keys[i] = ks
	}
	for _, call := range steps[0].calls {
		key := ActionKey(call.Name, call.Arguments)
		streak := 0
		for streak < len(steps) && slices.Contains(keys[streak], key) {
			streak++
		}
		if streak < ReminderStreak {
			continue
		}
		fps := make([]string, 0, streak)
		complete := true
		for i := 0; i < streak && complete; i++ {
			fp, ok := stepResultFingerprint(steps[i], key)
			if !ok {
				complete = false
				break
			}
			fps = append(fps, fp)
		}
		// A changed result anywhere in the streak means the model is getting
		// new information each round — productive polling, not a loop.
		if !complete || !allEqual(fps) {
			continue
		}
		return reminderBody(call.Name, streak)
	}
	return ""
}

// step is one assistant tool-call batch plus the executed results that
// followed it, paired by call id.
type step struct {
	calls   []agentcore.ToolCallContent
	results map[string]agentcore.ToolResultMessage
}

// scanSteps walks msgs backwards collecting up to max tool-call steps of the
// current run, returned most-recent-first. The walk stops at the first
// message that is neither a tool result nor a tool-calling assistant turn:
// a user turn or compaction checkpoint starts a new window, and a text-only
// assistant turn is a run's natural end.
func scanSteps(msgs agentcore.MessageList, max int) []step {
	var steps []step
	var pending []agentcore.ToolResultMessage
	for i := len(msgs) - 1; i >= 0 && len(steps) < max; i-- {
		switch m := msgs[i].(type) {
		case agentcore.ToolResultMessage:
			pending = append(pending, m)
		case agentcore.AssistantMessage:
			calls := m.ToolCalls()
			if len(calls) == 0 {
				return steps
			}
			st := step{calls: calls, results: make(map[string]agentcore.ToolResultMessage, len(pending))}
			for _, r := range pending {
				st.results[r.ToolCallID] = r
			}
			pending = nil
			steps = append(steps, st)
		default:
			return steps
		}
	}
	return steps
}

// stepResultFingerprint fingerprints the executed result of the first call in
// st whose action key matches key. ok is false when no matching call has a
// result, leaving the evidence incomplete.
func stepResultFingerprint(st step, key string) (fp string, ok bool) {
	for _, c := range st.calls {
		if ActionKey(c.Name, c.Arguments) != key {
			continue
		}
		res, paired := st.results[c.ID]
		if !paired {
			continue
		}
		return ResultFingerprint(res), true
	}
	return "", false
}

// ActionKey returns the stable identity of a (tool, args) call: a sha256 over
// the tool name and the canonicalized arguments. Semantically identical
// arguments that differ only in object key order hash to the same key, so a
// stuck model cannot evade the guard by reordering fields (qwen's
// tool-call-repeat-key); array order is preserved because element position is
// usually argument meaning. Large payloads hash to a fixed-size digest, so
// call identity never depends on re-sending raw JSON.
func ActionKey(name string, args json.RawMessage) string {
	h := sha256.New()
	h.Write([]byte(name))
	h.Write([]byte{0})
	h.Write(canonicalArgs(args))
	return hex.EncodeToString(h.Sum(nil))
}

// canonicalArgs serializes arguments for hashing: well-formed JSON is
// re-encoded with map keys sorted (Go's encoding/json canonical map order),
// numbers kept verbatim, and array order preserved. Malformed JSON — which
// the loop preserves verbatim so schema validation can report it — falls back
// to its raw trimmed bytes, so two identical malformed calls still hash equal.
func canonicalArgs(args json.RawMessage) []byte {
	trimmed := bytes.TrimSpace(args)
	if len(trimmed) == 0 {
		return []byte("{}")
	}
	if !json.Valid(trimmed) {
		return trimmed
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return trimmed
	}
	out, err := json.Marshal(v)
	if err != nil {
		return trimmed
	}
	return out
}

// ResultFingerprint fingerprints what the model will see of a tool result:
// the error flag plus the content blocks. Details are excluded — the provider
// encoders transmit only the content text to the model, and several tools put
// per-call identifiers (background job ids, diffs) in Details that would make
// identical answers fingerprint differently. Call id and timestamp are
// excluded for the same reason: they differ on every execution.
func ResultFingerprint(res agentcore.ToolResultMessage) string {
	h := sha256.New()
	if res.IsError {
		h.Write([]byte("err\x00"))
	}
	for _, block := range res.Content {
		switch b := block.(type) {
		case agentcore.TextContent:
			h.Write([]byte("text\x00"))
			h.Write([]byte(b.Text))
		case agentcore.ImageContent:
			h.Write([]byte("image\x00"))
			h.Write([]byte(b.MimeType))
			h.Write([]byte{0})
			h.Write([]byte(b.Data))
		default:
			h.Write([]byte("block\x00"))
			h.Write(canonicalJSON(block))
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// canonicalJSON serializes v deterministically, degrading unserializable
// values to a fixed marker rather than failing the fingerprint.
func canonicalJSON(v any) []byte {
	if v == nil {
		return []byte("null")
	}
	out, err := json.Marshal(v)
	if err != nil {
		return []byte("<unserializable>")
	}
	return out
}

func allEqual(fps []string) bool {
	for _, fp := range fps[1:] {
		if fp != fps[0] {
			return false
		}
	}
	return true
}

// reminderBody is the injected guidance. The phrasing follows the minimax
// runaway guard's steer text: state the observation, forbid unchanged
// repetition, offer the two productive exits, and mark the reminder as
// turn-scoped so the model does not persist it.
func reminderBody(tool string, streak int) string {
	return "[runaway guard] The last " + strconv.Itoa(streak) + " tool calls were the same call (" + tool +
		") with identical arguments, and every one returned an identical result. Repeating it " +
		"unchanged will not produce new information. Stop and either change strategy with a " +
		"concrete expected state change, or report the blocker to the user. Repetition alone " +
		"does not establish that the task is complete or impossible.\n" +
		"This reminder is temporary runtime context for the current turn only; do not save it " +
		"or generalize it into memory, skills, or other persistent instructions."
}
