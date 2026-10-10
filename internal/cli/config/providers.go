// providers.go implements the config.toml [provider."<id>"] connection
// sections (T8.1, spec wiki/port/provider-config.md §4.1): one table per
// named provider carrying the connection face — base_url, wire protocol,
// credentials (api_key / credential reference / env_key) and the egress
// proxy. [models."<id>"] profiles reference a provider by id and inherit its
// unset fields per-field (grok model_providers alignment): a model's explicit
// value wins, and declaring ANY credential field opts the model out of
// credential inheritance wholesale, so half a credential can never be
// assembled from two sources.
//
// The provider table key is the reference id (the literal [[provider.x]] shape
// the user asked for would be a TOML array-of-tables — one key repeated — so
// the single-table form is the deviation of record, spec §7). Malformed
// provider entries are skipped with a warning rather than failing the whole
// config file (grok's lenient parse); referenced-but-undefined providers
// fail closed at resolution time.
package config

import (
	"fmt"
	"sort"
	"strings"
)

// ProviderSpec is one [provider."<id>"] entry: the connection face models
// inherit from. Every field is optional; a section without base_url is
// unusable and is skipped with a warning at load (see normalizeProviders).
type ProviderSpec struct {
	// BaseURL is the endpoint root (e.g. "https://token.sensenova.cn/v1").
	BaseURL string `toml:"base_url"`
	// Protocol pins the wire protocol: "openai" (default), "openai/resp_api"
	// or "anthropic" — the same values --protocol accepts.
	Protocol string `toml:"protocol"`
	// APIKey is the literal key. Precedence within one face: APIKey >
	// Credential > EnvKey. Never logged.
	APIKey string `toml:"api_key"`
	// Credential is a named credential reference (issue #568): the literal
	// secret lives in $PIGO_HOME/.credentials.yaml under this name.
	Credential string `toml:"credential"`
	// EnvKey names the environment variable holding the key — the config-file
	// tier of the built-in providers' spec env vars.
	EnvKey string `toml:"env_key"`
	// Proxy is the egress proxy URL (e.g. "http://127.0.0.1:7890") this
	// provider's requests ride. Empty = the default transport (env-proxy
	// semantics unchanged); a model-level proxy overrides it.
	Proxy string `toml:"proxy"`
}

// UnmarshalTOML decodes one provider table. A scalar value directly under
// [provider] is tolerated as an empty spec rather than failing the config.
func (p *ProviderSpec) UnmarshalTOML(data any) error {
	table, ok := data.(map[string]any)
	if !ok {
		return nil
	}
	p.BaseURL = tomlString(table, "base_url")
	p.Protocol = tomlString(table, "protocol")
	p.APIKey = tomlString(table, "api_key")
	p.Credential = tomlString(table, "credential")
	p.EnvKey = tomlString(table, "env_key")
	p.Proxy = tomlString(table, "proxy")
	return nil
}

// Empty reports whether the spec declares nothing usable.
func (p ProviderSpec) Empty() bool {
	return p.BaseURL == "" && p.Protocol == "" && p.APIKey == "" &&
		p.Credential == "" && p.EnvKey == "" && p.Proxy == ""
}

// ProviderFor looks up a config-defined provider by reference id: exact
// first, then case-insensitive (profile `provider = "SenseNova"` matches
// [provider."sensenova"]). Usable = present with a base_url; an id that only
// names a skipped/unusable section resolves to false so callers fail closed.
func (c FileConfig) ProviderFor(id string) (ProviderSpec, bool) {
	if id == "" || len(c.Providers) == 0 {
		return ProviderSpec{}, false
	}
	if p, ok := c.Providers[id]; ok && p.BaseURL != "" {
		return p, true
	}
	for key, p := range c.Providers {
		if p.BaseURL == "" {
			continue
		}
		if strings.EqualFold(key, id) {
			return p, true
		}
	}
	return ProviderSpec{}, false
}

// ProviderIDs lists the usable provider ids sorted alphabetically, so listing
// faces are stable across opens.
func (c FileConfig) ProviderIDs() []string {
	ids := make([]string, 0, len(c.Providers))
	for key, p := range c.Providers {
		if p.BaseURL != "" {
			ids = append(ids, key)
		}
	}
	sort.Strings(ids)
	return ids
}

// ModelConnection is the resolved connection face of one model profile after
// provider inheritance: what a caller needs to build the wire driver and its
// client. Every field is optional except where noted; empty means "the
// caller's current value stays" (a mid-session switch keeps the session's
// endpoint when neither profile nor provider declares one).
type ModelConnection struct {
	// ProviderRef is the reference the connection came from: the profile's
	// provider field resolved to a config-defined provider id, a built-in
	// family name, or "" when the profile declares no provider at all.
	ProviderRef string
	// UsedConfigProvider reports whether ProviderRef named a usable
	// [provider] section (its fields actually fed the inheritance).
	UsedConfigProvider bool
	BaseURL            string
	Protocol           string
	APIKey             string
	Credential         string
	EnvKey             string
	Proxy              string
	// ThinkingLevel is the profile's default reasoning-effort level (never
	// inherited: a provider section declares no effort).
	ThinkingLevel   string
	ContextWindow   int
	MaxOutputTokens int
}

// ResolveModelConnection resolves one [models."<id>"] profile against the
// [provider] sections: the profile's explicit fields win, unset fields
// inherit the referenced provider's, and declaring any credential field opts
// the profile out of credential inheritance wholesale. ok is false when key
// names no usable profile. The resolution is pure — no env, no registry — so
// both the startup path and the mid-session /model switch share one answer.
func (c FileConfig) ResolveModelConnection(key string) (ModelConnection, bool) {
	_, profile, ok := c.ProfileFor(key)
	if !ok {
		return ModelConnection{}, false
	}
	conn := ModelConnection{
		ProviderRef:     profile.Provider,
		BaseURL:         profile.BaseURL,
		Protocol:        profile.Protocol,
		APIKey:          profile.APIKey,
		Credential:      profile.Credential,
		EnvKey:          profile.EnvKey,
		Proxy:           profile.Proxy,
		ThinkingLevel:   profile.ThinkingLevel,
		ContextWindow:   profile.ContextWindow,
		MaxOutputTokens: profile.MaxOutputTokens,
	}
	provider, used := c.ProviderFor(profile.Provider)
	if !used {
		return conn, true
	}
	conn.UsedConfigProvider = true
	if conn.BaseURL == "" {
		conn.BaseURL = provider.BaseURL
	}
	if conn.Protocol == "" {
		conn.Protocol = provider.Protocol
	}
	if conn.Proxy == "" {
		conn.Proxy = provider.Proxy
	}
	// Credentials inherit only when the profile declares none of its own
	// (grok: a model with any auth field of its own never assembles the rest
	// from the provider).
	if conn.APIKey == "" && conn.Credential == "" && conn.EnvKey == "" {
		conn.APIKey = provider.APIKey
		conn.Credential = provider.Credential
		conn.EnvKey = provider.EnvKey
	}
	return conn, true
}

// ValidateProviders reports the load-time warnings for the [provider]
// sections: unusable (base_url-less) entries are skipped by every lookup, so
// each one warns once by id, mirroring grok's per-provider parse warnings.
func (c FileConfig) ValidateProviders() []string {
	ids := make([]string, 0, len(c.Providers))
	for id := range c.Providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var warnings []string
	for _, id := range ids {
		p := c.Providers[id]
		switch {
		case p.Empty():
			warnings = append(warnings, fmt.Sprintf("provider %q declares nothing; ignored", id))
		case p.BaseURL == "":
			warnings = append(warnings, fmt.Sprintf("provider %q has no base_url; ignored (models referencing it fail closed)", id))
		}
	}
	return warnings
}
