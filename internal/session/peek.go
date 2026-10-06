// Peek side-thread sessions (T4.3): the persisted home of a /btw side thread.
//
// A peek session is a real session file with one distinguishing property: its id
// carries the "peek_" purpose prefix, and Store.List() therefore omits it. That
// is how a side thread can be durable — reopenable after a restart, navigable
// with the same tree primitives — without polluting the surfaces that enumerate
// real conversations (--list-sessions, --continue/--resume, /dream distillation,
// the TUI session picker). The prefix is deliberately the whole mechanism: no
// SessionHeader field is added, so SchemaVersion stays at 3 and old files keep
// loading unchanged ("不改 schema" in the selection note).
//
// Content contract: a peek file holds ONLY the side thread's own Q&A — never a
// copy of the main conversation's background. The background is re-seeded from
// the live main context on every reopen, so a peek file stays small and can
// never resurrect a stale copy of the main transcript. Lineage is recorded with
// the existing ParentSession field.
package session

import (
	"errors"
	"strings"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
)

// PeekPrefix is the purpose prefix marking a session as a /btw side thread.
const PeekPrefix = "peek_"

// IsPeek reports whether id is a peek (side-thread) session id. The prefix is
// the sole purpose marker by design — see the file comment.
func IsPeek(id string) bool {
	return strings.HasPrefix(id, PeekPrefix)
}

// NewPeekID returns a fresh, time-ordered peek session id for now. It keeps the
// parent id scheme (NewID) so peek files sort by creation time too.
func NewPeekID(now time.Time) string {
	return PeekPrefix + NewID(now)
}

// PeekSession is the write handle for one persisted side thread. It mirrors the
// cursor bookkeeping a driver keeps for a main session (store + header + active
// leaf), but appends only the side thread's own messages.
//
// A nil *PeekSession is usable: every method degrades to a no-op, so a driver
// without a store (or a test) keeps the pre-T4.3 in-memory-only behavior by
// simply passing nil around.
type PeekSession struct {
	store  *Store
	header SessionHeader
	// leaf is the id of the last persisted entry; "" until the first append.
	leaf string
}

// OpenPeek creates a fresh peek session descending from parent and returns its
// handle. The peek header inherits the parent's model/provider/system prompt/cwd
// so a reopened side thread is faithful to the session it branched from, and
// records parent.ID as ParentSession for lineage. Nothing of the parent's
// messages is copied (see the file comment).
func OpenPeek(s *Store, parent SessionHeader, now time.Time) (*PeekSession, error) {
	if s == nil {
		return nil, errors.New("session: open peek: nil store")
	}
	if parent.ID == "" {
		return nil, errors.New("session: open peek: parent id must not be empty")
	}
	// uniqueID keeps a fast second /btw from reusing the id of the peek session
	// it just wrote (and advances now along with the id).
	id, now := s.uniqueID(now, PeekPrefix)
	h := SessionHeader{
		ID:            id,
		CreatedAt:     now,
		UpdatedAt:     now,
		Model:         parent.Model,
		Provider:      parent.Provider,
		SystemPrompt:  parent.SystemPrompt,
		ParentSession: parent.ID,
		Cwd:           parent.Cwd,
	}
	if err := s.Save(h, nil); err != nil {
		return nil, err
	}
	return &PeekSession{store: s, header: h}, nil
}

// Header returns the peek session's header (its ID is the file stem).
func (p *PeekSession) Header() SessionHeader {
	if p == nil {
		return SessionHeader{}
	}
	return p.header
}

// ID returns the peek session's id, "" when there is no peek session.
func (p *PeekSession) ID() string {
	if p == nil {
		return ""
	}
	return p.header.ID
}

// Append persists messages as the next links of the peek session's chain,
// growing its tree exactly like AppendBranch does for a main session (an
// existing branch is never flattened). It is a no-op for an empty batch or a
// nil handle.
func (p *PeekSession) Append(messages agentcore.MessageList) error {
	if p == nil || p.store == nil || len(messages) == 0 {
		return nil
	}
	p.header.UpdatedAt = time.Now().UTC()
	leaf, err := p.store.AppendBranch(p.header, p.leaf, messages)
	if err != nil {
		return err
	}
	p.leaf = leaf
	return nil
}

// Messages returns the side thread's persisted Q&A in order, for replaying a
// reopened peek session. A nil handle yields no messages and no error.
func (p *PeekSession) Messages() (agentcore.MessageList, error) {
	if p == nil || p.store == nil {
		return nil, nil
	}
	_, entries, err := p.store.LoadEntries(p.header.ID)
	if err != nil {
		return nil, err
	}
	msgs := make(agentcore.MessageList, 0, len(entries))
	for _, e := range entries {
		msgs = append(msgs, e.Message)
	}
	return msgs, nil
}

// LatestPeek returns the most recently updated peek session branched from
// parentID, or nil when the parent has no side thread on disk. It is how a bare
// "/btw" reopens a side thread from a previous process.
func (s *Store) LatestPeek(parentID string) (*PeekSession, error) {
	if s == nil || parentID == "" {
		return nil, nil
	}
	headers, err := s.ListAll()
	if err != nil {
		return nil, err
	}
	var best SessionHeader
	found := false
	for _, h := range headers {
		if !IsPeek(h.ID) || h.ParentSession != parentID {
			continue
		}
		// Tie-break on the id: two side threads can share an UpdatedAt second,
		// and the ids are time-ordered, so the later-created one wins — the same
		// thread a bare /btw in the previous process would have been using.
		if !found || h.UpdatedAt.After(best.UpdatedAt) ||
			(h.UpdatedAt.Equal(best.UpdatedAt) && h.ID > best.ID) {
			best, found = h, true
		}
	}
	if !found {
		return nil, nil
	}
	// Anchor the write cursor on the file's last entry so the next append grows
	// the existing chain instead of rooting a second one.
	_, entries, err := s.LoadEntries(best.ID)
	if err != nil {
		return nil, err
	}
	p := &PeekSession{store: s, header: best}
	if len(entries) > 0 {
		p.leaf = entries[len(entries)-1].ID
	}
	return p, nil
}
