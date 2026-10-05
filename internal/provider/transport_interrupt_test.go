package provider

import "testing"

// TestIsStreamInterruption pins the stream-interruption discriminator to the
// failure families pump emits. The negative rows are the point of the test:
// grok folded the broad StreamError class into Api and recovery silently went
// absent, so decode/finish errors (persistent protocol mismatch), user aborts,
// and connect-time failures (nothing consumed yet) must never classify as
// interruptions.
func TestIsStreamInterruption(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		// Recoverable mid-stream family (pigo's {Http, IdleTimeout} kinds;
		// EmptyResponse never transits pump — the runtime matches
		// agentcore.ErrStreamIncomplete directly).
		{"read error: transport: network error: connection reset by peer", true},
		{"read error: transport: network timeout: i/o timeout", true},
		{"idle timeout: no data received", true},
		{"content stall timeout", true},
		// Persistent protocol failures: not transient drops.
		{"decode error: invalid character 'x'", false},
		{"finish error: malformed tail", false},
		// User abort and connect-time failures: no partial response consumed,
		// or the user asked for the stop.
		{"stream aborted", false},
		{"stream aborted: context canceled", false},
		{"transport: upstream 401: invalid api key", false},
		{"transport: upstream 429", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsStreamInterruption(c.msg); got != c.want {
			t.Errorf("IsStreamInterruption(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
}
