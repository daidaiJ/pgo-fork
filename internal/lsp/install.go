package lsp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// installTimeout bounds one `go install` of the server command: gopls is a
// compile-heavy module and a cold toolchain download can add minutes on top.
const installTimeout = 10 * time.Minute

// cacheBinDir is overridable in tests.
var cacheBinDir = defaultCacheBin

func defaultCacheBin() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "pigo", "bin"), nil
}

// autoInstall ensures the default server command exists, installing it into
// the pigo cache bin with `go install …@latest` when it does not (opencode's
// on-demand shape). Returns the absolute command path to launch. A custom
// [lsp.gopls] command is never installed — pigo cannot know its module path.
func autoInstall(command string) (string, error) {
	if command != DefaultCommand {
		return "", fmt.Errorf("lsp: %s not found (auto-install only covers %s)", command, DefaultCommand)
	}
	bin, err := cacheBinDir()
	if err != nil {
		return "", fmt.Errorf("resolve cache dir: %w", err)
	}
	target := filepath.Join(bin, command)
	if runtime.GOOS == "windows" {
		target += ".exe"
	}
	if fi, err := os.Stat(target); err == nil && !fi.IsDir() {
		return target, nil
	}
	goPath, err := exec.LookPath("go")
	if err != nil {
		return "", fmt.Errorf("the go tool is not on PATH — install Go, or gopls itself (go install golang.org/x/tools/gopls@latest)")
	}
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), installTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, goPath, "install", "golang.org/x/tools/gopls@latest")
	cmd.Env = append(os.Environ(), "GOBIN="+bin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("timed out after %s (output tail: %s)", installTimeout, tail(string(out), 400))
		}
		return "", fmt.Errorf("go install golang.org/x/tools/gopls@latest: %s", tail(string(out), 400))
	}
	if _, err := os.Stat(target); err != nil {
		return "", fmt.Errorf("go install reported success but %s is missing", target)
	}
	return target, nil
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}
