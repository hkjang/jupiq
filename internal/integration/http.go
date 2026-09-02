package integration

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxResponseBytes = 8 << 20

type HTTPOptions struct {
	VerifyTLS bool
	Timeout   time.Duration
}

var blockedMetadataHosts = map[string]struct{}{
	"metadata":                   {},
	"metadata.google.internal":   {},
	"metadata.azure.internal":    {},
	"instance-data":              {},
	"instance-data.ec2.internal": {},
}

func ValidateEndpoint(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("URL 형식이 올바르지 않습니다: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("http 또는 https 주소만 사용할 수 있습니다")
	}
	if u.Hostname() == "" {
		return nil, errors.New("호스트 이름이 필요합니다")
	}
	hostname := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if hostname == "localhost" {
		return nil, errors.New("loopback 주소는 연동 대상으로 사용할 수 없습니다")
	}
	if _, blocked := blockedMetadataHosts[hostname]; blocked {
		return nil, errors.New("클라우드 metadata 주소는 연동 대상으로 사용할 수 없습니다")
	}
	if ip := net.ParseIP(hostname); ip != nil {
		if err := validateDestinationIP(ip); err != nil {
			return nil, err
		}
	}
	if u.User != nil {
		return nil, errors.New("URL에 사용자 정보(userinfo)를 포함할 수 없습니다")
	}
	if u.Fragment != "" {
		return nil, errors.New("URL fragment는 사용할 수 없습니다")
	}
	return u, nil
}

func validateDestinationIP(ip net.IP) error {
	if ip == nil {
		return errors.New("대상 IP 주소를 확인할 수 없습니다")
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return fmt.Errorf("허용되지 않는 로컬·link-local 대상 IP입니다: %s", ip.String())
	}
	// Well-known provider metadata endpoints that are not always link-local.
	for _, raw := range []string{"100.100.100.200", "192.0.0.192"} {
		if ip.Equal(net.ParseIP(raw)) {
			return errors.New("클라우드 metadata IP는 연동 대상으로 사용할 수 없습니다")
		}
	}
	return nil
}

func safeDialContext(timeout time.Duration) func(context.Context, string, string) (net.Conn, error) {
	dialTimeout := 5 * time.Second
	if timeout > 0 {
		dialTimeout = min(timeout, dialTimeout)
	}
	dialer := &net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("대상 주소 해석 실패: %w", err)
		}
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("대상 DNS 해석 실패: %w", err)
		}
		if len(ips) == 0 {
			return nil, errors.New("대상 DNS가 IP를 반환하지 않았습니다")
		}
		// Reject mixed safe/unsafe answers as well, preventing a resolver from
		// steering a later retry to loopback or metadata infrastructure.
		for _, ip := range ips {
			if err := validateDestinationIP(ip); err != nil {
				return nil, err
			}
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
	}
}

func newHTTPClient(options HTTPOptions) *http.Client {
	timeout := options.Timeout
	if timeout <= 0 || timeout > 30*time.Second {
		timeout = 10 * time.Second
	}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           safeDialContext(timeout),
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          20,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: timeout,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: !options.VerifyTLS}, // #nosec G402 -- explicit per-integration admin option
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("연동 대상 redirect가 3회를 초과했습니다")
			}
			if _, err := ValidateEndpoint(req.URL.String()); err != nil {
				return err
			}
			if len(via) > 0 && !strings.EqualFold(req.URL.Hostname(), via[len(via)-1].URL.Hostname()) {
				req.Header.Del("Authorization")
				req.Header.Del("Cookie")
			}
			return nil
		},
	}
}

// SafeHTTPClient is shared by adapters such as OIDC discovery so every
// administrator-configured endpoint uses the same proxy-free limits.
func SafeHTTPClient(verifyTLS bool, timeout time.Duration) *http.Client {
	return newHTTPClient(HTTPOptions{VerifyTLS: verifyTLS, Timeout: timeout})
}

func doJSON(ctx context.Context, client *http.Client, method, endpoint, token string, body io.Reader, target any, retry bool) (int, http.Header, error) {
	attempts := 1
	if retry && (method == http.MethodGet || method == http.MethodHead) {
		attempts = 2
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "jupiq-control-plane")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			if attempt+1 < attempts {
				continue
			}
			return 0, nil, err
		}
		defer resp.Body.Close()
		limited := io.LimitReader(resp.Body, maxResponseBytes+1)
		payload, err := io.ReadAll(limited)
		if err != nil {
			return resp.StatusCode, resp.Header, err
		}
		if len(payload) > maxResponseBytes {
			return resp.StatusCode, resp.Header, errors.New("원격 응답 크기가 8 MiB 제한을 초과했습니다")
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return resp.StatusCode, resp.Header, fmt.Errorf("원격 API가 HTTP %d를 반환했습니다", resp.StatusCode)
		}
		if target != nil && len(payload) > 0 {
			if err := json.Unmarshal(payload, target); err != nil {
				return resp.StatusCode, resp.Header, fmt.Errorf("원격 JSON 응답 해석 실패: %w", err)
			}
		}
		return resp.StatusCode, resp.Header, nil
	}
	return 0, nil, lastErr
}

func joinURL(base string, path string) (string, error) {
	u, err := ValidateEndpoint(base)
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(path, "/")
	u.RawQuery = ""
	return u.String(), nil
}
