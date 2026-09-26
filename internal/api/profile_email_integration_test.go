package api

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hkjang/jupiq/internal/auth"
	"github.com/hkjang/jupiq/internal/secure"
	"github.com/hkjang/jupiq/internal/store"
)

// PATCH /api/v1/auth/me가 보낼 수 없는 주소를 프로덕션 배선에서 거부하는지,
// 빈 주소는 계속 허용하는지 확인한다. 거부가 없으면 메일 수신자 해석이 버리는
// 값이 users.email에 남아 그 사용자만 조용히 알림을 못 받는다.
func TestProfileEmailValidationHTTPIntegration(t *testing.T) {
	dsn := os.Getenv("JUPIQ_INTEGRATION_TEST_DSN")
	if dsn == "" {
		t.Skip("set JUPIQ_INTEGRATION_TEST_DSN to run PostgreSQL integration coverage")
	}
	ctx := context.Background()
	cipher, err := secure.NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(ctx, dsn, cipher)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	marker := fmt.Sprintf("api-profile-%d", time.Now().UnixNano())
	var userID int64
	if err := database.Pool.QueryRow(ctx, `INSERT INTO users(username,display_name,email,auth_source) VALUES($1,$1,$2,'local') RETURNING id`, marker+"-user", marker+"-keep@corp.internal").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID)
	}()
	user, err := database.GetUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	authService := auth.NewService(database, cipher)
	_, token, expires, err := authService.CreateSession(ctx, user, "127.0.0.1", "profile-email-test")
	if err != nil {
		t.Fatal(err)
	}
	handler := New(database, authService, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	patch := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/auth/me", strings.NewReader(body))
		req.AddCookie(auth.SecureCookie(token, expires, false))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	storedEmail := func() string {
		t.Helper()
		var email string
		if err := database.Pool.QueryRow(ctx, `SELECT email FROM users WHERE id=$1`, userID).Scan(&email); err != nil {
			t.Fatal(err)
		}
		return email
	}

	response := patch(`{"email":"  a@corp.internal  ","display_name":" 홍길동 "}`)
	if response.Code != http.StatusOK {
		t.Fatalf("a padded but valid address must be accepted: status=%d body=%s", response.Code, response.Body.String())
	}
	if body := response.Body.String(); !strings.Contains(body, `"email":"a@corp.internal"`) || !strings.Contains(body, `"display_name":"홍길동"`) {
		t.Fatalf("the response must carry trimmed values: %s", body)
	}
	if got := storedEmail(); got != "a@corp.internal" {
		t.Fatalf("users.email must be stored trimmed: %q", got)
	}

	// 비어 있지 않고 보낼 수 없는 주소만 400이다.
	response = patch(`{"email":"nonsense","display_name":"홍길동"}`)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"invalid_email"`) {
		t.Fatalf("an unsendable address was not rejected: status=%d body=%s", response.Code, response.Body.String())
	}
	if got := storedEmail(); got != "a@corp.internal" {
		t.Fatalf("a rejected request must not change users.email: %q", got)
	}

	// 빈 값은 "주소를 지운다"는 뜻이라 절대 400이 아니다 — Seed 계정(email='')의
	// 프로필 저장도 이 경로를 쓴다.
	response = patch(`{"email":"  \t\r\n ","display_name":"홍길동"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("a blank address must clear the value, not fail: status=%d body=%s", response.Code, response.Body.String())
	}
	if got := storedEmail(); got != "" {
		t.Fatalf("a blank address must clear users.email: %q", got)
	}
}
