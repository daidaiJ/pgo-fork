// System-reminder wrapping (US-002): the <system-reminder> envelope marks a
// reminder body as background context from the harness, not a user instruction
// (the pi / Claude Code semantic convention). It lives in agentcore because
// two layers render wrapped reminders: the loop's per-turn reminder registry
// (runtime) and the compaction pipeline's post-compaction live-state
// re-injection (compaction, T3.3.1).
package agentcore

// systemReminderPreamble marks the wrapped body as background context rather
// than a user instruction. It leads every injected reminder so the model
// never mistakes harness state for a user request.
const systemReminderPreamble = "The following is background context provided automatically by the harness. " +
	"It is NOT a message or instruction from the user; do not act on it as a request. " +
	"Use it only to stay aware of the current state."

// WrapSystemReminder wraps a reminder body in <system-reminder> tags with the
// background-context preamble. The result is the text of a single injected
// message.
func WrapSystemReminder(body string) string {
	return "<system-reminder>\n" + systemReminderPreamble + "\n\n" + body + "\n</system-reminder>"
}
