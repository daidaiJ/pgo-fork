package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/cli/ui"
	"github.com/smallnest/pigo/internal/statline"
)

// newTestStatusBar builds a usage row with a fixed model name so tests do not
// depend on the environment.
func newTestStatusBar() statusBar {
	opts := Options{Model: "claude-opus", ProviderName: "test-provider"}
	return newStatusBar(DefaultTheme(), opts, "/tmp/project")
}

// usageTurn builds one settled-turn record for the status bar's ledger view.
func usageTurn(in, out, cacheRead int) statline.Record {
	return statline.Record{
		At:         time.Now().UTC(),
		Model:      "claude-opus",
		Input:      in,
		Output:     out,
		CacheRead:  cacheRead,
		TTFTMs:     800,
		DurationMs: 3000,
	}
}

func TestUsageRowRendersModelAndStats(t *testing.T) {
	s := newTestStatusBar()
	now := time.Now()
	s.usage.seed(statline.Aggregate([]statline.Record{
		usageTurn(43000, 400, 8000),
		usageTurn(700, 280, 0),
	}))

	out := s.Render(200, now)
	for _, want := range []string{
		"claude-opus (test-provider)", // model · provider
		"✓ 2",                         // completed API turns
		"in 44K",                      // summed prompt tokens
		"out 680",                     // summed completion tokens
		"cache",                       // cache hit share (observed)
		"miss 1",                      // the second turn read no cache
		"ttft",                        // last turn's first-token latency
		"tok/s",                       // last turn's throughput
	} {
		if !strings.Contains(stripANSI(out), want) {
			t.Errorf("usage row missing %q; got %q", want, stripANSI(out))
		}
	}
	if w := ui.Width(stripANSI(out)); w > 200 {
		t.Errorf("render width %d exceeds terminal width 200", w)
	}
}

// TestUsageRowShowsRetriesAndFailures pins the two grok segments O1 added
// (ApiRetries `↻ n`, ApiCalls failure tally `✗ n`): both appear only when they
// happened.
func TestUsageRowShowsRetriesAndFailures(t *testing.T) {
	s := newTestStatusBar()
	now := time.Now()
	recs := []statline.Record{usageTurn(1000, 100, 900), usageTurn(1000, 100, 900)}
	recs[0].Retries = 2
	recs[1].Err = true
	s.usage.seed(statline.Aggregate(recs))

	out := stripANSI(s.Render(200, now))
	for _, want := range []string{"✓ 1", "✗ 1", "↻ 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage row missing %q; got %q", want, out)
		}
	}
	// A clean session shows neither.
	s.usage.seed(statline.Aggregate([]statline.Record{usageTurn(1000, 100, 900)}))
	out = stripANSI(s.Render(200, now))
	if strings.Contains(out, "↻") || strings.Contains(out, "✗") {
		t.Errorf("zero-hide failed: %q", out)
	}
}

// TestUsageRowLiveTTFTBeforeFirstRecord covers the mid-turn case: with no
// settled record yet, the perf segment shows the in-flight turn's elapsed TTFT.
func TestUsageRowLiveTTFTBeforeFirstRecord(t *testing.T) {
	s := newTestStatusBar()
	now := time.Now()
	s.usage.beginRun(now.Add(-time.Minute))
	s.usage.markFirstDelta(now.Add(-50 * time.Second))
	if out := stripANSI(s.Render(200, now)); !strings.Contains(out, "10s") && !strings.Contains(out, "10000ms") {
		t.Errorf("expected the live TTFT (~10s) in the row: %q", out)
	}
}

func TestUsageRowHidesUnobservedSegments(t *testing.T) {
	s := newTestStatusBar()
	out := stripANSI(s.Render(120, time.Now()))
	if strings.Contains(out, "✓") || strings.Contains(out, "in ") || strings.Contains(out, "cache") {
		t.Errorf("segments should stay hidden before any run data: %q", out)
	}
	if !strings.Contains(out, "claude-opus") {
		t.Errorf("model name should always render: %q", out)
	}
}

// TestUsageRowShowsColdCache verifies the 2026-10-07 contract flip (user
// report: the cache segment was missing from the row): a turn with observed
// prompt usage always shows the cache share, and a gateway that reports no
// cached tokens reads as an explicit "cache 0%" — a cold cache is visible,
// not silently hidden.
func TestUsageRowShowsColdCache(t *testing.T) {
	s := newTestStatusBar()
	now := time.Now()
	s.usage.seed(statline.Aggregate([]statline.Record{usageTurn(500, 100, 0)}))
	if out := stripANSI(s.Render(120, now)); !strings.Contains(out, "cache 0%") {
		t.Errorf("cache segment should read 0%% when no cache tokens are reported: %q", out)
	}
	s.usage.seed(statline.Aggregate([]statline.Record{usageTurn(500, 100, 0), usageTurn(500, 100, 400)}))
	if out := stripANSI(s.Render(120, now)); !strings.Contains(out, "cache 29%") {
		t.Errorf("cache segment should report the hit share (400/1400): %q", out)
	}
}

// TestUsageRowThinkingShare verifies the think segment: streamed reasoning
// characters over all streamed characters, hidden until a stream exists.
func TestUsageRowThinkingShare(t *testing.T) {
	s := newTestStatusBar()
	now := time.Now()
	s.usage.beginRun(now)
	if out := stripANSI(s.Render(160, now)); strings.Contains(out, "think") {
		t.Errorf("think segment should stay hidden before any stream: %q", out)
	}
	s.usage.addChars(strings.Repeat("a", 600), true)
	s.usage.addChars(strings.Repeat("b", 400), false)
	if out := stripANSI(s.Render(160, now)); !strings.Contains(out, "think 60%") {
		t.Errorf("think segment should report 60%% reasoning share: %q", out)
	}
}

func TestUsageRowVeryNarrowNeverOverflows(t *testing.T) {
	s := newTestStatusBar()
	now := time.Now()
	s.usage.seed(statline.Aggregate([]statline.Record{usageTurn(91000, 24000, 40000)}))

	for _, width := range []int{1, 2, 3, 5, 8, 12} {
		out := s.Render(width, now)
		if w := ui.Width(stripANSI(out)); w > width {
			t.Errorf("width %d: render width %d overflows: %q", width, w, stripANSI(out))
		}
	}
}

func TestUsageRowZeroWidthEmpty(t *testing.T) {
	s := newTestStatusBar()
	if out := s.Render(0, time.Now()); out != "" {
		t.Errorf("zero width should render empty, got %q", out)
	}
}

func TestUsageRowSetModelKeepsProviderSuffix(t *testing.T) {
	s := newTestStatusBar()
	s.SetModel("next-model")
	if out := stripANSI(s.Render(120, time.Now())); !strings.Contains(out, "next-model (test-provider)") {
		t.Errorf("/model switch should keep the provider suffix: %q", out)
	}
}

func TestHumanizeInt(t *testing.T) {
	cases := map[int]string{0: "0", 90866: "90,866", 1000: "1,000", 999: "999", 1234567: "1,234,567"}
	for in, want := range cases {
		if got := humanizeInt(in); got != want {
			t.Errorf("humanizeInt(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestAbbreviateHome(t *testing.T) {
	home := homeDir()
	if home == "" {
		t.Skip("no home dir available")
	}
	if got := abbreviateHome(home); got != "~" {
		t.Errorf("abbreviateHome(home) = %q, want ~", got)
	}
	if got := abbreviateHome(home + "/foo/bar"); got != "~/foo/bar" {
		t.Errorf("abbreviateHome(home/foo/bar) = %q, want ~/foo/bar", got)
	}
	if got := abbreviateHome("/etc/passwd"); got != "/etc/passwd" {
		t.Errorf("abbreviateHome(/etc/passwd) = %q, want unchanged", got)
	}
}

func TestHumanTokens(t *testing.T) {
	cases := map[int]string{
		0: "0", 999: "999", 1000: "1.0K", 3000: "3.0K", 26000: "26K", 148766: "149K",
		150340: "150K", 1000000: "1.0M", 2600000: "2.6M",
	}
	for in, want := range cases {
		if got := humanTokens(in); got != want {
			t.Errorf("humanTokens(%d) = %q, want %q", in, got, want)
		}
	}
}
