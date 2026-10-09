package client

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"

	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/logger"
)

var HTTPClient *http.Client
var ImpatientHTTPClient *http.Client
var UserContentRequestHTTPClient *http.Client

// proxyClientCache memoizes one client per distinct outbound proxy URL, so a
// per-channel proxy costs a single construction no matter how much traffic the
// channel serves.
var proxyClientCache sync.Map // proxyURL -> *http.Client

func Init() {
	if config.UserContentRequestProxy != "" {
		logger.SysLog(fmt.Sprintf("using %s as proxy to fetch user content", config.UserContentRequestProxy))
		proxyURL, err := url.Parse(config.UserContentRequestProxy)
		if err != nil {
			logger.FatalLog(fmt.Sprintf("USER_CONTENT_REQUEST_PROXY set but invalid: %s", config.UserContentRequestProxy))
		}
		transport := &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		}
		UserContentRequestHTTPClient = &http.Client{
			Transport: transport,
			Timeout:   time.Second * time.Duration(config.UserContentRequestTimeout),
		}
	} else {
		UserContentRequestHTTPClient = &http.Client{}
	}
	var transport http.RoundTripper
	if config.RelayProxy != "" {
		logger.SysLog(fmt.Sprintf("using %s as api relay proxy", config.RelayProxy))
		proxyURL, err := url.Parse(config.RelayProxy)
		if err != nil {
			logger.FatalLog(fmt.Sprintf("USER_CONTENT_REQUEST_PROXY set but invalid: %s", config.UserContentRequestProxy))
		}
		transport = &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		}
	}

	if config.RelayTimeout == 0 {
		HTTPClient = &http.Client{
			Transport: transport,
		}
	} else {
		HTTPClient = &http.Client{
			Timeout:   time.Duration(config.RelayTimeout) * time.Second,
			Transport: transport,
		}
	}

	ImpatientHTTPClient = &http.Client{
		Timeout:   5 * time.Second,
		Transport: transport,
	}
}

// RelayHTTPClient returns the client a relay request for this channel must use:
// the shared client when the channel declares no proxy, otherwise a client that
// routes the upstream call through the configured proxy server.
//
// A channel's own proxy always wins over the process-wide RELAY_PROXY, which is
// the whole point of configuring it per channel. An unparseable proxy URL never
// takes the channel offline: it logs and falls back to the shared client.
func RelayHTTPClient(proxyURL string) *http.Client {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL != "" {
		if cached, ok := proxyClientCache.Load(proxyURL); ok {
			return cached.(*http.Client)
		}
		if built := BuildProxyClient(proxyURL); built != nil {
			actual, _ := proxyClientCache.LoadOrStore(proxyURL, built)
			return actual.(*http.Client)
		}
		logger.SysError(fmt.Sprintf("channel proxy %q is not a valid http/https/socks5 URL, falling back to the default relay client", proxyURL))
	}
	if HTTPClient != nil {
		return HTTPClient
	}
	return http.DefaultClient
}

// BuildProxyClient builds a relay client whose transport dials through proxyURL.
// It supports http, https, socks5 and socks5h proxies. It returns nil when the
// URL cannot be understood, so callers can fall back deliberately.
func BuildProxyClient(proxyURL string) *http.Client {
	parsed, err := url.Parse(proxyURL)
	if err != nil || parsed.Host == "" {
		return nil
	}

	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	}

	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		// http.ProxyURL covers both plain HTTP proxies and CONNECT tunnels to
		// HTTPS destinations, which is what an https:// proxy URL means.
		transport.Proxy = http.ProxyURL(parsed)
	case "socks5", "socks5h":
		var auth *proxy.Auth
		if parsed.User != nil {
			password, _ := parsed.User.Password()
			auth = &proxy.Auth{
				User:     parsed.User.Username(),
				Password: password,
			}
		}
		dialer, derr := proxy.SOCKS5("tcp", parsed.Host, auth, proxy.Direct)
		if derr != nil {
			return nil
		}
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			// contextDialer is implemented by x/net/proxy socks dialers; the
			// fallback keeps older dialers working.
			type contextDialer interface {
				DialContext(ctx context.Context, network, address string) (net.Conn, error)
			}
			if cd, ok := dialer.(contextDialer); ok {
				return cd.DialContext(ctx, network, addr)
			}
			return dialer.Dial(network, addr)
		}
	default:
		return nil
	}

	c := &http.Client{Transport: transport}
	if config.RelayTimeout != 0 {
		c.Timeout = time.Duration(config.RelayTimeout) * time.Second
	}
	return c
}
