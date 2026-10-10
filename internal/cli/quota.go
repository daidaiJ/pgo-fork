package cli

import (
	"context"
	"strings"
	"time"

	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/usage"
	// The quota sources register themselves on import (spec §2.2): a provider
	// is supported because its file exists, not because a registry line was
	// added here.
	_ "github.com/smallnest/pigo/internal/usage/sources"
)

// QuotaTimeout bounds the whole provider-quota lookup. /usage is synchronous
// in the REPL and a bounded network step in the TUI, so the lookup must be
// short and fail soft: a slow provider costs a bounded delay, never a hung
// command. Measured against the live Command Code endpoint the proxied route
// takes ~2s, so this leaves room for a spike without approaching the tens of
// seconds a direct (proxyless) route needs.
const QuotaTimeout = 8 * time.Second

// QuotaProbe identifies the provider connection a quota lookup asks about.
type QuotaProbe struct {
	Provider string
	BaseURL  string
	APIKey   string
}

// Label names the probe for display: the provider name when it carries
// identity, else the base URL's host. A provider declared through
// [provider."<id>"] resolves to the generic "custom" driver (T8.1), which is
// not a name worth showing.
func (p QuotaProbe) Label() string {
	if name := strings.TrimSpace(p.Provider); name != "" && !strings.EqualFold(name, "custom") {
		return name
	}
	if host := usage.Host(p.BaseURL); host != "" {
		return host
	}
	return "provider"
}

// ProbeQuota resolves the probe's plan quota under QuotaTimeout.
func ProbeQuota(p QuotaProbe) (*usage.Snapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), QuotaTimeout)
	defer cancel()
	return usage.Query(ctx, usage.Credential{Provider: p.Provider, BaseURL: p.BaseURL, Key: p.APIKey})
}

// QuotaProbeFor builds the probe for a session's live provider. The key comes
// from the credential store, so a --api-key flag, environment variable, config
// entry, or a key discovered in another agent CLI's store all serve the
// lookup without /usage knowing which one it was.
func QuotaProbeFor(live *LiveConfig, creds *provider.CredentialStore) QuotaProbe {
	if live == nil {
		return QuotaProbe{}
	}
	var key string
	if creds != nil {
		key = creds.GetAPIKey(context.Background(), live.ProviderName)
	}
	return QuotaProbe{
		Provider: live.ProviderName,
		BaseURL:  EffectiveBaseURL(live.ProviderName, live.BaseURL),
		APIKey:   key,
	}
}

// QuotaSectionFor probes the live provider's plan quota for /usage. It returns
// nil when the session carries no provider identity at all; otherwise the
// section holds either a snapshot or the reason there is none, which the
// renderer reports or stays quiet about.
func QuotaSectionFor(live *LiveConfig, creds *provider.CredentialStore) *QuotaSection {
	if live == nil || (live.ProviderName == "" && live.BaseURL == "") {
		return nil
	}
	probe := QuotaProbeFor(live, creds)
	snap, err := ProbeQuota(probe)
	return QuotaSectionFrom(probe, snap, err)
}

// QuotaSupported reports whether the provider has a quota source at all. It is
// the cheap pre-check that keeps a provider without one (sensenova, openrouter,
// …) from starting a lookup that could only fail, so its panel tab stays quiet.
func QuotaSupported(probe QuotaProbe) bool {
	_, ok := usage.Lookup(probe.Provider, probe.BaseURL)
	return ok
}

// QuotaSectionFrom builds the section for a finished lookup, naming the
// provider from the source that served it. That name is the useful one: a
// connection declared through [provider."<id>"] resolves to the generic
// "custom"/"openai" driver, so only the source knows the plan belongs to
// "commandcode".
func QuotaSectionFrom(probe QuotaProbe, snap *usage.Snapshot, err error) *QuotaSection {
	label := probe.Label()
	if src, ok := usage.Lookup(probe.Provider, probe.BaseURL); ok {
		label = src.Name()
	}
	return &QuotaSection{Label: label, Snapshot: snap, Err: err}
}

// EffectiveBaseURL fills in a registry provider's default base URL when the
// connection carries none, so the host half of source matching sees the real
// endpoint (a bare `--provider opencode-go` has no base URL of its own).
func EffectiveBaseURL(providerName, baseURL string) string {
	if strings.TrimSpace(baseURL) != "" {
		return baseURL
	}
	if spec, ok := provider.LookupProviderSpec(providerName); ok {
		return spec.DefaultBaseURL
	}
	return ""
}
