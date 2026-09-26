package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hkjang/jupiq/internal/secure"
)

// users.email을 쓰는 두 경로(UpdateProfile·UpsertOIDCUser)가 저장 시점에 값을
// 다듬는지 실제 PostgreSQL에서 확인한다. 다듬지 않으면 "\t bob@corp.internal \r\n"
// 같은 값이 남고 메일 수신자 해석(mail.ValidAddress)이 그것을 버려 그 사용자는
// 알림을 못 받는다 — 흔적도 남지 않는다.
func TestProfileEmailNormalizationIntegration(t *testing.T) {
	dsn := os.Getenv("JUPIQ_INTEGRATION_TEST_DSN")
	if dsn == "" {
		t.Skip("set JUPIQ_INTEGRATION_TEST_DSN to run PostgreSQL integration coverage")
	}
	ctx := context.Background()
	cipher, err := secure.NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	database, err := Open(ctx, dsn, cipher)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	marker := fmt.Sprintf("profile-norm-%d", time.Now().UnixNano())
	defer func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM users WHERE username LIKE $1`, marker+"%")
	}()

	storedEmail := func(userID int64) string {
		t.Helper()
		var email string
		if err := database.Pool.QueryRow(ctx, `SELECT email FROM users WHERE id=$1`, userID).Scan(&email); err != nil {
			t.Fatal(err)
		}
		return email
	}

	t.Run("UpdateProfile trims display name and email", func(t *testing.T) {
		userID := createTestUser(ctx, t, database, marker+"-local", "")
		user, err := database.UpdateProfile(ctx, userID, " 홍길동 ", "\t a@corp.internal \r\n")
		if err != nil {
			t.Fatal(err)
		}
		if user.Email != "a@corp.internal" || user.DisplayName != "홍길동" {
			t.Fatalf("UpdateProfile must return trimmed values: display_name=%q email=%q", user.DisplayName, user.Email)
		}
		if got := storedEmail(userID); got != "a@corp.internal" {
			t.Fatalf("users.email must be stored trimmed: %q", got)
		}
	})

	t.Run("UpdateProfile stores blank-only email as empty", func(t *testing.T) {
		userID := createTestUser(ctx, t, database, marker+"-blank", "keep@corp.internal")
		// 탭·줄바꿈·NBSP만 든 주소는 "주소를 지운다"는 뜻이다. users.email은
		// NOT NULL DEFAULT ''라 빈 값은 계속 허용해야 한다.
		user, err := database.UpdateProfile(ctx, userID, "이름", " \t\r\n ")
		if err != nil {
			t.Fatal(err)
		}
		if user.Email != "" {
			t.Fatalf("blank-only email must clear the address: %q", user.Email)
		}
		if got := storedEmail(userID); got != "" {
			t.Fatalf("users.email must be stored empty: %q", got)
		}
	})

	t.Run("UpsertOIDCUser trims on create and on update", func(t *testing.T) {
		subject := marker + "-subject"
		created, err := database.UpsertOIDCUser(ctx, subject, marker+"-oidc", " 김철수 ", "\t bob@corp.internal \r\n", "AI", true)
		if err != nil {
			t.Fatal(err)
		}
		if created.Email != "bob@corp.internal" {
			t.Fatalf("OIDC create branch must store a trimmed email: %q", created.Email)
		}
		if got := storedEmail(created.ID); got != "bob@corp.internal" {
			t.Fatalf("users.email must be stored trimmed after OIDC create: %q", got)
		}
		// 두 번째 로그인은 갱신 갈래다 — 같은 정규화를 거쳐야 두 갈래가 갈리지 않는다.
		updated, err := database.UpsertOIDCUser(ctx, subject, marker+"-oidc", " 김철수 ", "   bob2@corp.internal\t", "AI", true)
		if err != nil {
			t.Fatal(err)
		}
		if updated.ID != created.ID {
			t.Fatalf("the second login must reuse the same account: %d != %d", updated.ID, created.ID)
		}
		if updated.Email != "bob2@corp.internal" {
			t.Fatalf("OIDC update branch must store a trimmed email: %q", updated.Email)
		}
		if got := storedEmail(updated.ID); got != "bob2@corp.internal" {
			t.Fatalf("users.email must be stored trimmed after OIDC update: %q", got)
		}
		// 모양이 이상해도 OIDC는 거부하지 않는다 — 로그인을 끊으면 안 된다.
		odd, err := database.UpsertOIDCUser(ctx, subject, marker+"-oidc", "김철수", "  nonsense  ", "AI", true)
		if err != nil {
			t.Fatalf("OIDC login must not be rejected for an odd address: %v", err)
		}
		if odd.Email != "nonsense" {
			t.Fatalf("OIDC must trim without rejecting: %q", odd.Email)
		}
	})
}
