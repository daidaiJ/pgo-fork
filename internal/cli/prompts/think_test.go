package prompts

import (
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/cli"
	"github.com/smallnest/pigo/internal/runtime"
)

// TestThinkCommandSwitchesLevel verifies /think mutates the live thinking level
// so the next turn picks it up, and that a bare /think reports the current level.
func TestThinkCommandSwitchesLevel(t *testing.T) {
	live := &cli.LiveConfig{Model: "test", ProviderName: "test", ThinkingLevel: agentcore.ThinkingMedium}
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, nil)
	exec := &Executor{Live: live}

	out := resolveExec(t, reg, exec, "/think high")
	if live.ThinkingLevel != agentcore.ThinkingHigh {
		t.Errorf("ThinkingLevel = %q, want high", live.ThinkingLevel)
	}
	if !strings.Contains(out.Message, "high") {
		t.Errorf("message = %q, want it to mention high", out.Message)
	}

	// Bare /think reports the current level without changing it.
	out = resolveExec(t, reg, exec, "/think")
	if live.ThinkingLevel != agentcore.ThinkingHigh {
		t.Errorf("bare /think mutated level to %q", live.ThinkingLevel)
	}
	if !strings.Contains(out.Message, "high") {
		t.Errorf("bare /think message = %q, want current level high", out.Message)
	}
}

// TestThinkCommandRejectsInvalid verifies an unknown level is rejected and the
// live level is left unchanged.
func TestThinkCommandRejectsInvalid(t *testing.T) {
	live := &cli.LiveConfig{Model: "test", ProviderName: "test", ThinkingLevel: agentcore.ThinkingLow}
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, nil)

	// T7.7: Parse rejects the unknown level purely — nothing mutates.
	_, err := reg.ResolveOutcome("/think bogus")
	if err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Errorf("ResolveOutcome /think bogus: err = %v, want an invalid-level refusal", err)
	}
	if live.ThinkingLevel != agentcore.ThinkingLow {
		t.Errorf("invalid level changed ThinkingLevel to %q", live.ThinkingLevel)
	}
}

// TestEffectAliasesThink verifies /effect behaves identically to /think: it
// switches the live thinking level and a bare /effect reports the current level.
func TestEffectAliasesThink(t *testing.T) {
	live := &cli.LiveConfig{Model: "test", ProviderName: "test", ThinkingLevel: agentcore.ThinkingMedium}
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, nil)
	exec := &Executor{Live: live}

	out := resolveExec(t, reg, exec, "/effect high")
	if live.ThinkingLevel != agentcore.ThinkingHigh {
		t.Errorf("ThinkingLevel = %q, want high", live.ThinkingLevel)
	}
	if !strings.Contains(out.Message, "high") {
		t.Errorf("message = %q, want it to mention high", out.Message)
	}

	out = resolveExec(t, reg, exec, "/effect")
	if !strings.Contains(out.Message, "high") {
		t.Errorf("bare /effect message = %q, want current level high", out.Message)
	}
}
