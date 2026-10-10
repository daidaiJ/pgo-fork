package prompts

import (
	"testing"

	"github.com/smallnest/pigo/internal/runtime"
)

// TestParseUsageAndStats pin the /usage and /stats grammars (O1/T7.3c): /usage
// takes no arguments, /stats takes an optional day|week|all window, and a typo
// is refused rather than silently defaulted.
func TestParseUsageAndStats(t *testing.T) {
	it, err := parseUsage("")
	if err != nil || it.Kind != runtime.IntentUsage {
		t.Fatalf("parseUsage(\"\") = (%v, %v), want IntentUsage", it, err)
	}
	if _, err := parseUsage("extra"); err == nil {
		t.Error("parseUsage with an argument should be refused")
	}

	cases := []struct {
		arg    string
		window string
	}{
		{"", ""},
		{"day", "day"},
		{"WEEK", "week"},
		{"all", "all"},
	}
	for _, c := range cases {
		it, err := parseStats(c.arg)
		if err != nil {
			t.Fatalf("parseStats(%q) = error %v", c.arg, err)
		}
		if it.Kind != runtime.IntentStats || it.Window != c.window {
			t.Errorf("parseStats(%q) = kind %v window %q, want IntentStats/%q", c.arg, it.Kind, it.Window, c.window)
		}
	}
	for _, bad := range []string{"month", "day week", "year"} {
		if _, err := parseStats(bad); err == nil {
			t.Errorf("parseStats(%q) should be refused", bad)
		}
	}
}

// TestResolveUsageCommandThroughRegistry checks the declared commands reach the
// typed intents through the real registry lookup (the path the front-ends use).
func TestResolveUsageCommandThroughRegistry(t *testing.T) {
	reg := bareRegistry(t)
	outcome, err := reg.ResolveOutcome("/usage")
	if err != nil {
		t.Fatalf("ResolveOutcome(/usage) = %v", err)
	}
	if outcome.Kind != runtime.SlashIntent || outcome.Intent.Kind != runtime.IntentUsage {
		t.Fatalf("/usage resolved to %+v, want the usage intent", outcome)
	}
	outcome, err = reg.ResolveOutcome("/stats day")
	if err != nil {
		t.Fatalf("ResolveOutcome(/stats day) = %v", err)
	}
	if outcome.Intent.Kind != runtime.IntentStats || outcome.Intent.Window != "day" {
		t.Fatalf("/stats day resolved to %+v, want IntentStats/day", outcome.Intent)
	}
	if _, err := reg.ResolveOutcome("/stats month"); err == nil {
		t.Error("/stats month should be refused")
	}
}
