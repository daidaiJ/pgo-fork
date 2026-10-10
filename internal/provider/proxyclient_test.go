// Proxy wiring tests (T8.1, spec wiki/port/provider-config.md §4.4): a
// configured proxy URL routes the resolved provider's requests through the
// proxy (verified with an httptest server acting as the forward proxy), an
// unusable URL fails fast, and an unset proxy leaves the default transport.
package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// TestResolveProviderWithProxyRoutesThroughProxy pins the wire path: with a
// proxy configured, the stream's request arrives at the proxy as an
// absolute-form request (the proxy receives it even though the target host
// is unreachable). The proxy answers 500 so the stream fails after the wire
// assertion — the point is routing, not the response.
func TestResolveProviderWithProxyRoutesThroughProxy(t *testing.T) {
	var routed atomic.Bool
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routed.Store(true)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer proxy.Close()

	prov, name, err := ResolveProviderWithProxy("some-model", "http://target.example/v1", "openai", "", func(string) string { return "" }, proxy.URL)
	if err != nil {
		t.Fatalf("ResolveProviderWithProxy: %v", err)
	}
	if name != "openai" {
		t.Fatalf("provider name = %q, want openai (explicit protocol)", name)
	}
	stream, err := prov.StreamCompletion(context.Background(), CompletionRequest{
		Model:   "some-model",
		Context: LlmContext{Messages: agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hi")}}}},
		Config:  StreamConfig{APIKey: "sk-test"},
	})
	if err == nil {
		stream.Close()
	}
	if !routed.Load() {
		t.Error("the proxy never saw the request; the client is not riding the proxy")
	}
}

// TestResolveProviderWithProxyInvalidURLFails pins the fail-fast rule: an
// explicitly written proxy URL that cannot be used is an error, never a
// silent direct connection.
func TestResolveProviderWithProxyInvalidURLFails(t *testing.T) {
	for _, bad := range []string{"no-scheme-host:7890", "://", "http://"} {
		if _, _, err := ResolveProviderWithProxy("m", "https://x.example/v1", "openai", "", func(string) string { return "" }, bad); err == nil {
			t.Errorf("proxy %q resolved without error, want fail-fast", bad)
		}
	}
	// The no-proxy path is unaffected by the seam.
	if _, name, err := ResolveProviderWithProxy("openai/gpt-4o", "", "", "", func(string) string { return "" }, ""); err != nil || name != "openrouter" {
		t.Errorf("empty proxy resolved = (%q, %v), want the plain resolution", name, err)
	}
}

// TestProxyClientRejectsBareProxy pins the URL validation at the helper.
func TestProxyClientRejectsBareProxy(t *testing.T) {
	if _, err := ProxyClient("localhost:7890"); err == nil {
		t.Error("ProxyClient(localhost:7890) should fail (no scheme)")
	}
	if c, err := ProxyClient("socks5://127.0.0.1:1080"); err != nil || c == nil {
		t.Errorf("ProxyClient(socks5://127.0.0.1:1080) = %v, %v; want a client", c, err)
	}
}
