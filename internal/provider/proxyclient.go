// Proxy wiring (T8.1, spec wiki/port/provider-config.md §4.4): a config.toml
// [provider]/[models] `proxy` URL routes that connection's requests through
// the named egress proxy. An explicitly written URL that cannot be used is a
// hard error (fail-fast, spec §7 deviation) — silently going direct would
// defeat the "prefer the proxy" intent the URL expresses. An unset proxy
// keeps today's transport behavior (standard env-proxy semantics).
package provider

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// httpClientSetter is the injection seam the concrete drivers implement so
// ResolveProviderWithProxy can hand the proxy client to a constructed
// provider without widening every constructor.
type httpClientSetter interface {
	setHTTPClient(c *http.Client)
}

// ProxyClient builds an HTTP client whose transport routes every request
// through proxyURL (http/https/socks5). The default transport is cloned so
// its TLS/HTTP2 defaults carry over and only the proxy dialer changes.
func ProxyClient(proxyURL string) (*http.Client, error) {
	u, err := url.Parse(strings.TrimSpace(proxyURL))
	if err != nil {
		return nil, fmt.Errorf("parse proxy url %q: %w", proxyURL, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("proxy url %q needs a scheme and host (e.g. http://127.0.0.1:7890)", proxyURL)
	}
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("default transport is not *http.Transport")
	}
	t := transport.Clone()
	t.Proxy = http.ProxyURL(u)
	return &http.Client{Transport: t}, nil
}

// applyProxyClient injects a proxy client into p when proxy is set. A driver
// without the seam is left untouched (it rides the transport default — the
// injection is best-effort for exotic providers, and every mainstream driver
// implements the seam).
func applyProxyClient(p Provider, proxy string) error {
	proxy = strings.TrimSpace(proxy)
	if proxy == "" {
		return nil
	}
	client, err := ProxyClient(proxy)
	if err != nil {
		return err
	}
	if setter, ok := p.(httpClientSetter); ok {
		setter.setHTTPClient(client)
	}
	return nil
}

// ResolveProviderWithProxy is ResolveProvider with the T8.1 proxy tier: when
// proxy is a non-empty URL, the resolved provider's requests ride it. proxy
// comes from the config resolution (model-level > provider-level, spec §4.1);
// an empty value resolves exactly like ResolveProvider.
func ResolveProviderWithProxy(model, baseURL, protocol, providerName string, env func(string) string, proxy string) (Provider, string, error) {
	prov, name, err := ResolveProvider(model, baseURL, protocol, providerName, env)
	if err != nil {
		return nil, "", err
	}
	if err := applyProxyClient(prov, proxy); err != nil {
		return nil, "", err
	}
	return prov, name, nil
}
