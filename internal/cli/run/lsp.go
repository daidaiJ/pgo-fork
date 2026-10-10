// LSP resolution (T8.2): the layered merge for the LSP switch —
//
//	default off < global config.toml [lsp] < project ./.pigo/config.json (trusted only) < PIGO_LSP env
//
// and the trust-gated project switch writer the /lsp surface uses. The
// command/args come from the global layer only: they name a local binary, a
// per-directory choice would let an untrusted repo redirect it.
package run

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/smallnest/pigo/internal/cli/config"
	"github.com/smallnest/pigo/internal/lsp"
	"github.com/smallnest/pigo/internal/runtime"
)

// ResolveLSPSettings merges the LSP layers into the manager settings. The
// global [lsp] table provides enabled/command/args/idle/tools; the project
// layer (.pigo/config.json, merged only when the directory is trusted — the
// same FR-14 gate the hooks use) overrides enabled; PIGO_LSP overrides last.
// tomlLSP is the already-loaded config.toml [lsp] table (zero value = absent).
func ResolveLSPSettings(tomlLSP config.LSPConfig) (lsp.Settings, error) {
	cwd, _ := os.Getwd()
	st := lsp.Settings{
		Enabled:     tomlLSP.Enabled,
		Command:     tomlLSP.Gopls.Command,
		Args:        tomlLSP.Gopls.Args,
		Idle:        time.Duration(tomlLSP.IdleMinutes) * time.Minute,
		ToolFilter:  tomlLSP.Gopls.Tools,
		Prewarm:     true,
		IdleReclaim: true,
		AutoInstall: tomlLSP.Gopls.AutoInstall == nil || *tomlLSP.Gopls.AutoInstall,
	}
	// Project layer (trusted only): {"lsp": {"enabled": bool}}. An untrusted
	// directory contributes nothing — a checked-out repo must not be able to
	// spawn a local process just by carrying the file.
	if Trusted(cwd) {
		layer, err := runtime.LoadConfigLayer(filepath.Join(cwd, ".pigo", "config.json"))
		if err != nil {
			return st, err
		}
		if layer != nil && layer.LSP != nil {
			st.Enabled = layer.LSP.Enabled
		}
	}
	// Env layer, last before a (future) CLI flag.
	env := runtime.EnvConfigLayer(os.Getenv)
	if env.LSP != nil {
		st.Enabled = env.LSP.Enabled
	}
	return st, nil
}

// SetProjectLSPEnabled writes the project layer's LSP switch to
// <cwd>/.pigo/config.json, preserving every other key. The write is
// trust-gated (fail closed): the same gate the resolver honors on read, so
// the switch cannot be turned on for a directory the user has not trusted.
func SetProjectLSPEnabled(cwd string, enabled bool) error {
	if !Trusted(cwd) {
		return fmt.Errorf("directory is not trusted — accept the first-launch trust prompt (or pigo --approve) before /lsp can write the project switch")
	}
	path := filepath.Join(cwd, ".pigo", "config.json")
	var layer runtime.ConfigLayer
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &layer); err != nil {
			return fmt.Errorf("parse %s: %w (fix or remove it before /lsp can write)", path, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	layer.LSP = &runtime.LSPSettings{Enabled: enabled}
	data, err := json.MarshalIndent(layer, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// LSPProjectConfigured reports whether the project layer file carries an
// lsp section — informational for the /lsp listing (it can explain why a
// present-but-untrusted project switch does nothing).
func LSPProjectConfigured(cwd string) bool {
	data, err := os.ReadFile(filepath.Join(cwd, ".pigo", "config.json"))
	if err != nil {
		return false
	}
	var layer struct {
		LSP *json.RawMessage `json:"lsp"`
	}
	if err := json.Unmarshal(data, &layer); err != nil {
		return false
	}
	return layer.LSP != nil && strings.TrimSpace(string(*layer.LSP)) != "null"
}
