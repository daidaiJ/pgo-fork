package runtime

// Unit tests for the T5.1 sub-agent result envelope kernel (envelope.go): the
// stop-reason normalization table, the next_step mapping table (kimi
// NEXT_STEP_BY_REASON adapted for the v1 no-resume wording), the fixed-field
// formatter, and the body cap. Table-driven per the spec's acceptance.

import (
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// TestStopReasonOf pins the raw agentcore stop-reason -> envelope vocabulary
// mapping, including the nil-final (no assistant turn at all) case.
func TestStopReasonOf(t *testing.T) {
	cases := []struct {
		reason string
		want   string
	}{
		{agentcore.StopReasonEndTurn, "completed"},
		{agentcore.StopReasonToolUse, "completed"},
		{agentcore.StopReasonLength, "max_tokens"},
		{agentcore.StopReasonAborted, "cancelled"},
		{agentcore.StopReasonError, "error"},
		{"something-new", "error"}, // unknown reasons fail closed as error
	}
	for _, tc := range cases {
		if got := stopReasonOf(&agentcore.AssistantMessage{StopReason: tc.reason}); got != tc.want {
			t.Errorf("stopReasonOf(%q) = %q, want %q", tc.reason, got, tc.want)
		}
	}
	if got := stopReasonOf(nil); got != "no_final_message" {
		t.Errorf("stopReasonOf(nil) = %q, want no_final_message", got)
	}
}

// TestBuildEnvelopeCompleted verifies the happy path keeps the verbatim-body
// contract: status completed, no next_step, body untouched.
func TestBuildEnvelopeCompleted(t *testing.T) {
	env, body := buildEnvelope("ag-1", &agentcore.AssistantMessage{StopReason: agentcore.StopReasonEndTurn}, "final report\nline 2")
	if env.Status != SubAgentStatusCompleted || env.StopReason != "completed" || env.AgentID != "ag-1" {
		t.Errorf("envelope = %+v", env)
	}
	if env.NextStep != "" {
		t.Errorf("completed next_step = %q, want empty", env.NextStep)
	}
	if body != "final report\nline 2" {
		t.Errorf("body = %q, want verbatim", body)
	}
}

// TestBuildEnvelopeEndTurnEmptyTextIsNoFinalMessage pins the edge case the old
// code papered over with a placeholder: an end_turn child with no text is a
// failure (no_final_message), not a success.
func TestBuildEnvelopeEndTurnEmptyTextIsNoFinalMessage(t *testing.T) {
	env, _ := buildEnvelope("ag-2", &agentcore.AssistantMessage{StopReason: agentcore.StopReasonEndTurn}, "  \n ")
	if env.Status != SubAgentStatusFailed || env.StopReason != "no_final_message" {
		t.Errorf("envelope = %+v, want failed/no_final_message", env)
	}
	if env.NextStep == "" {
		t.Error("failed envelope must carry next_step guidance")
	}
}

// TestNextStepFor pins the mapping table fragments that distinguish each
// guidance (acceptance: each stop_reason maps to a DIFFERENT next_step).
func TestNextStepFor(t *testing.T) {
	cases := map[string][]string{
		"max_tokens":        {"output limit", "smaller follow-up"},
		"error":             {"Re-dispatch", "report the failure"},
		"cancelled":         {"stopped by the user", "Do not restart"},
		"no_final_message":  {"no final report"},
		"unknown-reason-xx": {"Re-dispatch"},
	}
	for reason, frags := range cases {
		got := nextStepFor(reason)
		if got == "" {
			t.Errorf("nextStepFor(%q) is empty", reason)
			continue
		}
		for _, frag := range frags {
			if !strings.Contains(got, frag) {
				t.Errorf("nextStepFor(%q) = %q, missing %q", reason, got, frag)
			}
		}
	}
	if got := nextStepFor("completed"); got != "" {
		t.Errorf("nextStepFor(completed) = %q, want empty", got)
	}
}

// TestEnvelopeFormat pins the fixed-field weak-model contract: field-per-line
// header, next_step only when non-empty, body after the --- separator.
func TestEnvelopeFormat(t *testing.T) {
	env := SubAgentEnvelope{AgentID: "call-42", Status: SubAgentStatusFailed, StopReason: "max_tokens", NextStep: "split it"}
	got := env.Format("partial body")
	for _, want := range []string{
		"[subagent result]",
		"agent_id: call-42",
		"status: failed",
		"stop_reason: max_tokens",
		"next_step: split it",
		"---\npartial body",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("format missing %q:\n%s", want, got)
		}
	}
	// A completed envelope (never formatted in practice) would omit next_step.
	minimal := SubAgentEnvelope{AgentID: "a", Status: SubAgentStatusFailed, StopReason: "error"}
	if strings.Contains(minimal.Format("b"), "next_step:") {
		t.Error("empty next_step must not render a next_step line")
	}
}

// TestCapBody pins the 2000-char body cap with a rune-boundary-safe cut and an
// explicit truncation marker.
func TestCapBody(t *testing.T) {
	if got := capBody("short"); got != "short" {
		t.Errorf("capBody(short) = %q", got)
	}
	long := strings.Repeat("世", maxEnvelopeBody) // multi-byte runes, well over 2000 bytes
	got := capBody(long)
	if !strings.HasSuffix(got, "\n… (truncated)") {
		t.Errorf("capped body lacks truncation marker: %q", got[len(got)-30:])
	}
	if head := strings.TrimSuffix(got, "\n… (truncated)"); len(head) > maxEnvelopeBody {
		t.Errorf("capped head = %d bytes, want <= %d", len(head), maxEnvelopeBody)
	}
	// The cut must not split a rune: decoding the head must succeed.
	if head := strings.TrimSuffix(got, "\n… (truncated)"); strings.Contains(head, "\ufffd") {
		t.Error("capped head contains replacement runes (mid-rune cut)")
	}
}
