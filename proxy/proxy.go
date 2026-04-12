package proxy

import (
	"fmt"
	"net/http"

	"golang.org/x/net/proxy"
)

// ProxyInfo holds connection details for a single proxy
type ProxyInfo struct {
	Address  string // host:port
	Username string
	Password string
	Protocol string // "socks5" or "http"
}

// ProxyProvider is the interface for proxy implementations
type ProxyProvider interface {
	Init() error
	GetProxy(accountKey string) (*ProxyInfo, error)
	ReleaseProxy(accountKey string) error
}

// MakeHTTPClient creates an HTTP client that routes through the given proxy.
// Returns a plain client if proxyInfo is nil.
func MakeHTTPClient(proxyInfo *ProxyInfo, timeout int) (*http.Client, error) {
	client := &http.Client{
		Timeout: 0, // set by caller
	}

	if proxyInfo == nil {
		return client, nil
	}

	if proxyInfo.Protocol == "socks5" {
		var auth *proxy.Auth
		if proxyInfo.Username != "" {
			auth = &proxy.Auth{
				User:     proxyInfo.Username,
				Password: proxyInfo.Password,
			}
		}
		dialer, err := proxy.SOCKS5("tcp", proxyInfo.Address, auth, proxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("failed to create SOCKS5 dialer: %w", err)
		}
		client.Transport = &http.Transport{
			Dial: dialer.Dial,
		}
	}
	// HTTP proxy could be added here in the future

	return client, nil
}

// ParseProxyURL parses a proxy URL string like "socks5://user:pass@host:port"
func ParseProxyURL(rawURL string) (*ProxyInfo, error) {
	if rawURL == "" {
		return nil, fmt.Errorf("empty proxy URL")
	}

	// Simple parser for socks5://user:pass@host:port
	info := &ProxyInfo{}

	if len(rawURL) > 9 && rawURL[:9] == "socks5://" {
		info.Protocol = "socks5"
		rawURL = rawURL[9:]
	} else if len(rawURL) > 7 && rawURL[:7] == "http://" {
		info.Protocol = "http"
		rawURL = rawURL[7:]
	} else {
		info.Protocol = "socks5"
	}

	// Check for auth: user:pass@host:port
	if atIdx := lastIndexByte(rawURL, '@'); atIdx != -1 {
		authPart := rawURL[:atIdx]
		info.Address = rawURL[atIdx+1:]
		if colonIdx := indexByte(authPart, ':'); colonIdx != -1 {
			info.Username = authPart[:colonIdx]
			info.Password = authPart[colonIdx+1:]
		} else {
			info.Username = authPart
		}
	} else {
		info.Address = rawURL
	}

	return info, nil
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func lastIndexByte(s string, c byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == c {
			return i
		}
	}
	return -1
}
