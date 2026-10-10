// Tests for the [provider."<id>"] connection sections and the model/provider
// inheritance (T8.1, spec wiki/port/provider-config.md §4.1): schema parsing
// with the overloaded "provider" key routed by shape, per-field inheritance
// with the wholesale credential opt-out, and the load-time validation
// warnings.
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/testenv"
)

// TestProviderSectionsParse pins the schema: [provider."<id>"] tables decode
// into the Providers map, and the top-level scalar provider hint keeps
// working on its own — the overloaded "provider" key routes by shape (TOML
// rejects a file carrying both, so one file is one shape).
func TestProviderSectionsParse(t *testing.T) {
	cfg, err := LoadFileConfig(writeTemp(t, `
[provider."sensenova"]
base_url = "https://token.sensenova.cn/v1"
protocol = "openai"
env_key = "SENSENOVA_API_KEY"
proxy = "http://127.0.0.1:7890"

[provider.openrouter-free]
base_url = "https://openrouter.ai/api/v1"
credential = "openrouter"
`))
	if err != nil {
		t.Fatalf("LoadFileConfig: %v", err)
	}
	if cfg.Provider != "" {
		t.Errorf("top-level provider hint = %q, want empty (sections shape)", cfg.Provider)
	}
	if len(cfg.Providers) != 2 {
		t.Fatalf("Providers = %v, want 2 sections", cfg.Providers)
	}
	p, ok := cfg.ProviderFor("sensenova")
	if !ok || p.BaseURL != "https://token.sensenova.cn/v1" || p.EnvKey != "SENSENOVA_API_KEY" || p.Proxy != "http://127.0.0.1:7890" {
		t.Errorf("ProviderFor(sensenova) = %+v, %v; want the full connection", p, ok)
	}
	// Case-insensitive reference + unusable section fails closed.
	if _, ok := cfg.ProviderFor("SenseNova"); !ok {
		t.Error("ProviderFor(SenseNova) should match case-insensitively")
	}
	if _, ok := cfg.ProviderFor("no-such"); ok {
		t.Error("ProviderFor(no-such) should be false")
	}
	// The scalar shape (pre-existing configs) still routes to the hint.
	hint, err := LoadFileConfig(writeTemp(t, "provider = \"deepseek\"\n"))
	if err != nil {
		t.Fatalf("LoadFileConfig(hint): %v", err)
	}
	if hint.Provider != "deepseek" || len(hint.Providers) != 0 {
		t.Errorf("hint shape = provider %q, %d sections; want deepseek / 0", hint.Provider, len(hint.Providers))
	}
	broken, err := LoadFileConfig(writeTemp(t, `
[provider.empty]
protocol = "openai"
[provider.full]
base_url = "https://x.example/v1"
`))
	if err != nil {
		t.Fatalf("LoadFileConfig: %v", err)
	}
	if _, ok := broken.ProviderFor("empty"); ok {
		t.Error("a base_url-less section must not resolve (fail closed)")
	}
	warnings := broken.ValidateProviders()
	if len(warnings) != 1 || !strings.Contains(warnings[0], `"empty"`) {
		t.Errorf("ValidateProviders() = %v, want one warning naming empty", warnings)
	}
}

// TestResolveModelConnectionInheritance pins the per-field inheritance: the
// profile's explicit values win, unset fields inherit the referenced
// provider, and declaring any credential field opts out of credential
// inheritance wholesale (grok alignment — no half-credential assembly).
func TestResolveModelConnectionInheritance(t *testing.T) {
	cfg, err := LoadFileConfig(writeTemp(t, `
[provider."gw"]
base_url = "https://gw.example/v1"
protocol = "openai"
api_key = "sk-provider"
credential = "gw-ref"
env_key = "GW_KEY"
proxy = "http://proxy.example:7890"

[models.inherits]
model = "m1"
provider = "gw"

[models.overrides]
model = "m2"
provider = "gw"
base_url = "https://own.example/v1"
proxy = "http://own-proxy:7891"
credential = "own-ref"

[models.hint-only]
model = "m3"
provider = "openrouter"
`))
	if err != nil {
		t.Fatalf("LoadFileConfig: %v", err)
	}
	conn, ok := cfg.ResolveModelConnection("inherits")
	if !ok || !conn.UsedConfigProvider {
		t.Fatalf("ResolveModelConnection(inherits) = %+v, %v; want a config-provider connection", conn, ok)
	}
	if conn.BaseURL != "https://gw.example/v1" || conn.Protocol != "openai" || conn.Proxy != "http://proxy.example:7890" {
		t.Errorf("connection fields = %+v, want the provider's inherited values", conn)
	}
	if conn.APIKey != "sk-provider" || conn.Credential != "gw-ref" || conn.EnvKey != "GW_KEY" {
		t.Errorf("credentials = %q/%q/%q, want the provider's set", conn.APIKey, conn.Credential, conn.EnvKey)
	}
	// Explicit model fields win; declaring a credential field opts the whole
	// credential group out of inheritance (grok wholesale rule — no
	// half-credential assembly from two sources).
	conn, ok = cfg.ResolveModelConnection("overrides")
	if !ok {
		t.Fatal("ResolveModelConnection(overrides) should resolve")
	}
	if conn.BaseURL != "https://own.example/v1" || conn.Proxy != "http://own-proxy:7891" {
		t.Errorf("override fields = %+v, want the profile's own values", conn)
	}
	if conn.APIKey != "" || conn.EnvKey != "" {
		t.Errorf("credentials = %q/%q/%q, want empty (the profile's own credential opts the group out of inheritance)", conn.APIKey, conn.Credential, conn.EnvKey)
	}
	if conn.Credential != "own-ref" {
		t.Errorf("credential = %q, want the profile's own reference", conn.Credential)
	}
	// A built-in family hint (no config section) resolves with the profile's
	// own fields and UsedConfigProvider=false.
	conn, ok = cfg.ResolveModelConnection("hint-only")
	if !ok || conn.UsedConfigProvider {
		t.Errorf("hint-only connection = %+v, %v; want profile fields without a config provider", conn, ok)
	}
	if _, ok := cfg.ResolveModelConnection("no-such"); ok {
		t.Error("ResolveModelConnection(no-such) should be false")
	}
}

// writeTemp writes a config fixture into the package temp root and returns
// its path.
func writeTemp(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(testenv.Dir(t), strings.ReplaceAll(t.Name(), "/", "_")+".toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}
