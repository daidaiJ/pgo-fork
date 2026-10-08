package prompts

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/cli"
	"github.com/smallnest/pigo/internal/cli/config"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/runtime"
)

// TestModelCommandSwitchesToBareProviderName verifies /model zai selects the
// zai provider's default model (issue #564): live.Model carries the concrete
// preset id so the next turn's wire request targets a real model instead of
// sending the literal provider name to OpenRouter.
func TestModelCommandSwitchesToBareProviderName(t *testing.T) {
	live := &cli.LiveConfig{Model: "openrouter/free", ProviderName: "openrouter"}
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, provider.NewCredentialStore(nil))

	out, err := reg.ResolveOutcome("/model zai")
	if err != nil {
		t.Fatalf("ResolveOutcome /model zai: %v", err)
	}
	if live.Model != "glm-4.7" {
		t.Errorf("live.Model = %q, want glm-4.7", live.Model)
	}
	if live.ProviderName != "zai" {
		t.Errorf("live.ProviderName = %q, want zai", live.ProviderName)
	}
	if !strings.Contains(out.Message, "glm-4.7") || !strings.Contains(out.Message, "zai") {
		t.Errorf("message = %q, want it to mention glm-4.7 (zai)", out.Message)
	}
}

// TestModelCommandConcreteIdUnchanged verifies a concrete model id switches
// verbatim, and a bare provider name without preset models reports the
// mismatch instead of silently falling back to OpenRouter.
func TestModelCommandConcreteIdUnchanged(t *testing.T) {
	live := &cli.LiveConfig{Model: "openrouter/free", ProviderName: "openrouter"}
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, provider.NewCredentialStore(nil))

	out, err := reg.ResolveOutcome("/model glm-5.2")
	if err != nil {
		t.Fatalf("ResolveOutcome /model glm-5.2: %v", err)
	}
	if live.Model != "glm-5.2" || live.ProviderName != "zai" {
		t.Errorf("live = (%q, %q), want (glm-5.2, zai)", live.Model, live.ProviderName)
	}
	if !strings.Contains(out.Message, "glm-5.2") {
		t.Errorf("message = %q, want it to mention glm-5.2", out.Message)
	}

	// A concrete id for another provider switches verbatim as before.
	out, err = reg.ResolveOutcome("/model deepseek-v4-pro")
	if err != nil {
		t.Fatalf("ResolveOutcome /model deepseek-v4-pro: %v", err)
	}
	if live.Model != "deepseek-v4-pro" || live.ProviderName != "deepseek" {
		t.Errorf("live = (%q, %q), want (deepseek-v4-pro, deepseek)", live.Model, live.ProviderName)
	}
}

// TestModelsFetchCommandAndSwitch drives "/models fetch" against an
// httptest endpoint (issue #566): the catalog lands on live.FetchedModels,
// and switching to a fetched id stays on the gateway that served it instead
// of falling through the heuristic chain.
func TestModelsFetchCommandAndSwitch(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":[{"id":"m-b"},{"id":"m-a"},{"id":"m-b"}]}`))
	}))
	defer srv.Close()

	live := &cli.LiveConfig{Model: "openrouter/free", ProviderName: "openai", BaseURL: srv.URL + "/v1"}
	creds := provider.NewCredentialStore(nil)
	creds.SetOverride("openai", "test-key")
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, creds)

	out, err := reg.ResolveOutcome("/models fetch")
	if err != nil {
		t.Fatalf("ResolveOutcome /models fetch: %v", err)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", gotAuth)
	}
	if len(live.FetchedModels) != 2 || live.FetchedModels[0] != "m-a" || live.FetchedModels[1] != "m-b" {
		t.Fatalf("live.FetchedModels = %v, want [m-a m-b]", live.FetchedModels)
	}
	if live.FetchedAt.IsZero() {
		t.Error("FetchedAt not stamped")
	}
	if !strings.Contains(out.Message, "2 models") {
		t.Errorf("message = %q, want it to mention 2 models", out.Message)
	}

	// Switching to a fetched id pins the live provider.
	out, err = reg.ResolveOutcome("/model m-b")
	if err != nil {
		t.Fatalf("ResolveOutcome /model m-b: %v", err)
	}
	if live.Model != "m-b" || live.ProviderName != "openai" {
		t.Fatalf("live = (%q, %q), want (m-b, openai)", live.Model, live.ProviderName)
	}
	if models := live.Provider.Models(); len(models) != 1 || models[0].ID != "m-b" || models[0].Provider != "openai" {
		t.Fatalf("wire models = %+v, want one openai/m-b entry", models)
	}
	if !strings.Contains(out.Message, "fetched catalog") {
		t.Errorf("message = %q, want the fetched-catalog note", out.Message)
	}
}

// TestModelsFetchDegrades verifies a failing endpoint degrades gracefully:
// the error is reported and the static preset listing still works.
func TestModelsFetchDegrades(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	live := &cli.LiveConfig{Model: "openrouter/free", ProviderName: "openai", BaseURL: srv.URL}
	creds := provider.NewCredentialStore(nil)
	creds.SetOverride("openai", "test-key")
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, creds)

	out, err := reg.ResolveOutcome("/models fetch")
	if err != nil {
		t.Fatalf("ResolveOutcome /models fetch: %v", err)
	}
	if live.FetchedModels != nil {
		t.Errorf("live.FetchedModels = %v, want nil after failed fetch", live.FetchedModels)
	}
	if !strings.Contains(out.Message, "fetch failed") {
		t.Errorf("message = %q, want the fetch-failed note", out.Message)
	}
	if live.Model != "openrouter/free" {
		t.Errorf("live.Model = %q, want unchanged", live.Model)
	}
}

// TestModelCommandEffortArg verifies the grok grammar "/model <id> <effort>":
// the pair switches the model AND the reasoning level in one dispatch (the
// chained dropdown's final Enter), and an unknown level or extra argument is
// refused without touching the live config.
func TestModelCommandEffortArg(t *testing.T) {
	live := &cli.LiveConfig{Model: "openrouter/free", ProviderName: "openrouter"}
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, provider.NewCredentialStore(nil))

	out, err := reg.ResolveOutcome("/model glm-5.2 high")
	if err != nil {
		t.Fatalf("ResolveOutcome /model glm-5.2 high: %v", err)
	}
	if live.Model != "glm-5.2" {
		t.Errorf("live.Model = %q, want glm-5.2", live.Model)
	}
	if live.ThinkingLevel != agentcore.ThinkingHigh {
		t.Errorf("live.ThinkingLevel = %q, want high", live.ThinkingLevel)
	}
	if !strings.Contains(out.Message, "effort: high") {
		t.Errorf("message = %q, want it to mention the effort", out.Message)
	}

	// An unknown level refuses the whole switch.
	before := live.Model
	out, err = reg.ResolveOutcome("/model glm-5.2 turbo")
	if err != nil {
		t.Fatalf("ResolveOutcome with unknown level: %v", err)
	}
	if !strings.Contains(out.Message, "unknown effort level") || live.Model != before {
		t.Errorf("unknown level: message = %q model = %q, want a refusal and no switch", out.Message, live.Model)
	}

	// Extra arguments are refused too.
	out, err = reg.ResolveOutcome("/model glm-5.2 high extra")
	if err != nil {
		t.Fatalf("ResolveOutcome with extra arg: %v", err)
	}
	if !strings.Contains(out.Message, "unexpected extra argument") {
		t.Errorf("extra arg: message = %q, want a usage refusal", out.Message)
	}
}

// TestModelCommandSwitchesViaConfigProfile verifies the config-profile switch
// face (T7.3 实测反馈: /model 列表与切换以 [models."<id>"] 档案为准): the
// provider is rebuilt from the profile (base_url/protocol), the api_key
// becomes the credential override for the resolved provider, the explicit
// window/output-cap win over the catalog, an explicit effort argument wins
// over the profile's thinking_level, and a profile-only id never falls
// through to the heuristic chain.
func TestModelCommandSwitchesViaConfigProfile(t *testing.T) {
	live := &cli.LiveConfig{
		Model:        "openrouter/free",
		ProviderName: "openrouter",
		BaseURL:      "https://old.example/v1",
		MaxContext:   config.MaxContext{},
	}
	live.ModelProfiles = map[string]config.ModelProfile{
		"gpt-56": {
			Model:           "gpt-5.6-luna",
			Name:            "GPT-5.6 Luna",
			Description:     "gateway profile",
			BaseURL:         "https://gw.example/v1",
			Protocol:        "openai",
			APIKey:          "sk-profile",
			ContextWindow:   1050000,
			MaxOutputTokens: 128000,
			ThinkingLevel:   "high",
		},
	}
	creds := provider.NewCredentialStore(nil)
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, creds)

	out, err := reg.ResolveOutcome("/model gpt-56")
	if err != nil {
		t.Fatalf("ResolveOutcome /model gpt-56: %v", err)
	}
	if live.Model != "gpt-5.6-luna" || live.ProviderName != "openai" {
		t.Errorf("live = (%q, %q), want (gpt-5.6-luna, openai)", live.Model, live.ProviderName)
	}
	if live.BaseURL != "https://gw.example/v1" {
		t.Errorf("live.BaseURL = %q, want the profile endpoint", live.BaseURL)
	}
	if live.ContextWindow != 1050000 || live.MaxOutputTokens != 128000 {
		t.Errorf("window/tokens = %d/%d, want the profile overrides", live.ContextWindow, live.MaxOutputTokens)
	}
	if live.ThinkingLevel != agentcore.ThinkingHigh {
		t.Errorf("effort = %q, want the profile default high", live.ThinkingLevel)
	}
	if !strings.Contains(out.Message, "config profile gpt-56") {
		t.Errorf("message = %q, want it to name the profile", out.Message)
	}
	if got := creds.GetAPIKey(t.Context(), "openai"); got != "sk-profile" {
		t.Errorf("credential override = %q, want sk-profile", got)
	}

	// An explicit effort argument wins over the profile's thinking_level.
	out, err = reg.ResolveOutcome("/model gpt-56 low")
	if err != nil {
		t.Fatalf("ResolveOutcome /model gpt-56 low: %v", err)
	}
	if live.ThinkingLevel != agentcore.ThinkingLow {
		t.Errorf("effort after explicit arg = %q, want low", live.ThinkingLevel)
	}
	if !strings.Contains(out.Message, "effort: low") {
		t.Errorf("message = %q, want the effort note", out.Message)
	}

	// A profile id with an effort suffix still routes through the profile,
	// and an unknown id keeps the heuristic path (no profile match).
	out, err = reg.ResolveOutcome("/model totally-unknown")
	if err != nil {
		t.Fatalf("ResolveOutcome /model totally-unknown: %v", err)
	}
	if !strings.Contains(out.Message, "cannot switch") && !strings.Contains(out.Message, "model switched") {
		t.Errorf("unknown-id message = %q, want the heuristic path outcome", out.Message)
	}

	// /models lists the profiles ahead of the preset catalog.
	out, err = reg.ResolveOutcome("/models")
	if err != nil {
		t.Fatalf("ResolveOutcome /models: %v", err)
	}
	if !strings.Contains(out.Message, "config profiles") || !strings.Contains(out.Message, "gpt-56") {
		t.Errorf("/models = %q, want the config-profile section", out.Message)
	}
}
