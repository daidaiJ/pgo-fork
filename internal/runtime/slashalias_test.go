// Alias resolution and the model-authored audience gate (T7.7 slice 4, spec
// wiki/port/slash-command-surface.md §4.3/§5.4): aliases are declared identity
// that resolves through the one registry lookup for typed invocations (grok's
// single key map) while the model-authored path stays fail-closed — exact
// canonical names only, human-only commands refused.
package runtime

import (
	"strings"
	"testing"
)

// aliasFixture builds a registry with one builtin declaring an alias and a
// Parse face (the /think shape), plus a plain builtin.
func aliasFixture() *SlashRegistry {
	r := NewSlashRegistry()
	r.AddBuiltin(SlashCommand{
		Name:    "think",
		Aliases: []string{"effect"},
		Parse: func(args string) (Intent, error) {
			return Intent{Kind: IntentThinkSet, Level: args}, nil
		},
	})
	r.AddBuiltin(SlashCommand{Name: "status", Expand: func(string) string { return "status" }})
	return r
}

// TestAliasResolvesThroughLookup pins the human path: the alias resolves to
// the owning command (canonical Name preserved), dispatch parses through the
// same command, the canonical catalog stays alias-free, and an unknown
// near-alias stays unknown.
func TestAliasResolvesThroughLookup(t *testing.T) {
	r := aliasFixture()
	cmd, ok := r.Lookup("effect")
	if !ok || cmd.Name != "think" {
		t.Fatalf("Lookup(effect) = (%+v, %v), want the think command", cmd, ok)
	}
	out, err := r.ResolveOutcome("/effect high")
	if err != nil || out.Kind != SlashIntent || out.Intent.Kind != IntentThinkSet || out.Intent.Level != "high" {
		t.Errorf("ResolveOutcome(/effect high) = %+v, %v; want the think intent", out, err)
	}
	for _, name := range r.List() {
		if name.Name == "effect" {
			t.Errorf("List must stay canonical; found alias row %q", name.Name)
		}
	}
	if _, ok := r.Lookup("efect"); ok {
		t.Error("Lookup(efect) resolved; a near-alias miss must stay unknown")
	}
	if _, err := r.ResolveOutcome("/efect high"); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("ResolveOutcome(/efect high) err = %v, want unknown command", err)
	}
}

// TestAliasCandidateRows pins the advertised surface: the alias appears as a
// row whose Name is the alias and whose description says what it aliases, in
// name order. The global builtinCommands map carries registrations from other
// tests in this package, so the pin is on the fixture's own rows.
func TestAliasCandidateRows(t *testing.T) {
	r := aliasFixture()
	var rows []string
	var effectRow SlashCommand
	for _, c := range r.Candidates() {
		switch c.Name {
		case "effect", "status", "think":
			rows = append(rows, c.Name)
		}
		if c.Name == "effect" {
			effectRow = c
		}
	}
	if want := "effect,status,think"; strings.Join(rows, ",") != want {
		t.Errorf("fixture candidate rows = %v, want %v (name order)", rows, want)
	}
	if effectRow.Parse == nil {
		t.Errorf("alias row should carry the owning command's faces, got %+v", effectRow)
	}
	if got := effectRow.Description; !strings.HasPrefix(got, "alias of /think") {
		t.Errorf("alias row description = %q, want the alias-of prefix", got)
	}
}

// TestAliasBuiltinKeyCollisionPanics pins the fail-closed registration rule:
// two built-ins claiming one key (alias vs canonical name, or alias vs alias)
// is a programming error, mirroring grok's rebuild_triggers panic.
func TestAliasBuiltinKeyCollisionPanics(t *testing.T) {
	r := NewSlashRegistry()
	r.AddBuiltin(SlashCommand{Name: "think"})
	defer func() {
		if recover() == nil {
			t.Error("builtin alias colliding with a builtin name must panic")
		}
	}()
	r.AddBuiltin(SlashCommand{Name: "other", Aliases: []string{"think"}})
}

// TestAliasShadowsLowerTierClaim pins the namespace-wide tier rule: a
// lower-tier command arriving after a built-in alias loses its canonical-name
// claim and is recorded as shadowed, exactly as against a built-in name.
func TestAliasShadowsLowerTierClaim(t *testing.T) {
	r := aliasFixture()
	r.AddUser(SlashCommand{Name: "effect", Expand: func(string) string { return "mine" }})
	if cmd, ok := r.Lookup("effect"); !ok || cmd.Name != "think" {
		t.Errorf("Lookup(effect) = (%+v, %v), want the builtin owner", cmd, ok)
	}
	sh := r.Shadowed()
	if len(sh) != 1 || sh[0].Name != "effect" || sh[0].Tier != TierGlobal {
		t.Errorf("Shadowed() = %v, want the user template's effect claim", sh)
	}
}

// TestRemoveDropsAliases pins that removing a command takes its aliases with
// it, so a re-registering surface controller cannot leave a dangling alias.
func TestRemoveDropsAliases(t *testing.T) {
	r := aliasFixture()
	if !r.Remove("think") {
		t.Fatal("Remove(think) = false, want true")
	}
	if _, ok := r.Lookup("effect"); ok {
		t.Error("Lookup(effect) resolved after the owning command was removed")
	}
	if _, ok := r.Lookup("think"); ok {
		t.Error("Lookup(think) resolved after removal")
	}
}

// modelOptedInBuiltin registers a builtin that admits model-authored input,
// with both an Expand face and an alias.
func modelOptedInBuiltin(r *SlashRegistry) {
	r.AddBuiltin(SlashCommand{
		Name:     "greet",
		Aliases:  []string{"hi"},
		Audience: AudienceHumanAndModel,
		Expand:   func(args string) string { return "hello " + args },
	})
}

// TestResolveModelAuthoredFailsClosed pins the audience gate matrix
// (T7.7 §6): a human-only built-in is refused on the model-authored path, its
// alias form and an unknown name come back as plain text, and only a command
// that opted in via AudienceHumanAndModel resolves — by exact canonical name,
// never through its alias.
func TestResolveModelAuthoredFailsClosed(t *testing.T) {
	r := aliasFixture()
	modelOptedInBuiltin(r)

	// Plain text and non-invocations pass through untouched.
	for _, in := range []string{"plain words", "/unknown cmd", "see /think docs"} {
		if out := r.ResolveModelAuthored(in); out.Handled || out.Prompt != in {
			t.Errorf("ResolveModelAuthored(%q) = %+v, want the verbatim text", in, out)
		}
	}
	// A human-only command is refused; so is its alias and its misspelling.
	for _, in := range []string{"/status", "/think high", "/effect high", "/thik high"} {
		if out := r.ResolveModelAuthored(in); out.Handled {
			t.Errorf("ResolveModelAuthored(%q) = %+v, want refusal (text)", in, out)
		}
	}
	// The opted-in command resolves by exact canonical name.
	out := r.ResolveModelAuthored("/greet world")
	if !out.Handled || out.Kind != SlashPrompt || out.Prompt != "hello world" {
		t.Errorf("ResolveModelAuthored(/greet world) = %+v, want the expanded prompt", out)
	}
	// Its alias is ignored (grok ExactCanonical).
	if out := r.ResolveModelAuthored("/hi there"); out.Handled {
		t.Errorf("ResolveModelAuthored(/hi there) = %+v, want text (aliases ignored)", out)
	}
	// A usage error in model-authored text degrades to text, not an error
	// path: the Parse here accepts anything, so pin the shape with an action
	// command instead — an opted-in action resolves as an action outcome.
	r.AddBuiltin(SlashCommand{
		Name:     "note",
		Audience: AudienceHumanAndModel,
		Action:   func(string) string { return "noted" },
	})
	if out := r.ResolveModelAuthored("/note"); !out.Handled || out.Kind != SlashAction || out.Message != "noted" {
		t.Errorf("ResolveModelAuthored(/note) = %+v, want the action outcome", out)
	}
}

// TestAudienceZeroValueFailsClosed pins the declaration default: a command
// that never declares an Audience stays human-only on the model path.
func TestAudienceZeroValueFailsClosed(t *testing.T) {
	if (SlashCommand{}.Audience) != AudienceHumanOnly {
		t.Fatal("zero Audience must be AudienceHumanOnly")
	}
	r := NewSlashRegistry()
	r.AddBuiltin(SlashCommand{Name: "quiet", Expand: func(string) string { return "x" }})
	if out := r.ResolveModelAuthored("/quiet"); out.Handled {
		t.Errorf("model-authored /quiet resolved: %+v, want refusal by default", out)
	}
	// The human path still works for the same command.
	out, err := r.ResolveOutcome("/quiet")
	if err != nil || !out.Handled || out.Prompt != "x" {
		t.Errorf("human /quiet = %+v, %v; want the expansion", out, err)
	}
}
