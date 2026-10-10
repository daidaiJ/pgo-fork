package sources

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/usage"
)

// fixtureAnchor is the instant the recorded bodies were captured at. Pinning
// nowFunc to it keeps every reset-time assertion below stable forever.
var fixtureAnchor = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

func pinClock(t *testing.T) {
	t.Helper()
	saved := nowFunc
	nowFunc = func() time.Time { return fixtureAnchor }
	t.Cleanup(func() { nowFunc = saved })
}

func readFixture(t *testing.T, provider, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", provider, name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return body
}

func TestSourceMatching(t *testing.T) {
	oc := newOpencodeGo()
	for _, provider := range []string{"opencode-go", "OpenCode-Go", " opencode-go "} {
		if !oc.MatchProvider(provider) {
			t.Errorf("opencode-go should match provider %q", provider)
		}
	}
	if oc.MatchProvider("opencode") {
		t.Error("opencode-go must not claim the pay-as-you-go opencode provider")
	}
	if !oc.MatchBase("https://opencode.ai/zen/go") || !oc.MatchBase("https://opencode.ai/zen/go/v1") {
		t.Error("opencode-go should match its own base URL, with or without the /v1 suffix")
	}
	if oc.MatchBase("https://opencode.ai/zen/v1") {
		t.Error("opencode-go must not claim the sibling /zen endpoint on the same host")
	}

	cc := newCommandCode()
	for _, provider := range []string{"commandcode", "CommandCode", "command-code"} {
		if !cc.MatchProvider(provider) {
			t.Errorf("commandcode should match provider %q", provider)
		}
	}
	if cc.MatchProvider("custom") {
		t.Error("commandcode must not claim the generic custom driver by name")
	}
	if !cc.MatchBase("https://api.commandcode.ai/provider/v1") {
		t.Error("commandcode should match its inference base URL host")
	}
	if cc.MatchBase("https://commandcode.ai/provider/v1") {
		t.Error("commandcode must match the api host only")
	}
}

// The registry is populated by the sources' own init(), which is what makes
// "add a provider = add a file" true.
func TestRegistryResolvesBothSources(t *testing.T) {
	got, ok := usage.Lookup("opencode-go", "")
	if !ok || got.Name() != "opencode-go" {
		t.Fatalf("Lookup(opencode-go) = (%v, %v)", got, ok)
	}
	got, ok = usage.Lookup("custom", "https://api.commandcode.ai/provider/v1")
	if !ok || got.Name() != "commandcode" {
		t.Fatalf("Lookup by commandcode base URL = (%v, %v)", got, ok)
	}
	if _, ok := usage.Lookup("sensenova", "https://token.sensenova.cn/v1"); ok {
		t.Error("an unsupported provider resolved to a source")
	}
}

func TestCommandCodePlanNames(t *testing.T) {
	cases := map[string]string{
		"individual-goat": "GOAT",
		"go":              "Go",
		"pro":             "Pro",
		"max10":           "Max 10",
		"max20":           "Max 20",
		"":                "",
		"brand-new":       "brand-new",
	}
	for in, want := range cases {
		if got := commandCodePlanName(in); got != want {
			t.Errorf("commandCodePlanName(%q) = %q, want %q", in, got, want)
		}
	}
}

// The recorded GOAT body: dollar-denominated 5-hour and weekly windows plus
// the monthly credit remainder, with the plan id from the subscriptions call.
func TestCommandCodeParsesRecordedGoatBody(t *testing.T) {
	pinClock(t)
	snap, err := newCommandCode().Parse(readFixture(t, "commandcode", "goat.json"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if snap.Plan != "GOAT" {
		t.Errorf("Plan = %q, want GOAT", snap.Plan)
	}
	if !strings.Contains(snap.Note, "2026-10-20") {
		t.Errorf("Note = %q, want the billing period end", snap.Note)
	}
	if len(snap.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", snap.Warnings)
	}
	usage.SortWindows(snap.Windows)
	if len(snap.Windows) != 3 {
		t.Fatalf("windows = %d, want 3 (5-hour, weekly, monthly credits): %+v", len(snap.Windows), snap.Windows)
	}

	five := snap.Windows[0]
	if five.Kind != usage.WindowFiveHour {
		t.Fatalf("first window = %q, want the 5-hour window", five.Kind)
	}
	if five.Percent == nil || !closeTo(*five.Percent, 0.523905795/14*100) {
		t.Errorf("5-hour percent = %v, want the used/cap share", five.Percent)
	}
	if five.Unit != "USD" || five.Total == nil || !closeTo(*five.Total, 14) {
		t.Errorf("5-hour window = %+v, want a dollar-denominated cap", five)
	}
	if five.ResetsAt == nil {
		t.Error("5-hour window lost its reset time")
	}
	if five.Note != "" {
		t.Errorf("5-hour note = %q, want empty (exceeded is false)", five.Note)
	}

	weekly := snap.Windows[1]
	if weekly.Kind != usage.WindowWeekly || weekly.Percent == nil || !closeTo(*weekly.Percent, 4.993972312/35*100) {
		t.Errorf("weekly window = %+v, want the used/cap share", weekly)
	}

	credits := snap.Windows[2]
	if credits.Kind != usage.WindowBilling {
		t.Fatalf("third window = %q, want the billing lane", credits.Kind)
	}
	if credits.Remaining == nil || !closeTo(*credits.Remaining, 52.4281492675) || credits.Unit != "USD" {
		t.Errorf("billing lane = %+v, want the monthly credit remainder in USD", credits)
	}
	if credits.Percent != nil {
		t.Error("the credit remainder must not invent a percentage")
	}
}

// The defensive key spellings, numeric-string values, an ISO reset and a
// failed subscriptions call.
func TestCommandCodeAcceptsAliasedKeys(t *testing.T) {
	pinClock(t)
	snap, err := newCommandCode().Parse(readFixture(t, "commandcode", "aliases.json"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(snap.Warnings) != 1 || !strings.Contains(snap.Warnings[0], "subscription details unavailable") {
		t.Errorf("warnings = %v, want one about the subscriptions call", snap.Warnings)
	}
	if snap.Plan != "" || snap.Note != "" {
		t.Errorf("plan/note = (%q, %q), want empty without subscriptions", snap.Plan, snap.Note)
	}
	if len(snap.Windows) != 3 {
		t.Fatalf("windows = %+v, want five-hour, weekly and the credit lane", snap.Windows)
	}
	usage.SortWindows(snap.Windows)
	five := snap.Windows[0]
	if five.Kind != usage.WindowFiveHour {
		t.Fatalf("first window = %q, want five-hour (from the five_hour alias)", five.Kind)
	}
	if five.Percent == nil || !closeTo(*five.Percent, 0.5/14*100) {
		t.Errorf("five-hour percent = %v, want 0.5/14", five.Percent)
	}
	if five.ResetsAt == nil {
		t.Error("five-hour window lost its ISO reset time")
	}
	// cap 0: unknown, never 0%.
	weekly := snap.Windows[1]
	if weekly.Kind != usage.WindowWeekly {
		t.Fatalf("second window = %q, want weekly", weekly.Kind)
	}
	if weekly.Percent != nil {
		t.Errorf("weekly percent = %v, want nil for a zero cap", *weekly.Percent)
	}
	if weekly.Note != "blocked" {
		t.Errorf("weekly note = %q, want blocked", weekly.Note)
	}
}

// A reset outside its window is dropped with a warning; the percentage stands.
func TestOpencodeGoDropsImpossibleReset(t *testing.T) {
	pinClock(t)
	snap, err := newOpencodeGo().Parse(readFixture(t, "opencode-go", "reset-beyond-window.json"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(snap.Windows) != 1 {
		t.Fatalf("windows = %+v, want one", snap.Windows)
	}
	win := snap.Windows[0]
	if win.Percent == nil || !closeTo(*win.Percent, 5) {
		t.Errorf("percent = %v, want 5 (the percentage survives a bad reset)", win.Percent)
	}
	if win.ResetsAt != nil {
		t.Error("a reset outside the window must be dropped")
	}
	if len(snap.Warnings) != 1 || !strings.Contains(snap.Warnings[0], "reset time") {
		t.Errorf("warnings = %v, want one about the reset time", snap.Warnings)
	}
}

func TestOpencodeGoParsesDocumentedBody(t *testing.T) {
	pinClock(t)
	snap, err := newOpencodeGo().Parse(readFixture(t, "opencode-go", "go.json"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if snap.Plan != "" {
		t.Errorf("Plan = %q, want empty (opencode go reports no plan name)", snap.Plan)
	}
	usage.SortWindows(snap.Windows)
	if len(snap.Windows) != 3 {
		t.Fatalf("windows = %+v, want the 5h/weekly/monthly trio", snap.Windows)
	}
	for i, want := range []struct {
		kind    usage.WindowKind
		percent float64
	}{
		{usage.WindowFiveHour, 17.4},
		{usage.WindowWeekly, 62},
		{usage.WindowMonthly, 88.1},
	} {
		win := snap.Windows[i]
		if win.Kind != want.kind {
			t.Fatalf("window %d = %q, want %q", i, win.Kind, want.kind)
		}
		if win.Percent == nil || !closeTo(*win.Percent, want.percent) {
			t.Errorf("%s percent = %v, want %v", win.Kind, win.Percent, want.percent)
		}
		if win.ResetsAt == nil {
			t.Errorf("%s lost its reset time", win.Kind)
		}
	}
	// A status worth repeating rides the window; the routine one does not.
	if note := snap.Windows[2].Note; !strings.Contains(note, "limited") {
		t.Errorf("monthly note = %q, want the provider's status", note)
	}
	if note := snap.Windows[0].Note; note != "" {
		t.Errorf("rolling note = %q, want empty for a routine status", note)
	}
}

// Rule 1 end to end: a body with nothing recognizable yields no windows, a
// warning, and no invented 0%.
func TestOpencodeGoEmptyBodyIsUnknownNotZero(t *testing.T) {
	snap, err := newOpencodeGo().Parse(readFixture(t, "opencode-go", "empty.json"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(snap.Windows) != 0 {
		t.Fatalf("windows = %+v, want none", snap.Windows)
	}
	if len(snap.Warnings) == 0 {
		t.Error("an unreadable body must be reported as a warning")
	}
}

func TestParseRejectsMalformedBody(t *testing.T) {
	if _, err := newOpencodeGo().Parse([]byte("not json")); err == nil {
		t.Error("opencode-go Parse accepted a non-JSON body")
	}
	if _, err := newCommandCode().Parse([]byte(`{"credits": "not an object"}`)); err == nil {
		t.Error("commandcode Parse accepted a malformed credits value")
	}
}

func closeTo(got, want float64) bool {
	diff := got - want
	if diff < 0 {
		diff = -diff
	}
	return diff < 1e-6
}
