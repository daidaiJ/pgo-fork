// This file implements the config.toml model profiles (T7.3 用户实测反馈：
// /model 切换列表的来源必须是配置里声明的模型档案，grok 的 [model."<id>"]
// 对齐——preset 目录/baseurl 在线目录只在未配置档案时兜底)。一个档案一条
// [models."<id>"] 子表；顶层 model = "<档案 id>" 让该档案成为启动模型，
// /model <id> 在会话内切换到它（按档案重建 provider、窗口、effort）。
package config

import (
	"sort"
	"strings"
)

// ModelProfile is one [models."<id>"] entry in config.toml. The table key is
// the profile id the /model switcher lists; Model is the wire model id sent
// to the endpoint and defaults to the key itself. Every field is optional:
// unset fields fall back to the session's current values (base_url/protocol/
// provider) or to the usual heuristic resolution (context window, effort).
type ModelProfile struct {
	// Model is the wire model id; empty means "same as the table key".
	Model string `toml:"model"`
	// Name is the switcher's display label; empty falls back to the id.
	Name string `toml:"name"`
	// Description is the switcher's one-line description (grok's row shape).
	Description string `toml:"description"`
	// BaseURL routes this profile to its own endpoint; empty keeps the
	// session's base_url.
	BaseURL string `toml:"base_url"`
	// Provider is the provider family hint (openai, anthropic, openrouter, …).
	// Empty lets the model-id heuristic pick.
	Provider string `toml:"provider"`
	// Protocol pins the wire protocol (openai, openai/resp_api, anthropic).
	Protocol string `toml:"protocol"`
	// APIKey authenticates this profile when its endpoint differs from the
	// session's; empty keeps the existing credential. Never logged.
	APIKey string `toml:"api_key"`
	// Credential is a named credential reference (issue #568) used when
	// APIKey is empty; the literal secret lives in .credentials.yaml.
	Credential string `toml:"credential"`
	// EnvKey names the environment variable holding the key (T8.1): the
	// profile tier of the built-in providers' spec env vars. Precedence
	// within the profile: APIKey > Credential > EnvKey.
	EnvKey string `toml:"env_key"`
	// Proxy is the egress proxy URL (T8.1) this profile's requests ride;
	// empty inherits the referenced [provider] section's proxy (a profile
	// that sets its own wins). Empty keeps the default transport.
	Proxy string `toml:"proxy"`
	// ContextWindow overrides the model's catalog window (0 = derive).
	ContextWindow int `toml:"context_window"`
	// MaxOutputTokens overrides the model's output cap (0 = derive).
	MaxOutputTokens int `toml:"max_output_tokens"`
	// ThinkingLevel is the reasoning-effort level applied when this profile
	// is selected without an explicit effort argument.
	ThinkingLevel string `toml:"thinking_level"`
}

// UnmarshalTOML decodes one profile table. A scalar value directly under
// [models] (a grok-style `default = "..."` line, which pigo does not use —
// the top-level `model` key picks the startup model) is tolerated as an empty
// profile rather than failing the whole config file.
func (p *ModelProfile) UnmarshalTOML(data any) error {
	table, ok := data.(map[string]any)
	if !ok {
		return nil
	}
	p.Model = tomlString(table, "model")
	p.Name = tomlString(table, "name")
	p.Description = tomlString(table, "description")
	p.BaseURL = tomlString(table, "base_url")
	p.Provider = tomlString(table, "provider")
	p.Protocol = tomlString(table, "protocol")
	p.APIKey = tomlString(table, "api_key")
	p.Credential = tomlString(table, "credential")
	p.EnvKey = tomlString(table, "env_key")
	p.Proxy = tomlString(table, "proxy")
	p.ContextWindow = tomlInt(table, "context_window")
	p.MaxOutputTokens = tomlInt(table, "max_output_tokens")
	p.ThinkingLevel = tomlString(table, "thinking_level")
	return nil
}

// Empty reports whether the profile declares nothing usable. Scalar keys
// under [models] decode to empty profiles (see UnmarshalTOML); they stay out
// of the switcher face and the lookup.
func (p ModelProfile) Empty() bool {
	return p.Model == "" && p.Name == "" && p.Description == "" &&
		p.BaseURL == "" && p.Provider == "" && p.Protocol == "" &&
		p.APIKey == "" && p.Credential == "" && p.EnvKey == "" &&
		p.Proxy == "" && p.ContextWindow == 0 &&
		p.MaxOutputTokens == 0 && p.ThinkingLevel == ""
}

// WireModel resolves the id a request carries: the explicit Model field, or
// the profile's table key.
func (p ModelProfile) WireModel(key string) string {
	if p.Model != "" {
		return p.Model
	}
	return key
}

// Label resolves the switcher display label: Name, else the id.
func (p ModelProfile) Label(key string) string {
	if p.Name != "" {
		return p.Name
	}
	return key
}

// ProfileFor looks up a model profile by id. Matching is exact first, then
// case-insensitive; a wire-model id also matches (so /model <wire-id> finds
// the profile that declares it). Empty profiles (scalar keys under [models])
// never match.
func (c FileConfig) ProfileFor(id string) (string, ModelProfile, bool) {
	if id == "" || len(c.Models) == 0 {
		return "", ModelProfile{}, false
	}
	if p, ok := c.Models[id]; ok && !p.Empty() {
		return id, p, true
	}
	for key, p := range c.Models {
		if p.Empty() {
			continue
		}
		if strings.EqualFold(key, id) || (p.Model != "" && strings.EqualFold(p.Model, id)) {
			return key, p, true
		}
	}
	return "", ModelProfile{}, false
}

// ProfileIDs lists the usable profile ids in config order sorted
// alphabetically, so the switcher face is stable across opens.
func (c FileConfig) ProfileIDs() []string {
	out := make([]string, 0, len(c.Models))
	for key, p := range c.Models {
		if p.Empty() {
			continue
		}
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// tomlString reads a string field out of a decoded profile table, ignoring
// absent keys and type mismatches (BurntSushi hands UnmarshalTOML the raw
// decoded values).
func tomlString(table map[string]any, key string) string {
	v, ok := table[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

// tomlInt reads an int field out of a decoded profile table (TOML integers
// decode to int64).
func tomlInt(table map[string]any, key string) int {
	v, ok := table[key]
	if !ok {
		return 0
	}
	n, ok := v.(int64)
	if !ok {
		return 0
	}
	if n < 0 {
		return 0
	}
	return int(n)
}
