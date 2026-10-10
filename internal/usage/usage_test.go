package usage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSplitBaseAndHost(t *testing.T) {
	cases := []struct {
		in         string
		wantHost   string
		wantPath   string
		wantNoHost bool
	}{
		{in: "https://opencode.ai/zen/go", wantHost: "opencode.ai", wantPath: "/zen/go"},
		{in: "https://OpenCode.AI/zen/go/", wantHost: "opencode.ai", wantPath: "/zen/go/"},
		{in: "api.commandcode.ai/provider/v1", wantHost: "api.commandcode.ai", wantPath: "/provider/v1"},
		{in: "https://api.commandcode.ai:443/x", wantHost: "api.commandcode.ai", wantPath: "/x"},
		{in: "https://user@host.example/v1", wantHost: "host.example", wantPath: "/v1"},
		{in: "https://127.0.0.1:8080/v1", wantHost: "127.0.0.1", wantPath: "/v1"},
		{in: "   ", wantNoHost: true},
		{in: "/just/a/path", wantNoHost: true},
	}
	for _, tc := range cases {
		host, path := SplitBase(tc.in)
		if tc.wantNoHost {
			if host != "" {
				t.Errorf("SplitBase(%q) host = %q, want empty", tc.in, host)
			}
			continue
		}
		if host != tc.wantHost || path != tc.wantPath {
			t.Errorf("SplitBase(%q) = (%q, %q), want (%q, %q)", tc.in, host, path, tc.wantHost, tc.wantPath)
		}
		if got := Host(tc.in); got != tc.wantHost {
			t.Errorf("Host(%q) = %q, want %q", tc.in, got, tc.wantHost)
		}
	}
}

// Rule 1: an unknown or non-positive limit is unknown, never zero.
func TestUsedPercentUnknownNeverZero(t *testing.T) {
	// The spec's negative-control name: an unknown limit yields nil, never 0.
	for _, limit := range []float64{0, -1} {
		if p := UsedPercent(5, limit); p != nil {
			t.Errorf("UsedPercent(5, %v) = %v, want nil (unknown is not 0%%)", limit, *p)
		}
	}
	if p := UsedPercent(7, 14); p == nil || *p != 50 {
		t.Fatalf("UsedPercent(7, 14) = %v, want 50", p)
	}
}

// Rule 2: percentages are clamped, and a remaining-share response inverts.
func TestPercentClampsAndInverts(t *testing.T) {
	if p := Percent(104.5); p == nil || *p != 100 {
		t.Errorf("Percent(104.5) = %v, want 100", p)
	}
	if p := Percent(-3); p == nil || *p != 0 {
		t.Errorf("Percent(-3) = %v, want 0", p)
	}
	if p := UsedFromRemainingPercent(17); p == nil || *p != 83 {
		t.Errorf("UsedFromRemainingPercent(17) = %v, want 83", p)
	}
}

// Rule 3: a reset outside its own window, or already past, is dropped with a
// warning rather than rendered as a confident countdown.
func TestCheckResetLegitimacy(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	inWindow := now.Add(3 * time.Hour)
	got, warn := CheckReset(inWindow, WindowFiveHour, now)
	if got == nil || !got.Equal(inWindow) || warn != "" {
		t.Errorf("in-window reset = (%v, %q), want kept silently", got, warn)
	}

	// Skew tolerance: one second inside the window plus the minute of skew.
	edge := now.Add(MaxDuration(WindowFiveHour) + ResetSkew - time.Second)
	if got, warn := CheckReset(edge, WindowFiveHour, now); got == nil || warn != "" {
		t.Errorf("edge reset = (%v, %q), want kept", got, warn)
	}

	beyond := now.Add(MaxDuration(WindowFiveHour) + 2*ResetSkew)
	if got, warn := CheckReset(beyond, WindowFiveHour, now); got != nil || warn == "" {
		t.Errorf("beyond-window reset = (%v, %q), want dropped with a warning", got, warn)
	}

	past := now.Add(-2 * time.Minute)
	if got, warn := CheckReset(past, WindowWeekly, now); got != nil || warn == "" {
		t.Errorf("past reset = (%v, %q), want dropped with a warning", got, warn)
	}

	// A kind with no known length cannot be judged, so the value is kept.
	if got, warn := CheckReset(now.Add(90*24*time.Hour), WindowOther, now); got == nil || warn != "" {
		t.Errorf("unknown-length reset = (%v, %q), want kept", got, warn)
	}
	if got, warn := CheckReset(time.Time{}, WindowFiveHour, now); got != nil || warn != "" {
		t.Errorf("zero reset = (%v, %q), want nil without a warning", got, warn)
	}
}

// Rules 4 and 5: shortest first, unknown lengths last, the independent tool
// lane after every model window.
func TestSortWindowsOrdersByLength(t *testing.T) {
	ws := []Window{
		{Kind: WindowMCP},
		{Kind: WindowMonthly},
		{Kind: WindowOther},
		{Kind: WindowWeekly},
		{Kind: WindowFiveHour},
	}
	SortWindows(ws)
	want := []WindowKind{WindowFiveHour, WindowWeekly, WindowMonthly, WindowOther, WindowMCP}
	for i, k := range want {
		if ws[i].Kind != k {
			t.Fatalf("window %d = %q, want %q (order: %v)", i, ws[i].Kind, k, ws)
		}
	}
}

// fakeSource is a registered Source used to exercise lookup and Query without
// touching a real provider.
type fakeSource struct {
	name     string
	provider string
	host     string
	body     []byte
	fetchErr error
	parseErr error
}

func (f *fakeSource) Name() string { return f.name }

func (f *fakeSource) MatchProvider(provider string) bool {
	return f.provider != "" && f.provider == provider
}

func (f *fakeSource) MatchBase(baseURL string) bool {
	return f.host != "" && Host(baseURL) == f.host
}

func (f *fakeSource) Fetch(context.Context, Credential) ([]byte, error) {
	if f.fetchErr != nil {
		return nil, f.fetchErr
	}
	return f.body, nil
}

func (f *fakeSource) Parse([]byte) (*Snapshot, error) {
	if f.parseErr != nil {
		return nil, f.parseErr
	}
	return &Snapshot{Windows: []Window{
		{Kind: WindowMonthly},
		{Kind: WindowFiveHour},
	}}, nil
}

func registerFake(t *testing.T, f *fakeSource) *fakeSource {
	t.Helper()
	Register(f)
	return f
}

func TestLookupPrefersProviderNameOverHost(t *testing.T) {
	byName := registerFake(t, &fakeSource{name: "fake-name", provider: "fakequota"})
	byHost := registerFake(t, &fakeSource{name: "fake-host", host: "quota.test"})

	got, ok := Lookup("fakequota", "https://quota.test/v1")
	if !ok || got.Name() != byName.name {
		t.Fatalf("Lookup by name = (%v, %v), want %s", got, ok, byName.name)
	}
	got, ok = Lookup("custom", "https://quota.test/v1")
	if !ok || got.Name() != byHost.name {
		t.Fatalf("Lookup by host = (%v, %v), want %s", got, ok, byHost.name)
	}
	if _, ok := Lookup("sensenova", ""); ok {
		t.Error("Lookup(sensenova) matched a source; an unsupported provider must stay unsupported")
	}
	if _, ok := Lookup("", ""); ok {
		t.Error("Lookup with no identity matched a source")
	}
}

func TestQueryReportsUnsupportedAndMissingCredential(t *testing.T) {
	_, err := Query(context.Background(), Credential{Provider: "sensenova"})
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("unsupported provider error = %v, want ErrUnsupported", err)
	}
	registerFake(t, &fakeSource{name: "fake-cred", provider: "fakecred"})
	_, err = Query(context.Background(), Credential{Provider: "fakecred"})
	if !errors.Is(err, ErrNoCredential) {
		t.Errorf("missing credential error = %v, want ErrNoCredential", err)
	}
	// The message names the provider, and never a key.
	if err != nil && containsSecret(err.Error()) {
		t.Errorf("error text leaked a credential: %q", err)
	}
}

// Query stamps the source, orders the windows and keeps the provider's own
// errors out of the returned snapshot.
func TestQueryStampsSourceAndOrdersWindows(t *testing.T) {
	registerFake(t, &fakeSource{name: "fake-ok", provider: "fakeok", body: []byte(`{}`)})
	snap, err := Query(context.Background(), Credential{Provider: "fakeok", Key: "sk-test"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if snap.Source != "fake-ok" {
		t.Errorf("Snapshot.Source = %q, want fake-ok", snap.Source)
	}
	if snap.FetchedAt.IsZero() {
		t.Error("Snapshot.FetchedAt not stamped")
	}
	want := []WindowKind{WindowFiveHour, WindowMonthly}
	for i, k := range want {
		if snap.Windows[i].Kind != k {
			t.Fatalf("window %d = %q, want %q", i, snap.Windows[i].Kind, k)
		}
	}

	registerFake(t, &fakeSource{name: "fake-fail", provider: "fakefail", fetchErr: errors.New("boom")})
	if _, err := Query(context.Background(), Credential{Provider: "fakefail", Key: "sk-secret-xyz"}); err == nil {
		t.Fatal("Query with a failing fetch returned no error")
	} else {
		if !containsText(err.Error(), "fake-fail") {
			t.Errorf("fetch error %q does not name the source", err)
		}
		if containsSecret(err.Error()) {
			t.Errorf("fetch error leaked the credential: %q", err)
		}
	}

	registerFake(t, &fakeSource{name: "fake-parse", provider: "fakeparse", parseErr: errors.New("bad body")})
	if _, err := Query(context.Background(), Credential{Provider: "fakeparse", Key: "sk-test"}); err == nil {
		t.Fatal("Query with a failing parse returned no error")
	}
}

func containsText(s, sub string) bool { return strings.Contains(s, sub) }

func containsSecret(s string) bool { return strings.Contains(s, "sk-") }
