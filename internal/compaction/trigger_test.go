package compaction

// Tests for the T4.4 余项 formulas: the model-aware full-compaction trigger
// line (minimax A/B 线 + per-model override table) and the dynamic max_tokens
// output budget.

import "testing"

func TestCompactionTriggerLineBaseline(t *testing.T) {
	s := DefaultCompactionSettings
	// No per-turn output budget → exactly pi's baseline window − reserve.
	if got := CompactionTriggerLine(200_000, 0, s); got != 183_616 {
		t.Errorf("baseline line = %d, want 183616", got)
	}
	// Unknown window → 0 (never trigger).
	if got := CompactionTriggerLine(0, 32_768, s); got != 0 {
		t.Errorf("unknown window line = %d, want 0", got)
	}
}

func TestCompactionTriggerLinePerTurn(t *testing.T) {
	s := DefaultCompactionSettings // margin 2048
	// A small output budget (below the reserve) leaves the baseline line.
	if got := CompactionTriggerLine(200_000, 8_192, s); got != 183_616 {
		t.Errorf("small perTurn line = %d, want 183616", got)
	}
	// A large output budget sinks the line by perTurn + margin (A 线 binds:
	// w − max(reserve, perTurn+margin) = 200000 − 34816).
	if got := CompactionTriggerLine(200_000, 32_768, s); got != 165_184 {
		t.Errorf("large perTurn line = %d, want 165184", got)
	}
	// Very large window: the B-line 0.95w term becomes the pre-defense bound
	// (minimax triple-min shape) even with a modest output budget.
	if got := CompactionTriggerLine(512_000, 8_192, s); got != 486_400 {
		t.Errorf("huge window line = %d, want 486400 (0.95w)", got)
	}
}

func TestResolveTriggerRatio(t *testing.T) {
	cases := []struct {
		model string
		want  float64
	}{
		{"MiniMax-M3", 0.90},
		{"minimax/MiniMax-M2.7", 0.90},
		{"kimi-k2.6", 0.85},
		{"moonshotai/kimi-k3", 0.85},
		{"gpt-6-astra", 0},
		{"", 0},
	}
	for _, c := range cases {
		if got := ResolveTriggerRatio(c.model); got != c.want {
			t.Errorf("ResolveTriggerRatio(%q) = %v, want %v", c.model, got, c.want)
		}
	}
}

func TestCompactionLine(t *testing.T) {
	s := DefaultCompactionSettings
	// Override models: ratio × window, perTurn term ignored.
	if got := CompactionLine(512_000, 32_768, s, "MiniMax-M3"); got != 460_800 {
		t.Errorf("minimax line = %d, want 460800", got)
	}
	if got := CompactionLine(256_000, 0, s, "kimi-k3"); got != 217_600 {
		t.Errorf("kimi line = %d, want 217600", got)
	}
	// Unmatched model: generic A/B formula.
	if got := CompactionLine(200_000, 0, s, "gpt-x"); got != 183_616 {
		t.Errorf("generic line = %d, want 183616", got)
	}
	if got := CompactionLine(0, 0, s, "MiniMax-M3"); got != 0 {
		t.Errorf("unknown window line = %d, want 0", got)
	}
}

func TestResolveDynamicMaxTokens(t *testing.T) {
	// Configured cap below the window-derived bound → the cap wins.
	if got := ResolveDynamicMaxTokens(100_000, 20_000, 8_192, 0, 2_048); got != 8_192 {
		t.Errorf("configured wins = %d, want 8192", got)
	}
	// Window-derived bound below the cap → the bound wins.
	if got := ResolveDynamicMaxTokens(100_000, 50_000, 32_768, 16_384, 2_048); got != 31_568 {
		t.Errorf("derived wins = %d, want 31568", got)
	}
	// Nearly-full context floors at OutputFloorTokens.
	if got := ResolveDynamicMaxTokens(100_000, 98_000, 8_192, 0, 2_048); got != OutputFloorTokens {
		t.Errorf("floored = %d, want %d", got, OutputFloorTokens)
	}
	// Unknown configured cap → purely window-derived.
	if got := ResolveDynamicMaxTokens(100_000, 20_000, 0, 0, 2_048); got != 77_952 {
		t.Errorf("uncapped = %d, want 77952", got)
	}
	// Unknown window → 0 (no hint).
	if got := ResolveDynamicMaxTokens(0, 0, 8_192, 0, 2_048); got != 0 {
		t.Errorf("unknown window = %d, want 0", got)
	}
}

func TestMicrocompactPressureLineFor(t *testing.T) {
	// zcode derivation: min(0.9×autoLine, autoLine−2K).
	if got := MicrocompactPressureLineFor(183_616); got != 165_254 {
		t.Errorf("derived micro line = %d, want 165254", got)
	}
	// Degenerate line → 0.
	if got := MicrocompactPressureLineFor(2_000); got != 0 {
		t.Errorf("degenerate micro line = %d, want 0", got)
	}
	if got := MicrocompactPressureLineFor(0); got != 0 {
		t.Errorf("zero micro line = %d, want 0", got)
	}
}
