package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/statline"
	"github.com/smallnest/pigo/internal/usage"
)

func ptr(v float64) *float64 { return &v }

// The converted GOAT body as /usage shows it: grok's allowance shape — label,
// 30-cell bar, floored percentage, resets line — for each window.
func TestWriteQuotaReportRendersPlanWindows(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	reset := now.Add(3*time.Hour + 30*time.Minute)
	sec := QuotaSection{
		Label: "commandcode",
		Snapshot: &usage.Snapshot{
			Source: "commandcode",
			Plan:   "GOAT",
			Note:   "billing period ends 2026-10-20",
			Windows: []usage.Window{
				{Kind: usage.WindowFiveHour, Percent: ptr(3.7), Used: ptr(0.67), Total: ptr(14), Unit: "USD", ResetsAt: &reset},
				{Kind: usage.WindowWeekly, Percent: ptr(62)},
				{Kind: usage.WindowBilling, Remaining: ptr(52.4281492675), Unit: "USD"},
			},
		},
	}
	var b bytes.Buffer
	WriteQuotaReport(&b, sec, now)
	out := b.String()

	for _, want := range []string{
		"commandcode · plan quota (GOAT)",
		"billing period ends 2026-10-20",
		"5-hour limit", "Weekly limit", "Monthly credits",
		"3%", "62%",
		// Rendered in local time, so the expectation is built the same way.
		"Resets " + reset.Local().Format("01-02 15:04") + " (in 3h 30m)",
		"$52.43 left",
		"$0.67 of $14.00 used",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("quota report missing %q; got:\n%s", want, out)
		}
	}
	// Grok's arithmetic: fill rounds, the label floors.
	bar := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.ContainsAny(line, "\u2588\u2591") {
			bar = line
			break
		}
	}
	if got := strings.Count(bar, "\u2588"); got != 1 {
		t.Errorf("3.7%% bar filled %d cells, want 1 (round(0.037*30)); line:\n%s", got, bar)
	}
	if got := strings.Count(bar, "\u2591"); got != quotaBarWidth-1 {
		t.Errorf("3.7%% bar has %d empty cells, want %d", got, quotaBarWidth-1)
	}
	// A full bar is never wider than the track.
	full := quotaBar(104)
	if strings.Count(full, "\u2588") != quotaBarWidth || !strings.Contains(full, "100%") {
		t.Errorf("over-limit bar = %q", full)
	}
	// The monthly credit lane has no percentage to invent.
	if strings.Contains(out, "0.0%") {
		t.Errorf("quota report invented a percentage:\n%s", out)
	}
}

// Rule 1's user-visible half: an unreadable window shows a neutral track, not
// a confident 0%.
func TestWriteQuotaReportUnknownWindowStaysNeutral(t *testing.T) {
	now := time.Now()
	sec := QuotaSection{Label: "opencode-go", Snapshot: &usage.Snapshot{
		Source:  "opencode-go",
		Windows: []usage.Window{{Kind: usage.WindowFiveHour}},
	}}
	var b bytes.Buffer
	WriteQuotaReport(&b, sec, now)
	out := b.String()
	if !strings.Contains(out, "--%") {
		t.Errorf("unknown window missing the neutral marker; got:\n%s", out)
	}
	if strings.Contains(out, " 0%") {
		t.Errorf("unknown window rendered as 0%%:\n%s", out)
	}
	if !strings.Contains(out, "opencode-go · plan quota") {
		t.Errorf("header missing the source name:\n%s", out)
	}
}

func TestWriteQuotaReportSkipsUnsupportedProvider(t *testing.T) {
	var b bytes.Buffer
	WriteQuotaReport(&b, QuotaSection{Label: "sensenova", Err: usage.ErrUnsupported}, time.Now())
	if out := b.String(); out != "" {
		t.Errorf("unsupported provider printed %q, want nothing", out)
	}
}

func TestWriteQuotaReportReportsFailureWithoutZeroing(t *testing.T) {
	now := time.Now()
	var b bytes.Buffer
	WriteQuotaReport(&b, QuotaSection{Label: "commandcode", Err: usage.ErrNoCredential}, now)
	out := b.String()
	if !strings.Contains(out, "plan quota unavailable (no API key configured)") {
		t.Errorf("missing-credential notice wrong:\n%s", out)
	}
	b.Reset()
	WriteQuotaReport(&b, QuotaSection{Label: "commandcode", Err: errFetch}, now)
	if out := b.String(); !strings.Contains(out, errFetch.Error()) {
		t.Errorf("fetch failure notice wrong:\n%s", out)
	}
}

var errFetch = &quotaTestErr{}

type quotaTestErr struct{}

func (*quotaTestErr) Error() string { return "HTTP 503 (Service Unavailable)" }

// The quota section follows the session block in one /usage output.
func TestWriteUsageReportAppendsQuotaSection(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	reset := now.Add(2 * time.Hour)
	stats := statline.Stats{Calls: 2, Input: 1000, Output: 100}
	var b bytes.Buffer
	WriteUsageReport(&b, stats, UsageReportOptions{
		SessionID: "sess-1",
		Model:     "glm-5.3-flash",
		Quota: &QuotaSection{Label: "commandcode", Snapshot: &usage.Snapshot{
			Source:  "commandcode",
			Plan:    "GOAT",
			Windows: []usage.Window{{Kind: usage.WindowWeekly, Percent: ptr(14.3), ResetsAt: &reset}},
		}},
	})
	out := b.String()
	sessionAt := strings.Index(out, "session usage")
	quotaAt := strings.Index(out, "commandcode · plan quota (GOAT)")
	if sessionAt < 0 || quotaAt < 0 {
		t.Fatalf("report missing a section:\n%s", out)
	}
	if quotaAt < sessionAt {
		t.Errorf("quota section rendered before the session block:\n%s", out)
	}
	if !strings.Contains(out, "✓ 2") {
		t.Errorf("session tallies missing:\n%s", out)
	}
}

// A session with no accounted turns still reports the plan: a fresh session
// that has not spent anything still has a subscription.
func TestWriteUsageReportQuotaOnEmptySession(t *testing.T) {
	var b bytes.Buffer
	WriteUsageReport(&b, statline.Stats{}, UsageReportOptions{
		SessionID: "sess-empty",
		Quota: &QuotaSection{Label: "commandcode", Snapshot: &usage.Snapshot{
			Source:  "commandcode",
			Windows: []usage.Window{{Kind: usage.WindowWeekly, Percent: ptr(1)}},
		}},
	})
	out := b.String()
	for _, want := range []string{"no accounted turns yet", "plan quota", "Weekly limit"} {
		if !strings.Contains(out, want) {
			t.Errorf("empty-session report missing %q:\n%s", want, out)
		}
	}
}

func TestWriteUsageReportWithoutQuotaIsUnchanged(t *testing.T) {
	var b bytes.Buffer
	WriteUsageReport(&b, statline.Stats{Calls: 1, Input: 10, Output: 5}, UsageReportOptions{SessionID: "s"})
	out := b.String()
	if strings.Contains(out, "plan quota") {
		t.Errorf("report without a probe printed a quota section:\n%s", out)
	}
	if strings.HasSuffix(out, "\n\n") {
		t.Errorf("report without a probe gained a trailing blank line: %q", out)
	}
}
