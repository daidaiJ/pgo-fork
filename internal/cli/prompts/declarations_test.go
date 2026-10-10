// This file holds the Parse face of the T7.7 slice-2 contract commands (/memory,
// /rebuild) and the pins for the loop-face declarations: the former stub table
// is identity + Projection now, /help and the menus render from it, and a face
// the calling front-end cannot project resolves to the explicit unavailability
// notice — never a silent no-op (spec wiki/port/slash-command-surface.md
// §4.4/§6).
package prompts

import (
	"sort"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/cli"
	"github.com/smallnest/pigo/internal/runtime"
)

// bareRegistry builds a registry with the full built-in catalog: the live
// commands plus the config-surface registrations each front-end wires (zero
// SurfaceDeps = the fail-closed faces). List() is exactly the declared catalog.
func bareRegistry(t *testing.T) *runtime.SlashRegistry {
	t.Helper()
	reg, err := BuildSlashRegistry(&cli.LiveConfig{Model: "test", ProviderName: "test"}, nil, nil, nil, PromptTemplateSources{})
	if err != nil {
		t.Fatalf("BuildSlashRegistry: %v", err)
	}
	RegisterSurfaceCommands(reg, &SurfaceDeps{})
	return reg
}

// TestBuiltinCatalogHasNoSilentStubs pins the closed built-in catalog: the
// name set is exhaustive (a re-introduced no-op stub shows up as a name or a
// missing face), and every built-in is either executable (Parse/Action/Run/
// Expand) or declares a Projection face — no registration may again exist
// whose only behavior is returning an empty status string. Aliases are
// declared identity (slice 4): /effect belongs to /think and /resume to
// /sessions as Aliases fields, so they appear in the Candidates rows instead
// of the canonical catalog.
func TestBuiltinCatalogHasNoSilentStubs(t *testing.T) {
	reg := bareRegistry(t)
	want := []string{
		"btw", "clone", "compact", "context", "copy", "dream", "dump",
		"exit", "export", "fork", "goal", "help", "import", "lsp", "mcp", "memory",
		"mode", "model", "models", "quit", "rebuild", "remote-control", "rename",
		"rewind", "session", "sessions", "shell", "skills", "status", "think",
		"tree",
	}
	var got []string
	for _, c := range reg.List() {
		got = append(got, c.Name)
		if c.Parse == nil && c.Action == nil && c.Run == nil && c.Expand == nil && c.Projection == runtime.ProjNone {
			t.Errorf("built-in /%s has neither an executable face nor a declared projection (silent stub?)", c.Name)
		}
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("builtin catalog = %v, want %v", got, want)
	}
	// Declared aliases resolve to their owning command (the human dispatch
	// path), stay out of the canonical catalog, and render as candidate rows
	// that say what they alias.
	for alias, owner := range map[string]string{"effect": "think", "resume": "sessions"} {
		cmd, ok := reg.Lookup(alias)
		if !ok || cmd.Name != owner {
			t.Errorf("Lookup(%s) = (%+v, %v), want the %s command", alias, cmd, ok, owner)
		}
	}
	rows := map[string]string{}
	for _, c := range reg.Candidates() {
		rows[c.Name] = c.Description
	}
	for alias, owner := range map[string]string{"effect": "think", "resume": "sessions"} {
		desc, ok := rows[alias]
		if !ok {
			t.Errorf("Candidates should carry the %s alias row", alias)
			continue
		}
		if !strings.HasPrefix(desc, "alias of /"+owner) {
			t.Errorf("%s row description = %q, want the alias-of-%s prefix", alias, desc, owner)
		}
	}
}

// TestDeclaredFacesResolveExplicitly pins §6: a declared command whose loop
// face the calling path cannot project resolves to the shared unavailability
// notice (Kind SlashAction, no prompt) — TUI faces say so, REPL faces point at
// --no-tui. /resume resolves through its declared alias and the notice keeps
// the invoked form.
func TestDeclaredFacesResolveExplicitly(t *testing.T) {
	reg := bareRegistry(t)
	for _, tc := range []struct{ line, want string }{
		{"/sessions", "(sessions unavailable: TUI-face command)"},
		{"/resume", "(resume unavailable: TUI-face command)"},
		{"/rename my title", "(rename unavailable: TUI-face command)"},
		{"/context", "(context unavailable: TUI-face command)"},
		{"/fork 2", "(fork unavailable: REPL-face command — run pigo --no-tui)"},
		{"/copy", "(copy unavailable: REPL-face command — run pigo --no-tui)"},
	} {
		out, err := reg.ResolveOutcome(tc.line)
		if err != nil {
			t.Errorf("%s: ResolveOutcome: %v", tc.line, err)
			continue
		}
		if out.Kind != runtime.SlashAction || out.Message != tc.want || out.Prompt != "" {
			t.Errorf("%s: outcome = %+v, want SlashAction with message %q and no prompt", tc.line, out, tc.want)
		}
	}
}

// TestParseMemoryRebuild pins the two slice-2 Parse faces: bare invocations
// parse to their intents; arguments are an explicit rejection (the former
// intercepts silently ignored /memory's arguments).
func TestParseMemoryRebuild(t *testing.T) {
	it, err := parseMemory("")
	if err != nil || it.Kind != runtime.IntentMemoryShow {
		t.Errorf("parseMemory(\"\") = %+v, %v; want IntentMemoryShow, nil", it, err)
	}
	if _, err := parseMemory(" focus"); err == nil || err.Error() != "memory: takes no arguments" {
		t.Errorf("parseMemory(\" focus\") error = %v, want \"memory: takes no arguments\"", err)
	}
	it, err = parseRebuild("")
	if err != nil || it.Kind != runtime.IntentRebuild {
		t.Errorf("parseRebuild(\"\") = %+v, %v; want IntentRebuild, nil", it, err)
	}
	if _, err := parseRebuild(" now"); err == nil || err.Error() != "rebuild: takes no arguments" {
		t.Errorf("parseRebuild(\" now\") error = %v, want \"rebuild: takes no arguments\"", err)
	}
}
