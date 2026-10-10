package run

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/cli/config"
	"github.com/smallnest/pigo/internal/runtime"
	"github.com/smallnest/pigo/internal/testenv"
	"github.com/smallnest/pigo/internal/trust"
)

// isolateTrust points PIGO_HOME at a fresh temp dir so Trusted(cwd) consults
// a private trust store, and returns a manager for seeding decisions.
func isolateTrust(t *testing.T) *trust.Manager {
	t.Helper()
	t.Chdir(testenv.Dir(t)) // project-layer reads/writes resolve against cwd
	home := testenv.Dir(t)
	t.Setenv("PIGO_HOME", home)
	t.Setenv("PIGO_LSP", "")
	m, err := trust.NewManager(filepath.Join(home, "trust.json"))
	if err != nil {
		t.Fatalf("trust manager: %v", err)
	}
	return m
}

func TestResolveLSPSettingsDefaultOff(t *testing.T) {
	isolateTrust(t)
	st, err := ResolveLSPSettings(config.LSPConfig{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if st.Enabled {
		t.Fatal("default must be off")
	}
	if st.Command != "" {
		t.Fatalf("command = %q (defaults applied by the manager, not the resolver)", st.Command)
	}
}

func TestResolveLSPSettingsGlobalToml(t *testing.T) {
	isolateTrust(t)
	st, err := ResolveLSPSettings(config.LSPConfig{
		Enabled:     true,
		IdleMinutes: 3,
		Gopls: config.LSPServerConfig{
			Command: "mygopls",
			Args:    []string{"serve"},
			Tools:   []string{"diagnostics"},
		},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !st.Enabled || st.Command != "mygopls" || len(st.Args) != 1 || st.Idle != 3*time.Minute {
		t.Fatalf("settings = %+v", st)
	}
	if len(st.ToolFilter) != 1 || st.ToolFilter[0] != "diagnostics" {
		t.Fatalf("tool filter = %v", st.ToolFilter)
	}
}

func TestResolveLSPSettingsProjectLayerTrustedGate(t *testing.T) {
	m := isolateTrust(t)
	cwd, _ := os.Getwd()

	// Project layer says on, global off, directory UNTRUSTED → stays off
	// (the security gate: an untrusted repo cannot spawn a local process).
	if err := SetProjectLSPEnabled(cwd, true); err == nil {
		t.Fatal("SetProjectLSPEnabled in an untrusted directory must refuse")
	}
	st, err := ResolveLSPSettings(config.LSPConfig{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if st.Enabled {
		t.Fatal("untrusted project switch must be ignored")
	}

	// Trust the directory: the write lands and the resolver picks it up.
	if err := m.SetDecision(cwd, trust.Trusted); err != nil {
		t.Fatalf("trust: %v", err)
	}
	if err := SetProjectLSPEnabled(cwd, true); err != nil {
		t.Fatalf("write project switch: %v", err)
	}
	st, err = ResolveLSPSettings(config.LSPConfig{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !st.Enabled {
		t.Fatal("trusted project switch must enable")
	}
	// The file carries the other-layer-preserving shape.
	data, err := os.ReadFile(filepath.Join(cwd, ".pigo", "config.json"))
	if err != nil {
		t.Fatalf("read project config: %v", err)
	}
	var layer map[string]json.RawMessage
	if err := json.Unmarshal(data, &layer); err != nil {
		t.Fatalf("parse project config: %v", err)
	}
	var lspSwitch runtime.LSPSettings
	if err := json.Unmarshal(layer["lsp"], &lspSwitch); err != nil {
		t.Fatalf("parse lsp section: %v", err)
	}
	if !lspSwitch.Enabled {
		t.Fatalf("lsp section = %s", layer["lsp"])
	}
}

func TestResolveLSPSettingsEnvOverridesProject(t *testing.T) {
	m := isolateTrust(t)
	cwd, _ := os.Getwd()
	if err := m.SetDecision(cwd, trust.Trusted); err != nil {
		t.Fatalf("trust: %v", err)
	}
	if err := SetProjectLSPEnabled(cwd, false); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Project says off; env forces on (env is the higher layer).
	t.Setenv("PIGO_LSP", "1")
	st, err := ResolveLSPSettings(config.LSPConfig{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !st.Enabled {
		t.Fatal("PIGO_LSP=1 must override the project off")
	}
	// And the reverse.
	t.Setenv("PIGO_LSP", "0")
	st, err = ResolveLSPSettings(config.LSPConfig{Enabled: true})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if st.Enabled {
		t.Fatal("PIGO_LSP=0 must override the global on")
	}
}

func TestResolveLSPSettingsMalformedProjectConfig(t *testing.T) {
	m := isolateTrust(t)
	cwd, _ := os.Getwd()
	if err := m.SetDecision(cwd, trust.Trusted); err != nil {
		t.Fatalf("trust: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cwd, ".pigo"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".pigo", "config.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := ResolveLSPSettings(config.LSPConfig{}); err == nil {
		t.Fatal("a malformed project config.json is a hard error")
	}
}

func TestLSPProjectConfigured(t *testing.T) {
	dir := testenv.Dir(t)
	if LSPProjectConfigured(dir) {
		t.Fatal("empty dir must report not-configured")
	}
	if err := os.MkdirAll(filepath.Join(dir, ".pigo"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	p := filepath.Join(dir, ".pigo", "config.json")
	if err := os.WriteFile(p, []byte(`{"lsp": {"enabled": true}}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !LSPProjectConfigured(dir) {
		t.Fatal("lsp section must be reported")
	}
	if err := os.WriteFile(p, []byte(`{"hooks": {}}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if LSPProjectConfigured(dir) {
		t.Fatal("a config without lsp must not be reported")
	}
}

// AutoInstall maps the nil-default-true config pointer: absent = install on
// demand, explicit false = fail fast on a missing command.
func TestResolveLSPSettingsAutoInstall(t *testing.T) {
	isolateTrust(t)
	st, err := ResolveLSPSettings(config.LSPConfig{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !st.AutoInstall {
		t.Fatal("absent auto_install must default to true")
	}
	falsy := false
	st, err = ResolveLSPSettings(config.LSPConfig{Gopls: config.LSPServerConfig{AutoInstall: &falsy}})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if st.AutoInstall {
		t.Fatal("auto_install = false must survive the resolve")
	}
	truly := true
	st, _ = ResolveLSPSettings(config.LSPConfig{Gopls: config.LSPServerConfig{AutoInstall: &truly}})
	if !st.AutoInstall {
		t.Fatal("auto_install = true must survive the resolve")
	}
}
