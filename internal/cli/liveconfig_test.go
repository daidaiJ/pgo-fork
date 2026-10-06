package cli

import (
	"context"
	"testing"

	"github.com/smallnest/pigo/internal/cli/config"
	"github.com/smallnest/pigo/internal/provider"
)

// fakeProvider returns Models() from a fixed slice so ResolveContextWindow's
// catalog lookup can be exercised without a real gateway.
type fakeProvider struct {
	models []provider.Model
}

func (f fakeProvider) Name() string                          { return "fake" }
func (f fakeProvider) Models() []provider.Model              { return f.models }
func (f fakeProvider) StreamCompletion(ctx context.Context, req provider.CompletionRequest) (*provider.AssistantMessageEventStream, error) {
	return nil, nil
}

// TestResolveContextWindow covers the window-resolution chain (T4.4 slice,
// 2026-10-06): the model's catalog window when known, the conservative
// DefaultContextWindow fallback otherwise, and an explicit [compaction]
// max_context lowering the result (config wins; it never raises).
func TestResolveContextWindow(t *testing.T) {
	prov := fakeProvider{models: []provider.Model{
		{Provider: "qianfan", ID: "ernie-4.5-turbo-32k", ContextWindow: 32000},
		{Provider: "openrouter", ID: "some/unknown-model", ContextWindow: 0},
	}}

	cases := []struct {
		name  string
		model string
		max   string // "" = unset; otherwise a valid ParseMaxContext value
		want  int
	}{
		{"catalog window wins", "ernie-4.5-turbo-32k", "", 32000},
		{"fallback for unknown window", "some/unknown-model", "", DefaultContextWindow},
		{"fallback for unknown model", "totally/unlisted", "", DefaultContextWindow},
		{"fraction lowers catalog window", "ernie-4.5-turbo-32k", "0.80", 25600},
		{"fraction lowers fallback window", "some/unknown-model", "0.5", 64000},
		{"absolute lowers catalog window", "ernie-4.5-turbo-32k", "16K", 16000},
		{"setting above fallback clamps to fallback", "some/unknown-model", "200K", DefaultContextWindow},
		{"setting above window is clamped to window", "ernie-4.5-turbo-32k", "300K", 32000},
	}
	for _, c := range cases {
		var mc config.MaxContext
		if c.max != "" {
			var err error
			mc, err = config.ParseMaxContext(c.max)
			if err != nil {
				t.Fatalf("%s: ParseMaxContext(%q): %v", c.name, c.max, err)
			}
		}
		if got := ResolveContextWindow(prov, c.model, mc); got != c.want {
			t.Errorf("%s: ResolveContextWindow(model=%q, max=%q) = %d, want %d", c.name, c.model, c.max, got, c.want)
		}
	}
}

// TestResolveContextWindow_NilProvider pins the no-provider path: no catalog to
// consult, so the fallback (or the explicit cap lowering it) applies.
func TestResolveContextWindow_NilProvider(t *testing.T) {
	if got := ResolveContextWindow(nil, "anything", config.MaxContext{}); got != DefaultContextWindow {
		t.Errorf("nil provider with no cap = %d, want %d", got, DefaultContextWindow)
	}
	mc, err := config.ParseMaxContext("0.25")
	if err != nil {
		t.Fatal(err)
	}
	if got := ResolveContextWindow(nil, "anything", mc); got != DefaultContextWindow/4 {
		t.Errorf("nil provider with 0.25 cap = %d, want %d", got, DefaultContextWindow/4)
	}
}

// TestResolveMaxOutputTokens covers the output-cap resolution chain (T4.4 余项):
// the model's catalog cap when declared, 0 (inert) when unknown — never a guess.
func TestResolveMaxOutputTokens(t *testing.T) {
	prov := fakeProvider{models: []provider.Model{
		{Provider: "anthropic", ID: "claude-x", MaxOutputTokens: 12000},
		{Provider: "openai", ID: "gpt-y", MaxOutputTokens: 0},
	}}
	if got := ResolveMaxOutputTokens(prov, "claude-x"); got != 12000 {
		t.Errorf("declared cap = %d, want 12000", got)
	}
	if got := ResolveMaxOutputTokens(prov, "gpt-y"); got != 0 {
		t.Errorf("undeclared cap = %d, want 0", got)
	}
	if got := ResolveMaxOutputTokens(prov, "totally/unlisted"); got != 0 {
		t.Errorf("unknown model cap = %d, want 0", got)
	}
	if got := ResolveMaxOutputTokens(nil, "claude-x"); got != 0 {
		t.Errorf("nil provider cap = %d, want 0", got)
	}
}
