package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/testenv"
)

func TestSetShellBackendRoundTrip(t *testing.T) {
	dir := testenv.Dir(t)
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[skills]\ndisabled = []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetShellBackend(path, "pwsh"); err != nil {
		t.Fatalf("SetShellBackend: %v", err)
	}
	cfg, err := LoadFileConfig(path)
	if err != nil {
		t.Fatalf("LoadFileConfig: %v", err)
	}
	if cfg.Shell.Backend != "pwsh" {
		t.Fatalf("backend = %q, want pwsh", cfg.Shell.Backend)
	}
	// A second write replaces the key in place.
	if err := SetShellBackend(path, "cmd"); err != nil {
		t.Fatalf("SetShellBackend(2): %v", err)
	}
	data, _ := os.ReadFile(path)
	if got := strings.Count(string(data), "backend = "); got != 1 {
		t.Fatalf("backend key count = %d, want 1 (replaced in place)\n%s", got, data)
	}
}

func TestSetShellBackendPreservesCommentsAndSections(t *testing.T) {
	dir := testenv.Dir(t)
	path := filepath.Join(dir, "config.toml")
	content := "# my shell prefs\n[shell]\n# keep me\nbackend = \"bash\"\n\n[skills]\ndisabled = []\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetShellBackend(path, "powershell"); err != nil {
		t.Fatalf("SetShellBackend: %v", err)
	}
	data, _ := os.ReadFile(path)
	text := string(data)
	for _, want := range []string{"# keep me", "[skills]", "backend = \"powershell\""} {
		if !strings.Contains(text, want) {
			t.Fatalf("config lost %q after the write:\n%s", want, text)
		}
	}
}

func TestSetShellBackendRefusals(t *testing.T) {
	dir := testenv.Dir(t)
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetShellBackend(path, ""); err == nil {
		t.Fatal("empty backend = nil error")
	}
	if err := SetShellBackend("", "bash"); err == nil {
		t.Fatal("empty path = nil error")
	}
	if err := SetShellBackend(filepath.Join(dir, "absent.toml"), "bash"); err == nil {
		t.Fatal("missing config = nil error (the config is read at startup; silently creating one would hide that)")
	}
}
