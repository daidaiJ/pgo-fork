// This file implements the runtime side of the session usage ledger (O1 /
// T7.3c): the sink the loop records each settled turn through, and the
// per-sub-agent attribution wrapper.
//
// It mirrors SubagentStore's shape (root = the sessions directory, session id
// bound by the front-end once the session exists) so a front-end binds both at
// the same point: Env.Usage.BindSession(header.ID) beside
// Env.Subagents.BindSession(header.ID). Reads go through statline.LoadUsage, so
// the recorder holds no accounting state of its own — the ledger file is the
// single source of truth, and the in-memory view is always a fresh aggregation
// of it.
package runtime

import (
	"fmt"
	"os"
	"sync"

	"github.com/smallnest/pigo/internal/statline"
)

// UsageRecorder persists per-turn usage records for the session bound to it.
// The zero value and a nil receiver are inert, so a front-end assembled without
// accounting (tests, tools-off runs) pays nothing.
type UsageRecorder struct {
	mu        sync.Mutex
	root      string
	sessionID string
}

// NewUsageRecorder returns a recorder writing under root (the sessions
// directory). An empty root yields an inert recorder.
func NewUsageRecorder(root string) *UsageRecorder {
	return &UsageRecorder{root: root}
}

// Root returns the directory the recorder writes under.
func (u *UsageRecorder) Root() string {
	if u == nil {
		return ""
	}
	return u.root
}

// BindSession scopes the recorder to a session id ("" unbinds it). A front-end
// calls this where it calls SubagentStore.BindSession, so a /sessions switch
// re-scopes both.
func (u *UsageRecorder) BindSession(id string) {
	if u == nil {
		return
	}
	u.mu.Lock()
	u.sessionID = id
	u.mu.Unlock()
}

// SessionID returns the bound session id ("" when unbound).
func (u *UsageRecorder) SessionID() string {
	if u == nil {
		return ""
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.sessionID
}

// Record appends one turn's record to the bound session's ledger. It is
// best-effort by construction (the contract persistSettle uses): a ledger write
// failure is reported on stderr and swallowed, so accounting can never turn a
// finished turn into a failure.
func (u *UsageRecorder) Record(rec statline.Record) {
	if u == nil {
		return
	}
	u.mu.Lock()
	root, id := u.root, u.sessionID
	u.mu.Unlock()
	if root == "" || id == "" {
		return
	}
	if err := statline.AppendUsage(root, id, rec); err != nil {
		fmt.Fprintf(os.Stderr, "pigo: usage record not saved: %v\n", err)
	}
}

// ChildSink returns the sink a sub-agent run records through: the record is
// attributed to the parent session with Subagent and AgentID set, and a turn
// that did not settle is marked incomplete (the fork's RecordSubagentUsage
// semantics). Process-isolated sub-agents are a separate process and never
// reach this sink (registered leftover with T7.1's P5).
func (u *UsageRecorder) ChildSink(agentID string) func(statline.Record) {
	return func(r statline.Record) {
		r.Subagent = true
		r.AgentID = agentID
		if r.Err {
			r.Incomplete = true
		}
		u.Record(r)
	}
}

// Records returns the bound session's records in file order (nil when unbound
// or the ledger is missing/unreadable).
func (u *UsageRecorder) Records() []statline.Record {
	if u == nil {
		return nil
	}
	u.mu.Lock()
	root, id := u.root, u.sessionID
	u.mu.Unlock()
	if root == "" || id == "" {
		return nil
	}
	recs, err := statline.LoadUsage(root, id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pigo: usage ledger unreadable: %v\n", err)
		return nil
	}
	return recs
}

// Stats returns the aggregation of the bound session's records — the value the
// status line, /usage and /stats all read.
func (u *UsageRecorder) Stats() statline.Stats {
	return statline.Aggregate(u.Records())
}
