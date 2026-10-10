package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// maxBodyBytes bounds a quota response: the documented bodies are a few
	// KB, so a much larger one is a wrong endpoint (an HTML console page).
	maxBodyBytes = 1 << 20
	// fetchTimeout bounds one request; the caller adds its own overall bound.
	fetchTimeout = 8 * time.Second
	userAgent    = "pigo-usage"
)

// Request is one quota request: the endpoint, the API key presented as a
// Bearer token, and any headers a provider's gateway requires on top of the
// defaults. A provider that identifies clients by User-Agent sets it here.
type Request struct {
	Endpoint string
	Key      string
	Headers  map[string]string
}

// GetJSON performs a Request with no extra headers: an HTTPS GET carrying the
// API key as a Bearer token, returning the response body.
func GetJSON(ctx context.Context, endpoint, key string) ([]byte, error) {
	return Do(ctx, Request{Endpoint: endpoint, Key: key})
}

// Do performs a Request. It enforces the module's egress rules (normalize
// rule 6) — HTTPS only, no loopback/private/link-local host, no cross-host
// redirect, a bounded body, and a JSON (not HTML) response. The key is never
// included in an error.
func Do(ctx context.Context, r Request) ([]byte, error) {
	u, err := url.Parse(strings.TrimSpace(r.Endpoint))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("bad quota endpoint %q", r.Endpoint)
	}
	if err := endpointGuard(u); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+r.Key)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	for k, v := range r.Headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{
		Timeout: fetchTimeout,
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			if err := endpointGuard(r.URL); err != nil {
				return err
			}
			if r.URL.Host != via[0].URL.Host {
				return fmt.Errorf("redirect leaves %s", via[0].URL.Host)
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBodyBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", maxBodyBytes)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d%s", resp.StatusCode, statusHint(body))
	}
	if !looksLikeJSON(body) {
		return nil, errors.New("response is not JSON (an HTML page or a login redirect?)")
	}
	return body, nil
}

// endpointGuard is the egress check Do applies to every request and redirect
// hop. It stays a variable only so tests can point the transport at a loopback
// test server; production never reassigns it.
var endpointGuard = guardURL

// guardURL rejects an endpoint that must not be contacted: anything but HTTPS,
// and any host that names this machine, a private network, or an internal
// name. The surveyed implementations added this for their user-supplied
// "custom source" route; it costs nothing on the built-in constant endpoints.
func guardURL(u *url.URL) error {
	if !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("quota endpoint must use https, got %q", u.Scheme)
	}
	if u.User != nil {
		return errors.New("quota endpoint must not carry userinfo")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return errors.New("quota endpoint has no host")
	}
	for _, suffix := range []string{".localhost", ".local", ".internal"} {
		if strings.HasSuffix(host, suffix) {
			return fmt.Errorf("quota endpoint host %q is a local name", host)
		}
	}
	if host == "localhost" {
		return errors.New("quota endpoint host is localhost")
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("quota endpoint host %q is not routable", host)
		}
	}
	return nil
}

// looksLikeJSON reports whether a body starts like a JSON document, which is
// what distinguishes a quota API from the console page a wrong URL returns.
func looksLikeJSON(body []byte) bool {
	t := bytes.TrimSpace(body)
	return len(t) > 0 && (t[0] == '{' || t[0] == '[')
}

// statusHint extracts the provider's own error code and message from a
// failure body, so a rejected credential stays distinguishable from a missing
// one. The surveyed providers disagree on transport shape (a 401 envelope, or
// HTTP 200 wrapping a business code), so the candidates are read generically.
func statusHint(body []byte) string {
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return ""
	}
	var parts []string
	if errObj, ok := doc["error"].(map[string]any); ok {
		for _, k := range []string{"code", "type", "message"} {
			if s, ok := scalarString(errObj[k]); ok {
				parts = append(parts, s)
			}
		}
	} else if s, ok := scalarString(doc["error"]); ok {
		parts = append(parts, s)
	}
	for _, k := range []string{"code", "errorCode", "msg", "message"} {
		if s, ok := scalarString(doc[k]); ok {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	hint := strings.Join(parts, " ")
	if len(hint) > 120 {
		hint = hint[:120] + "…"
	}
	return " (" + hint + ")"
}

func scalarString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		if strings.TrimSpace(t) == "" {
			return "", false
		}
		return strings.TrimSpace(t), true
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), true
	default:
		return "", false
	}
}
