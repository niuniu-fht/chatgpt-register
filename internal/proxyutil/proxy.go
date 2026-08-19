package proxyutil

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	xproxy "golang.org/x/net/proxy"
)

// Normalize accepts URL proxies and the compact host:port[:user:pass] format.
func Normalize(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.Contains(raw, "://") {
		return raw
	}
	parts := strings.Split(raw, ":")
	switch len(parts) {
	case 2:
		return "http://" + parts[0] + ":" + parts[1]
	case 4:
		return "http://" + url.QueryEscape(parts[2]) + ":" + url.QueryEscape(parts[3]) + "@" + parts[0] + ":" + parts[1]
	default:
		return "http://" + raw
	}
}

func Parse(raw string) (*url.URL, error) {
	u, err := url.Parse(Normalize(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("代理格式不正确")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
		return u, nil
	default:
		return nil, fmt.Errorf("代理协议不支持: %s", u.Scheme)
	}
}

func ServerAndAuth(raw string) (server, user, pass string, err error) {
	u, err := Parse(raw)
	if err != nil {
		return "", "", "", err
	}
	if u.User != nil {
		user = u.User.Username()
		pass, _ = u.User.Password()
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme == "socks5h" {
		scheme = "socks5"
	}
	return scheme + "://" + u.Host, user, pass, nil
}

func HTTPClient(raw string, timeout time.Duration) (*http.Client, error) {
	transport := &http.Transport{}
	if strings.TrimSpace(raw) == "" {
		return &http.Client{Timeout: timeout, Transport: transport}, nil
	}
	u, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		transport.Proxy = http.ProxyURL(u)
	case "socks5", "socks5h":
		var auth *xproxy.Auth
		if u.User != nil {
			password, _ := u.User.Password()
			auth = &xproxy.Auth{User: u.User.Username(), Password: password}
		}
		dialer, dialErr := xproxy.SOCKS5("tcp", u.Host, auth, xproxy.Direct)
		if dialErr != nil {
			return nil, fmt.Errorf("创建 SOCKS5 代理连接: %w", dialErr)
		}
		if contextDialer, ok := dialer.(xproxy.ContextDialer); ok {
			transport.DialContext = contextDialer.DialContext
		} else {
			transport.DialContext = func(_ context.Context, network, address string) (net.Conn, error) {
				return dialer.Dial(network, address)
			}
		}
	}
	return &http.Client{Timeout: timeout, Transport: transport}, nil
}

func List(raw string) []string {
	raw = strings.ReplaceAll(raw, ",", "\n")
	var result []string
	for _, line := range strings.Split(raw, "\n") {
		if item := strings.TrimSpace(line); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func Redacted(raw string) string {
	u, err := Parse(raw)
	if err != nil {
		return "格式错误"
	}
	u.User = nil
	return u.String()
}
