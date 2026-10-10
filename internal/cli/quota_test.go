package cli

import (
	"errors"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/usage"
)

func TestQuotaProbeLabel(t *testing.T) {
	cases := []struct {
		probe QuotaProbe
		want  string
	}{
		{probe: QuotaProbe{Provider: "opencode-go"}, want: "opencode-go"},
		{probe: QuotaProbe{Provider: "custom", BaseURL: "https://api.commandcode.ai/provider/v1"}, want: "api.commandcode.ai"},
		{probe: QuotaProbe{BaseURL: "https://api.commandcode.ai/provider/v1"}, want: "api.commandcode.ai"},
		{probe: QuotaProbe{}, want: "provider"},
	}
	for _, tc := range cases {
		if got := tc.probe.Label(); got != tc.want {
			t.Errorf("QuotaProbe%+v.Label() = %q, want %q", tc.probe, got, tc.want)
		}
	}
}

func TestEffectiveBaseURL(t *testing.T) {
	// A bare registry provider has no base URL of its own; the registry's
	// default is what the host matching needs.
	if got := EffectiveBaseURL("opencode-go", ""); got != "https://opencode.ai/zen/go" {
		t.Errorf("EffectiveBaseURL(opencode-go) = %q, want the registry default", got)
	}
	// An explicit connection always wins.
	if got := EffectiveBaseURL("opencode-go", "https://proxy.example/zen/go"); got != "https://proxy.example/zen/go" {
		t.Errorf("EffectiveBaseURL kept the registry default over an explicit URL: %q", got)
	}
	// An unknown provider with no URL has nothing to offer.
	if got := EffectiveBaseURL("sensenova", ""); got != "" {
		t.Errorf("EffectiveBaseURL(sensenova) = %q, want empty", got)
	}
}

// An unsupported provider is refused before any credential work, so a plain
// /usage on a provider without a quota source stays instant.
func TestProbeQuotaUnsupportedIsImmediate(t *testing.T) {
	start := time.Now()
	_, err := ProbeQuota(QuotaProbe{Provider: "sensenova", BaseURL: "https://token.sensenova.cn/v1"})
	if !errors.Is(err, usage.ErrUnsupported) {
		t.Fatalf("ProbeQuota error = %v, want ErrUnsupported", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("unsupported provider took %s, want an immediate refusal", elapsed)
	}
}

// A provider with a source but no credential is reported as such, and the
// lookup stops before the network.
func TestProbeQuotaWithoutCredential(t *testing.T) {
	_, err := ProbeQuota(QuotaProbe{Provider: "opencode-go"})
	if !errors.Is(err, usage.ErrNoCredential) {
		t.Fatalf("ProbeQuota without a key = %v, want ErrNoCredential", err)
	}
}

func TestQuotaSectionForSkipsSessionsWithoutProvider(t *testing.T) {
	if sec := QuotaSectionFor(nil, nil); sec != nil {
		t.Errorf("QuotaSectionFor(nil) = %+v, want nil", sec)
	}
	if sec := QuotaSectionFor(&LiveConfig{}, nil); sec != nil {
		t.Errorf("QuotaSectionFor(empty live config) = %+v, want nil", sec)
	}
}

// The probe's key comes from the credential store, including a key discovered
// in another agent CLI's store — the zero-input route.
func TestQuotaProbeForUsesDiscoveredCredential(t *testing.T) {
	creds := provider.NewCredentialStore(nil)
	creds.SetDiscovery(func(provider string) string {
		if provider == "opencode-go" {
			return "sk-discovered"
		}
		return ""
	})
	probe := QuotaProbeFor(&LiveConfig{ProviderName: "opencode-go"}, creds)
	if probe.APIKey != "sk-discovered" {
		t.Errorf("probe key = %q, want the discovered credential", probe.APIKey)
	}
	if probe.BaseURL != "https://opencode.ai/zen/go" {
		t.Errorf("probe base URL = %q, want the registry default", probe.BaseURL)
	}
	if probe.Label() != "opencode-go" {
		t.Errorf("probe label = %q", probe.Label())
	}
}

// The section keeps the provider identity only when a snapshot failed to
// resolve; a resolved one is labelled from its own source name.
func TestQuotaSectionForLabelsFailure(t *testing.T) {
	creds := provider.NewCredentialStore(nil)
	sec := QuotaSectionFor(&LiveConfig{ProviderName: "custom", BaseURL: "https://api.commandcode.ai/provider/v1"}, creds)
	if sec == nil {
		t.Fatal("QuotaSectionFor returned nil for a config-defined provider")
	}
	if sec.Label != "api.commandcode.ai" {
		t.Errorf("section label = %q, want the endpoint host", sec.Label)
	}
	if !errors.Is(sec.Err, usage.ErrNoCredential) {
		t.Errorf("section error = %v, want ErrNoCredential", sec.Err)
	}
}
