package lsp

import (
	"net/url"
	"path/filepath"
	"strings"
)

// PathToURI renders an absolute filesystem path as an LSP file URI. Windows
// drive paths become file:///D:/CODE/... (the form gopls emits and accepts);
// the drive colon stays unescaped, which both VS Code and gopls read fine.
// The path must be absolute; a relative path is resolved against the process
// working directory first (mirroring os/exec behavior).
func PathToURI(path string) string {
	if !filepath.IsAbs(path) {
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
	}
	u := url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(path)}
	return u.String()
}

// URIToPath converts an LSP file URI back to a filesystem path. It handles
// the forms servers emit across platforms: file:///D:/x (Windows, colon
// plain or %3A-escaped), file:///home/x (POSIX), and the rare one-slash
// file:/D:/x form. Non-file schemes return the input unchanged.
func URIToPath(uri string) string {
	if !strings.HasPrefix(uri, "file:") {
		return uri
	}
	u, err := url.Parse(uri)
	if err != nil {
		return uri
	}
	p := u.Path
	if u.Host != "" {
		p = "//" + u.Host + p // UNC hosts: file://server/share → //server/share
	}
	p = strings.TrimPrefix(p, "/")
	if len(p) >= 2 && p[1] == ':' && (len(p) == 2 || p[2] == '/') { // Windows drive: D:/x → D:\x (a POSIX /a:b/c stays)
		return filepath.FromSlash(p)
	}
	// POSIX form: the leading slash was stripped alongside the Windows one;
	// restore it. On Windows this stays a slash path (a POSIX URI has no
	// meaningful local mapping there) — the caller compares against paths it
	// got from the same server, so the form stays self-consistent.
	return "/" + strings.ReplaceAll(p, "\\", "/")
}
