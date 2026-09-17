// Package authtest는 테스트용 가짜 OpenID 제공자다. 실제 RSA 키 쌍을 만들어
// Discovery·JWKS를 서빙하고 그 키로 JWT를 서명한다 — 검증 코드는 프로덕션
// 배선 그대로(go-oidc, SafeHTTPClient) 진짜 서명을 본다.
//
// 연동 HTTP 클라이언트가 loopback 대상을 막으므로(SSRF 방어) 제공자는
// loopback이 아닌 로컬 인터페이스 주소에 묶인다. 그런 주소가 없는 환경에서는
// 테스트를 건너뛴다.
package authtest

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

type FakeIDP struct {
	Server *httptest.Server
	Key    *rsa.PrivateKey
	KeyID  string
	// JWKSRequests counts key-set fetches so tests can see caching at work.
	JWKSRequests atomic.Int64
}

// URL은 issuer다.
func (idp *FakeIDP) URL() string { return idp.Server.URL }

// NonLoopbackAddr는 연동 클라이언트가 허용하는 로컬 IPv4 주소를 찾는다.
func NonLoopbackAddr(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Skipf("interfaces unavailable: %v", err)
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipNet.IP.To4()
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			continue
		}
		return ip.String()
	}
	t.Skip("no non-loopback IPv4 interface for the fake identity provider")
	return ""
}

// NewFakeIDP는 제공자를 띄운다. 테스트가 끝나면 닫힌다.
func NewFakeIDP(t *testing.T) *FakeIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp := &FakeIDP{Key: key, KeyID: "test-key-1"}
	listener, err := net.Listen("tcp", net.JoinHostPort(NonLoopbackAddr(t), "0"))
	if err != nil {
		t.Skipf("cannot listen on a non-loopback address: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                idp.Server.URL,
			"authorization_endpoint":                idp.Server.URL + "/authorize",
			"token_endpoint":                        idp.Server.URL + "/token",
			"jwks_uri":                              idp.Server.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, r *http.Request) {
		idp.JWKSRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "alg": "RS256", "use": "sig", "kid": idp.KeyID,
			"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
		}}})
	})
	idp.Server = httptest.NewUnstartedServer(mux)
	idp.Server.Listener = listener
	idp.Server.Start()
	t.Cleanup(idp.Server.Close)
	return idp
}

// Sign은 제공자의 키로 RS256 JWT를 만든다. claims에 iss가 없으면 제공자 자신이다.
func (idp *FakeIDP) Sign(t *testing.T, claims map[string]any) string {
	t.Helper()
	return idp.SignWith(t, jwt.SigningMethodRS256, idp.Key, claims)
}

// SignWith는 알고리즘과 키를 고를 수 있다(HS256·다른 제공자의 키 같은 거부 사례용).
func (idp *FakeIDP) SignWith(t *testing.T, method jwt.SigningMethod, key any, claims map[string]any) string {
	t.Helper()
	payload := jwt.MapClaims{}
	for name, value := range claims {
		payload[name] = value
	}
	if _, ok := payload["iss"]; !ok {
		payload["iss"] = idp.Server.URL
	}
	token := jwt.NewWithClaims(method, payload)
	token.Header["kid"] = idp.KeyID
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}
