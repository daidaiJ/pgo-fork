// This file implements the zero-input credential route the community quota
// tools use (grok-build-proxy docs-local/usage/quota-endpoints-and-credentials.md
// §2 "复用 agent CLI 已落盘的凭据"): rather than asking the user to paste a key
// pigo can already find, read the one the OpenCode CLI holds for itself.
//
// Scope is deliberately narrow. The surveyed matrix covers Claude Code's
// keychain, Codex's auth.json, Antigravity accounts and xAI OAuth; the spec
// (wiki/port/provider-usage.md §2.4) defers that matrix to a later phase. This
// file covers the OpenCode store only, because it is the one the community
// route for OpenCode Go and Command Code names, and only for the provider ids
// listed in opencodeAuthEntryIDs.
package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// opencodeAuthEntryIDs maps a pigo provider name to the ids the OpenCode CLI
// files its credential under. The surveyed implementations read the OpenCode
// store for exactly this trio (opencode-go / command-code / xai); the xAI entry
// remains unmapped until pigo has a provider that speaks it.
var opencodeAuthEntryIDs = map[string][]string{
	"opencode-go":  {"opencode-go"},
	"opencode":     {"opencode"},
	"commandcode":  {"command-code", "commandcode"},
	"command-code": {"command-code", "commandcode"},
}

// DiscoverFromOpenCode returns the API key the OpenCode CLI holds for a pigo
// provider name, or "" when there is none — an unknown provider, an absent
// store, an unreadable file, or no entry for it. It never returns an error:
// this is a convenience layer over someone else's file, and a pigo that cannot
// read it is exactly as capable as one that never looked.
func DiscoverFromOpenCode(providerName string) string {
	return discoverOpenCodeAuth(providerName, opencodeAuthPaths())
}

// discoverOpenCodeAuth is DiscoverFromOpenCode with the candidate paths
// injected, so tests read a fixture store instead of the real one.
func discoverOpenCodeAuth(providerName string, paths []string) string {
	ids := opencodeAuthEntryIDs[providerName]
	if len(ids) == 0 {
		return ""
	}
	for _, path := range paths {
		if key := readOpenCodeAuthKey(path, ids); key != "" {
			return key
		}
	}
	return ""
}

// opencodeAuthKeyFields are the entry fields a key may live in, in precedence
// order: an API key first, then the OAuth-ish names a store might use instead.
var opencodeAuthKeyFields = []string{"key", "token", "apiKey", "access"}

// readOpenCodeAuthKey reads one auth.json and returns the first non-empty key
// among the given entry ids. The file is a flat map of provider id to an entry
// object; a store that files a bare string is read too, since the format is
// another project's to change.
func readOpenCodeAuthKey(path string, ids []string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return ""
	}
	for _, id := range ids {
		entry, ok := entries[id]
		if !ok {
			continue
		}
		var fields map[string]any
		if err := json.Unmarshal(entry, &fields); err == nil {
			for _, field := range opencodeAuthKeyFields {
				if v, ok := fields[field].(string); ok && v != "" {
					return v
				}
			}
			continue
		}
		var bare string
		if err := json.Unmarshal(entry, &bare); err == nil && bare != "" {
			return bare
		}
	}
	return ""
}

// opencodeAuthPaths lists the OpenCode credential-store locations, most
// specific first: the XDG data home when one is set, the documented
// ~/.local/share/opencode/auth.json, and the Windows per-user data directory
// a Windows build of that CLI would use.
func opencodeAuthPaths() []string {
	var paths []string
	add := func(dir string) {
		if dir == "" {
			return
		}
		paths = append(paths, filepath.Join(dir, "opencode", "auth.json"))
	}
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		add(xdg)
	}
	if home, err := os.UserHomeDir(); err == nil {
		add(filepath.Join(home, ".local", "share"))
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			add(local)
		} else {
			add(filepath.Join(home, "AppData", "Local"))
		}
	}
	return paths
}
