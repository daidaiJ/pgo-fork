package provider

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/smallnest/pigo/internal/testenv"
)

// writeAuthStore writes an OpenCode-style auth.json into a fresh test dir and
// returns the path.
func writeAuthStore(t *testing.T, body string) string {
	t.Helper()
	dir := testenv.Dir(t)
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write auth store: %v", err)
	}
	return path
}

func TestDiscoverOpenCodeAuthReadsProviderEntry(t *testing.T) {
	path := writeAuthStore(t, `{
		"anthropic": {"type": "oauth", "access": "not-this-one"},
		"opencode-go": {"type": "api", "key": "sk-oc-go"},
		"command-code": {"type": "api", "key": "sk-cc"}
	}`)

	cases := []struct {
		provider string
		want     string
	}{
		{provider: "opencode-go", want: "sk-oc-go"},
		{provider: "commandcode", want: "sk-cc"},
		{provider: "command-code", want: "sk-cc"},
		{provider: "opencode", want: ""},
		{provider: "openrouter", want: ""},
		{provider: "opencode-zen", want: ""},
	}
	for _, tc := range cases {
		if got := discoverOpenCodeAuth(tc.provider, []string{path}); got != tc.want {
			t.Errorf("discoverOpenCodeAuth(%q) = %q, want %q", tc.provider, got, tc.want)
		}
	}
}

func TestDiscoverOpenCodeAuthAcceptsStoreVariants(t *testing.T) {
	// A bare string per entry, and the alternate key field names.
	bare := writeAuthStore(t, `{"opencode-go": "sk-bare"}`)
	if got := discoverOpenCodeAuth("opencode-go", []string{bare}); got != "sk-bare" {
		t.Errorf("bare string entry = %q, want sk-bare", got)
	}
	token := writeAuthStore(t, `{"opencode-go": {"type": "oauth", "token": "sk-token"}}`)
	if got := discoverOpenCodeAuth("opencode-go", []string{token}); got != "sk-token" {
		t.Errorf("token field = %q, want sk-token", got)
	}
	// An entry present but empty falls through to the next candidate path.
	empty := writeAuthStore(t, `{"opencode-go": {"type": "api"}}`)
	real := writeAuthStore(t, `{"opencode-go": {"key": "sk-later"}}`)
	if got := discoverOpenCodeAuth("opencode-go", []string{empty, real}); got != "sk-later" {
		t.Errorf("fallback across paths = %q, want sk-later", got)
	}
}

func TestDiscoverOpenCodeAuthToleratesMissingAndBrokenStores(t *testing.T) {
	dir := testenv.Dir(t)
	paths := []string{filepath.Join(dir, "absent.json")}
	if got := discoverOpenCodeAuth("opencode-go", paths); got != "" {
		t.Errorf("missing store = %q, want empty", got)
	}
	broken := writeAuthStore(t, `{not json`)
	if got := discoverOpenCodeAuth("opencode-go", []string{broken}); got != "" {
		t.Errorf("unreadable store = %q, want empty", got)
	}
	if got := discoverOpenCodeAuth("opencode-go", nil); got != "" {
		t.Errorf("no candidate paths = %q, want empty", got)
	}
}

func TestOpenCodeAuthPathsArePlausible(t *testing.T) {
	paths := opencodeAuthPaths()
	if len(paths) == 0 {
		t.Fatal("no candidate auth.json paths")
	}
	for _, p := range paths {
		if filepath.Base(filepath.Dir(p)) != "opencode" || filepath.Base(p) != "auth.json" {
			t.Errorf("candidate path %q is not an opencode/auth.json", p)
		}
	}
}

// The store consults discovery last: an explicit key of pigo's own always wins,
// and a discovered key is memoized (read once per process).
func TestCredentialStoreDiscoveryPrecedence(t *testing.T) {
	ctx := context.Background()
	calls := 0
	store := NewCredentialStore(&APIKeyConfig{Keys: map[string]string{"opencode-go": "sk-config"}})
	store.SetDiscovery(func(provider string) string {
		calls++
		if provider == "opencode-go" {
			return "sk-discovered"
		}
		return ""
	})

	if got := store.GetAPIKey(ctx, "opencode-go"); got != "sk-config" {
		t.Errorf("config key lost to discovery: %q", got)
	}
	store.SetOverride("opencode-go", "sk-override")
	if got := store.GetAPIKey(ctx, "opencode-go"); got != "sk-override" {
		t.Errorf("override lost to discovery: %q", got)
	}

	// A provider with no key of its own falls through to discovery...
	if got := store.GetAPIKey(ctx, "commandcode"); got != "" {
		t.Errorf("commandcode = %q, want empty", got)
	}
	// ...and each provider is asked at most once.
	before := calls
	for i := 0; i < 3; i++ {
		store.GetAPIKey(ctx, "commandcode")
	}
	if calls != before {
		t.Errorf("discovery called %d extra times; the answer must be memoized", calls-before)
	}

	// No discovery layer at all (a store built for a credential-free path)
	// still resolves its own keys.
	plain := NewCredentialStore(nil)
	plain.SetDiscovery(nil)
	plain.SetOverride("opencode-go", "sk-only")
	if got := plain.GetAPIKey(ctx, "opencode-go"); got != "sk-only" {
		t.Errorf("store without discovery = %q, want sk-only", got)
	}
}

// HasCredential reflects a discovered key too: the seam that decides whether a
// provider is usable without an explicit configuration.
func TestHasCredentialSeesDiscoveredKey(t *testing.T) {
	store := NewCredentialStore(nil)
	store.SetDiscovery(func(provider string) string {
		if provider == "opencode-go" {
			return "sk-discovered"
		}
		return ""
	})
	if !store.HasCredential(context.Background(), "opencode-go") {
		t.Error("HasCredential missed a discovered key")
	}
	if store.HasCredential(context.Background(), "openrouter") {
		t.Error("HasCredential invented a credential")
	}
}
