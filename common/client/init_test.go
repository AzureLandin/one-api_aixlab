package client

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// A channel without a proxy must get exactly the shared client, so unconfigured
// channels keep the pre-existing behaviour bit for bit.
func TestRelayHTTPClientWithoutProxyReturnsSharedClient(t *testing.T) {
	original := HTTPClient
	HTTPClient = &http.Client{}
	defer func() { HTTPClient = original }()

	for _, proxyURL := range []string{"", "   "} {
		if got := RelayHTTPClient(proxyURL); got != HTTPClient {
			t.Fatalf("blank proxy %q must yield the shared client", proxyURL)
		}
	}
}

// A channel proxy must produce a distinct client that is reused across calls,
// so we do not build a transport per request.
func TestRelayHTTPClientWithProxyIsCached(t *testing.T) {
	first := RelayHTTPClient("http://127.0.0.1:18080")
	second := RelayHTTPClient("http://127.0.0.1:18080")
	if first == nil {
		t.Fatal("proxy client must not be nil")
	}
	if first != second {
		t.Fatal("the same proxy URL must reuse one cached client")
	}
	if first == HTTPClient {
		t.Fatal("a proxied channel must not share the default client")
	}
}

// A malformed proxy URL must never take the channel offline.
func TestRelayHTTPClientInvalidProxyFallsBack(t *testing.T) {
	original := HTTPClient
	HTTPClient = &http.Client{}
	defer func() { HTTPClient = original }()

	for _, bad := range []string{"not a url", "ftp://127.0.0.1:21", "://missing-scheme"} {
		if got := RelayHTTPClient(bad); got != HTTPClient {
			t.Fatalf("invalid proxy %q must fall back to the shared client", bad)
		}
	}
}

func TestBuildProxyClientSchemes(t *testing.T) {
	cases := map[string]bool{
		"http://127.0.0.1:7890":             true,
		"https://proxy.example.com:8443":    true,
		"socks5://127.0.0.1:1080":           true,
		"socks5h://127.0.0.1:1080":          true,
		"socks5://user:pass@127.0.0.1:1080": true,
		"ftp://127.0.0.1:21":                false,
		"http://":                           false,
		"":                                  false,
	}
	for proxyURL, wantOK := range cases {
		got := BuildProxyClient(proxyURL)
		if wantOK && got == nil {
			t.Fatalf("proxy %q should build a client", proxyURL)
		}
		if !wantOK && got != nil {
			t.Fatalf("proxy %q should be rejected", proxyURL)
		}
	}
}

// End-to-end: requests for a channel with a proxy must actually traverse that
// proxy, and requests without one must not.
func TestRelayHTTPClientRoutesThroughProxy(t *testing.T) {
	var proxiedHits int32

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "origin-ok")
	}))
	defer origin.Close()

	// A forward proxy receives the absolute-form request URI.
	forwardProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&proxiedHits, 1)
		if !strings.HasPrefix(r.RequestURI, "http://") {
			t.Errorf("proxy should receive absolute-form URI, got %q", r.RequestURI)
		}
		_, _ = io.WriteString(w, "proxy-ok")
	}))
	defer forwardProxy.Close()

	proxied := RelayHTTPClient(forwardProxy.URL)
	resp, err := proxied.Get(origin.URL)
	if err != nil {
		t.Fatalf("proxied request failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "proxy-ok" {
		t.Fatalf("request should have gone through the proxy, got %q", body)
	}
	if atomic.LoadInt32(&proxiedHits) != 1 {
		t.Fatalf("proxy should have been hit exactly once, got %d", proxiedHits)
	}

	// A direct channel must bypass the proxy entirely.
	direct := RelayHTTPClient("")
	resp2, err := direct.Get(origin.URL)
	if err != nil {
		t.Fatalf("direct request failed: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	if string(body2) != "origin-ok" {
		t.Fatalf("direct request should reach the origin, got %q", body2)
	}
	if atomic.LoadInt32(&proxiedHits) != 1 {
		t.Fatalf("direct request must not touch the proxy, hits=%d", proxiedHits)
	}
}
