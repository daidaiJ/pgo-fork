// Package usage implements the provider subscription-quota face behind the
// /usage command's plan section (T6.3; spec wiki/port/provider-usage.md).
//
// It is a leaf package — the standard library only — so every provider is one
// file under sources/ plus its recorded fixtures, and the core types and
// normalization rules below never change when one is added (spec §2.2). The
// split the spec demands is enforced by the Source interface: Fetch owns the
// transport (and, for a provider whose quota picture spans several endpoints,
// the request sequence), Parse is a pure function of the response body and is
// table-tested against fixtures with no network.
//
// Normalization hard rules (spec §2.3), all of them from the surveyed
// providers' recorded failure modes: unknown is never zero (rule 1), the
// response's percent semantics are declared by the source and inverted here
// (rule 2), a reset time outside its own window is dropped rather than
// guessed (rule 3), windows are ordered by length with unknown lengths last
// (rule 4), independent lanes such as MCP tool quota stay separate (rule 5),
// egress is HTTPS-only and cannot reach loopback/private hosts (rule 6, in
// fetch.go), and a scheduled cancellation rides Snapshot.Note (rule 7).
package usage

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// WindowKind names a quota window. The vocabulary is closed and shared by
// every source, so the renderer never needs provider knowledge.
type WindowKind string

const (
	WindowFiveHour WindowKind = "five-hour"
	WindowWeekly   WindowKind = "weekly"
	WindowMonthly  WindowKind = "monthly"
	WindowBilling  WindowKind = "billing"
	WindowMCP      WindowKind = "mcp"
	WindowOther    WindowKind = "other"
)

// Window is one normalized quota window. Percent is always the USED share
// (rule 2), clamped to [0,100]; the pointers keep "unknown" distinct from
// zero, so an unreadable window renders as "--%" rather than "0%" (rule 1).
type Window struct {
	Kind    WindowKind
	Percent *float64
	// Used and Total carry the provider's own units when it reports the
	// window as a pair rather than a percentage (a dollar cap, a request
	// count). Either may be nil.
	Used  *float64
	Total *float64
	// Remaining is an absolute leftover with no usable total (a credit
	// balance); Unit names its currency/unit ("USD" or empty).
	Remaining *float64
	Unit      string
	ResetsAt  *time.Time
	// Note carries a provider qualifier worth showing next to the window
	// ("blocked", "status limited").
	Note string
}

// Title is the window's display name derived from its kind.
func (w Window) Title() string {
	switch w.Kind {
	case WindowFiveHour:
		return "5-hour limit"
	case WindowWeekly:
		return "Weekly limit"
	case WindowMonthly:
		return "Monthly limit"
	case WindowBilling:
		return "Monthly credits"
	case WindowMCP:
		return "MCP tools"
	default:
		return "Usage"
	}
}

// Snapshot is one provider's normalized quota picture.
type Snapshot struct {
	// Source is the registered source's name ("opencode-go"), stamped by
	// Query rather than by Parse.
	Source string
	Plan   string
	// Windows are ordered shortest-first by Query (rule 4).
	Windows []Window
	// Warnings record what normalization had to drop or could not read; they
	// are advisory, never a reason to hide a readable window.
	Warnings []string
	// Note carries subscription-level state (rule 7): a scheduled
	// cancellation, an end date.
	Note      string
	FetchedAt time.Time
}

// Credential is the resolved connection face a source fetches with: the
// provider identity (for source lookup), its effective base URL, and the API
// key. Sources never log or return the key.
type Credential struct {
	Provider string
	BaseURL  string
	Key      string
}

// Source is one provider's quota face.
type Source interface {
	Name() string
	// MatchProvider reports whether the source serves a provider by name
	// (case-insensitive): the built-in registry identity ("opencode-go").
	MatchProvider(provider string) bool
	// MatchBase reports whether the source serves a base URL's host and path.
	// It is what identifies a provider configured through a
	// [provider."<id>"] section: T8.1 resolves such a connection to the
	// generic driver named "custom", so the name carries no provider identity
	// and only the endpoint does.
	MatchBase(baseURL string) bool
	// Fetch performs the transport step and returns the response body Parse
	// consumes. For a provider whose quota spans endpoints it issues each
	// request and returns them bundled.
	Fetch(ctx context.Context, cred Credential) ([]byte, error)
	// Parse normalizes a response body. It must be pure: no network, no
	// clock-dependent branching beyond the reset check, and no mutation of
	// package state, so fixtures drive it directly.
	Parse(body []byte) (*Snapshot, error)
}

var sources []Source

// Register adds a source to the lookup table. It is called from the source
// file's init(), so adding a provider never edits this file.
func Register(s Source) { sources = append(sources, s) }

// ErrUnsupported reports that no source serves the provider. Callers render
// nothing at all for it: a provider whose plan cannot be queried is not an
// error the user needs to see.
var ErrUnsupported = errors.New("provider has no quota source")

// ErrNoCredential reports that the provider has a quota source but the lookup
// had no API key to present.
var ErrNoCredential = errors.New("no API key configured for the provider")

// Lookup finds the source serving a provider identity: a name match wins over
// a base-URL match, and registration order breaks ties within each pass. It
// reports false when no source claims the provider.
func Lookup(provider, baseURL string) (Source, bool) {
	provider = strings.TrimSpace(provider)
	if provider != "" {
		for _, s := range sources {
			if s.MatchProvider(provider) {
				return s, true
			}
		}
	}
	if strings.TrimSpace(baseURL) != "" {
		for _, s := range sources {
			if s.MatchBase(baseURL) {
				return s, true
			}
		}
	}
	return nil, false
}

// Query resolves a provider's quota snapshot: source lookup, credential
// check, fetch, parse. The returned error wraps ErrUnsupported or
// ErrNoCredential for the two cases the caller renders differently, and wraps
// the transport/parse failure otherwise. The key is never echoed in an error.
func Query(ctx context.Context, cred Credential) (*Snapshot, error) {
	src, ok := Lookup(cred.Provider, cred.BaseURL)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, describeCredential(cred))
	}
	if strings.TrimSpace(cred.Key) == "" {
		return nil, fmt.Errorf("%w: %s", ErrNoCredential, describeCredential(cred))
	}
	body, err := src.Fetch(ctx, cred)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", src.Name(), err)
	}
	snap, err := src.Parse(body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", src.Name(), err)
	}
	if snap == nil {
		return nil, fmt.Errorf("%s: empty quota response", src.Name())
	}
	snap.Source = src.Name()
	snap.FetchedAt = time.Now()
	SortWindows(snap.Windows)
	return snap, nil
}

// describeCredential names the provider for an error message: the provider
// name when it carries identity, else the base URL's host.
func describeCredential(c Credential) string {
	if p := strings.TrimSpace(c.Provider); p != "" && !strings.EqualFold(p, "custom") {
		return p
	}
	if h := Host(c.BaseURL); h != "" {
		return h
	}
	return "unknown provider"
}

// Host returns the lowercased host of a base URL, or "" when it cannot be
// parsed. It is the host half of the base-URL matching rule.
func Host(rawURL string) string {
	host, _ := SplitBase(rawURL)
	return host
}

// SplitBase splits a base URL into its lowercased host and its path (trailing
// slashes preserved as written). It is deliberately forgiving: a malformed or
// relative value yields ("", "") rather than an error, because matching a
// provider never needs to fail loudly.
func SplitBase(rawURL string) (host, path string) {
	raw := strings.TrimSpace(rawURL)
	if raw == "" {
		return "", ""
	}
	// A scheme-less value ("api.commandcode.ai/v1") is common in configs;
	// cut the authority off the first slash so it still matches by host.
	rest := raw
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	path = ""
	if i := strings.Index(rest, "/"); i >= 0 {
		rest, path = rest[:i], rest[i:]
	}
	if i := strings.Index(rest, "@"); i >= 0 {
		rest = rest[i+1:]
	}
	if i := strings.Index(rest, ":"); i >= 0 {
		rest = rest[:i]
	}
	return strings.ToLower(rest), path
}

// SortWindows orders windows shortest-first (rule 4) so the tightest window
// reads first and the longest is the secondary one; an unknown length sorts
// last. The independent tool lane (MCP) sorts after every model window
// (rule 5) — it is never merged into a model window's identity.
func SortWindows(ws []Window) {
	sort.SliceStable(ws, func(i, j int) bool {
		rank := func(w Window) int {
			if w.Kind == WindowMCP {
				return 2
			}
			if MaxDuration(w.Kind) == 0 {
				return 1
			}
			return 0
		}
		ri, rj := rank(ws[i]), rank(ws[j])
		if ri != rj {
			return ri < rj
		}
		return MaxDuration(ws[i].Kind) < MaxDuration(ws[j].Kind)
	})
}

// MaxDuration is the nominal length of a window kind (0 = unknown), used for
// ordering and for the reset-legitimacy check.
func MaxDuration(k WindowKind) time.Duration {
	switch k {
	case WindowFiveHour:
		return 5 * time.Hour
	case WindowWeekly:
		return 7 * 24 * time.Hour
	case WindowMonthly, WindowBilling:
		return 31 * 24 * time.Hour
	default:
		return 0
	}
}
