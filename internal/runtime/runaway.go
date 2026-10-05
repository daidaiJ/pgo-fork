// This file wires the runaway sentinel (T3.2) into the per-turn reminder
// mechanism: the detector in internal/runaway is a pure function over the
// message tail, and this provider consults it each turn so a stuck model gets
// an ephemeral <system-reminder> nudge on the request where it would otherwise
// repeat itself. Injection is advisory only — the run is never terminated.
package runtime

import (
	"context"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/runaway"
)

// RunawayReminderProvider injects the anti-loop sentinel reminder (T3.2) when
// the trailing tool-call steps repeat identically without producing new
// information. It is stateless: the streak is re-derived from the message tail
// on every consultation, so the signal survives resume / --continue and resets
// at user turns and compaction boundaries (see package runaway).
type RunawayReminderProvider struct{}

// Name implements ReminderProvider.
func (p *RunawayReminderProvider) Name() string { return "runaway" }

// Reminder implements ReminderProvider: it fires only while the sentinel
// detects an unproductive repeat streak, so the reminder appears on exactly
// the turns it applies to and drops out as soon as the model changes behavior.
func (p *RunawayReminderProvider) Reminder(_ context.Context, msgs agentcore.MessageList) (string, bool) {
	body := runaway.Detect(msgs)
	return body, body != ""
}
