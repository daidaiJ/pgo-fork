package tui

import (
	"strings"
	"testing"
)

// TestGlamourStyleDropsATXPrefixes verifies the 2026-10-07 style patch (user
// report: "##" survived into the rendered output): the transcript's glamour
// renderer strips the stock ATX block prefixes on H2–H6 while keeping the
// shared heading base (color + bold), so headings render clean.
func TestGlamourStyleDropsATXPrefixes(t *testing.T) {
	r := rendererFor(80)
	if r == nil {
		t.Fatal("glamour renderer build failed")
	}
	out, err := r.Render("## Hello\n\nbody text")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if plain := stripANSI(out); strings.Contains(plain, "##") {
		t.Errorf("h2 ATX prefix leaked into the render: %q", plain)
	}
	if !strings.Contains(stripANSI(out), "Hello") {
		t.Errorf("heading text lost: %q", stripANSI(out))
	}
}
