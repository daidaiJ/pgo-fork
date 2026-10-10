// Sub-agent transcript persistence (T7.1, mechanism A of the resume design in
// wiki/port/subagent-resume.md §5.2). A settled sub-agent run writes its child
// message history plus a small meta record to a sidecar directory beside the
// parent session file, so a later dispatch of the task tool with
// resume="<agent_id>" can replay that history as a prefix instead of paying for
// the same work twice.
//
// Layout: <sessionsRoot>/<sessionID>.subagents/<agentID>/{transcript.jsonl,
// meta.json}. The sidecar is keyed by the parent session id, so ownership is
// structural: a store bound to another session cannot see it (no cross-session
// resume). It lives beside the session file (not under the memory root) so the
// resume face is independent of memory.enabled — see the P3 ruling.
//
// The suffix literal is duplicated in internal/session's Delete (which removes
// the sidecar with the session); the two packages do not import each other, so
// SubagentsSidecarSuffix here and the literal there must stay in sync.
package runtime

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
)

// SubagentsSidecarSuffix is the suffix appended to a session id to name its
// sub-agent sidecar directory (<sessionID>.subagents). It is a name
// session.Store never enumerates as a session file.
const SubagentsSidecarSuffix = ".subagents"

const (
	subagentTranscriptFile = "transcript.jsonl"
	subagentMetaFile       = "meta.json"
	// subagentTranscriptScanMax bounds a single transcript line (one message,
	// which can carry a large tool result). Mirrors session scan headroom.
	subagentTranscriptScanMax = 16 << 20
)

// SubagentMeta is the sidecar's small metadata record: enough to decide whether
// a transcript is resumable and to pin the source model triple across a
// process restart (grok's subagents/<id>/meta.json analogue). It never carries
// credentials — a pin that needs a config-only key resolves it independently
// and surfaces a 401 through the normal envelope (registered observation).
type SubagentMeta struct {
	// AgentID is the parent task call's tool-call id — the resume handle.
	AgentID string `json:"agent_id"`
	// Status is the envelope status of the settled run: "completed" or
	// "failed". A non-terminal value (a future incremental writer's "running")
	// is rejected on resume so a live agent is never double-run.
	Status string `json:"status"`
	// StopReason is the envelope stop vocabulary ("completed", "max_tokens",
	// "error", "cancelled", "no_final_message").
	StopReason string `json:"stop_reason,omitempty"`
	// Model/BaseURL/Protocol/Provider/Proxy are the source run's provider
	// triple, pinned on resume (P2) and used to re-resolve a provider when the
	// resuming process runs a different model.
	Model    string `json:"model,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Provider string `json:"provider,omitempty"`
	Proxy    string `json:"proxy,omitempty"`
	// Messages is the number of messages in the transcript.
	Messages int `json:"messages"`
	// ResumedFrom records the source agent id when this run was itself a
	// resume (lineage, grok meta.resumed_from).
	ResumedFrom string `json:"resumed_from,omitempty"`
	// CreatedAt/UpdatedAt bound the run's wall clock (RFC 3339, UTC).
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SubagentStore persists sub-agent transcripts under a sessions root. It is
// bound to one parent session id at a time (BindSession) so every write and
// read is scoped to that session; an unbound store is inert (writes and reads
// are skipped), which is how a front-end that never binds keeps the feature
// off. All methods are safe for concurrent use by parallel task children and
// are nil-receiver safe, so a nil store means "no persistence" without a guard
// at every call site.
type SubagentStore struct {
	root string

	mu        sync.Mutex
	sessionID string
}

// NewSubagentStore returns a store rooted at sessionsRoot (the directory the
// session files live in, e.g. <PIGO_HOME>/sessions). An empty root yields nil
// (persistence off), matching the "store nil = unbound" contract.
func NewSubagentStore(sessionsRoot string) *SubagentStore {
	if strings.TrimSpace(sessionsRoot) == "" {
		return nil
	}
	return &SubagentStore{root: sessionsRoot}
}

// Root returns the sessions root the store writes under.
func (s *SubagentStore) Root() string {
	if s == nil {
		return ""
	}
	return s.root
}

// BindSession scopes the store to a parent session id — the value every
// subsequent write and read is keyed under. Front-ends call it once the
// session is created or resumed (and again on a mid-session switch).
func (s *SubagentStore) BindSession(sessionID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.sessionID = sessionID
	s.mu.Unlock()
}

// SessionID returns the currently bound session id ("" when unbound).
func (s *SubagentStore) SessionID() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionID
}

// agentDir returns <root>/<sessionID>.subagents/<agentID>.
func (s *SubagentStore) agentDir(sessionID, agentID string) string {
	return filepath.Join(s.root, sessionID+SubagentsSidecarSuffix, agentID)
}

// Append writes msgs as the agent's transcript, one message per JSON line. v1
// writes the whole transcript at settle (D-T7: the child loop's list is only
// final once the run ends, so per-turn incremental append — grok's shape — is
// the registered follow-up); a subsequent call rewrites it, so the file always
// matches the latest settled state. An unbound store or an empty agent id is a
// no-op: there is no session to key the sidecar under.
func (s *SubagentStore) Append(agentID string, msgs agentcore.MessageList) error {
	if s == nil || agentID == "" {
		return nil
	}
	sessionID := s.SessionID()
	if sessionID == "" {
		return nil
	}
	dir := s.agentDir(sessionID, agentID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("runtime: create sub-agent sidecar: %w", err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for i, m := range msgs {
		if err := enc.Encode(m); err != nil {
			return fmt.Errorf("runtime: encode sub-agent message[%d]: %w", i, err)
		}
	}
	return writeSidecarFile(filepath.Join(dir, subagentTranscriptFile), buf.Bytes())
}

// Finalize writes the agent's meta record (status, stop reason, source provider
// triple). It is written atomically (temp + rename) so a reader never sees a
// half-written record.
func (s *SubagentStore) Finalize(agentID string, meta SubagentMeta) error {
	if s == nil || agentID == "" {
		return nil
	}
	sessionID := s.SessionID()
	if sessionID == "" {
		return nil
	}
	dir := s.agentDir(sessionID, agentID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("runtime: create sub-agent sidecar: %w", err)
	}
	meta.AgentID = agentID
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("runtime: encode sub-agent meta: %w", err)
	}
	return writeSidecarFile(filepath.Join(dir, subagentMetaFile), append(b, '\n'))
}

// Load reads an agent's transcript and meta. A missing transcript is a normal
// "nothing to resume" result — (nil, nil, nil), not an error. A present
// transcript whose last line was torn mid-write yields the recoverable prefix
// (the torn tail is dropped, mirroring session load tolerance); a bad line with
// more content after it is real corruption and is an error. A missing meta
// (transcript present) returns a nil meta so the caller can fail closed.
func (s *SubagentStore) Load(agentID string) (agentcore.MessageList, *SubagentMeta, error) {
	if s == nil || agentID == "" {
		return nil, nil, nil
	}
	sessionID := s.SessionID()
	if sessionID == "" {
		return nil, nil, nil
	}
	dir := s.agentDir(sessionID, agentID)
	f, err := os.Open(filepath.Join(dir, subagentTranscriptFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("runtime: open sub-agent transcript: %w", err)
	}
	defer f.Close()
	msgs, err := decodeTranscript(f)
	if err != nil {
		return nil, nil, err
	}
	meta, err := readSubagentMeta(filepath.Join(dir, subagentMetaFile))
	if err != nil {
		return nil, nil, err
	}
	return msgs, meta, nil
}

// decodeTranscript decodes a transcript stream (one JSON message per line). A
// line that fails to decode is only tolerated as truncation when nothing
// follows it; otherwise the file is corrupt and an error is returned.
func decodeTranscript(r io.Reader) (agentcore.MessageList, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), subagentTranscriptScanMax)
	var out agentcore.MessageList
	broken := false
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if broken {
			return nil, fmt.Errorf("runtime: sub-agent transcript: corrupt line followed by more content")
		}
		var one agentcore.MessageList
		payload := make([]byte, 0, len(line)+2)
		payload = append(payload, '[')
		payload = append(payload, line...)
		payload = append(payload, ']')
		if err := json.Unmarshal(payload, &one); err != nil || len(one) != 1 {
			broken = true // tolerated only if this is the final non-empty line
			continue
		}
		out = append(out, one[0])
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("runtime: read sub-agent transcript: %w", err)
	}
	return out, nil
}

// readSubagentMeta decodes a meta record. A missing file returns (nil, nil).
func readSubagentMeta(path string) (*SubagentMeta, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("runtime: read sub-agent meta: %w", err)
	}
	var m SubagentMeta
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("runtime: decode sub-agent meta %s: %w", path, err)
	}
	return &m, nil
}

// writeSidecarFile writes data to path atomically (unique temp file + rename),
// so a concurrent reader never observes a partial record.
func writeSidecarFile(path string, data []byte) error {
	tmp := fmt.Sprintf("%s.%d.%d.tmp", path, os.Getpid(), time.Now().UnixNano())
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("runtime: write sidecar temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("runtime: commit sidecar: %w", err)
	}
	return nil
}
