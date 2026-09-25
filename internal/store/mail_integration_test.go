package store

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hkjang/jupiq/internal/mail"
	"github.com/hkjang/jupiq/internal/secure"
)

// 메일 기능이 저장소에서 빌려 쓰는 조회들이 실제 PostgreSQL에서 계약대로
// 동작하는지 — 기본값은 꺼짐, 비밀번호는 secrets로만, 발송 기록은 성공·실패
// 모두, 만료 임박 키는 한 번만.
func TestMailStoreContractsIntegration(t *testing.T) {
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
	marker := fmt.Sprintf("mail-%d", time.Now().UnixNano())
	if err := database.Seed(ctx, marker, "IntegrationPassword!123"); err != nil {
		t.Fatal(err)
	}
	var adminID int64
	if err := database.Pool.QueryRow(ctx, `SELECT id FROM users WHERE username=$1`, marker).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	var previousMail []byte
	if err := database.Pool.QueryRow(ctx, `SELECT value FROM settings WHERE setting_key=$1`, mail.SettingKey).Scan(&previousMail); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = database.Pool.Exec(ctx, `UPDATE settings SET value=$2 WHERE setting_key=$1`, mail.SettingKey, previousMail)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM mail_deliveries WHERE actor_user_id=$1 OR recipient LIKE $2`, adminID, marker+"%")
		_, _ = database.Pool.Exec(ctx, `DELETE FROM secrets WHERE secret_key=$1`, mail.SecretKey)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM users WHERE username LIKE $1`, marker+"%")
	}()

	// 새로 설치한 곳: 꺼져 있고 비밀번호가 없다.
	raw, password, err := database.MailSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if config := mail.ParseConfig(raw, password); config.Enabled || password != "" || config.Port != 25 {
		t.Fatalf("fresh install must be disabled with relay defaults: %+v", config)
	}

	// 비밀번호는 secrets에 암호화 저장되고 설정 문서에는 나타나지 않는다.
	if err := database.UpdateSettingsAndSecrets(ctx, map[string]any{mail.SettingKey: map[string]any{"enabled": true, "smtp_host": "relay.internal", "from_address": "jupiq@corp.internal", "username": "jupiq"}}, map[string]string{mail.SecretKey: "s3cret"}, adminID); err != nil {
		t.Fatal(err)
	}
	raw, password, err = database.MailSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if password != "s3cret" || string(raw) == "" {
		t.Fatalf("password must come back decrypted from secrets: %q", password)
	}
	settings, err := database.ListSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if doc := string(settings[mail.SettingKey]); strings.Contains(doc, "s3cret") || strings.Contains(doc, "password") {
		t.Fatalf("settings document leaks the password: %s", doc)
	}
	status, err := database.SecretStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, configured := status[mail.SecretKey]; !configured {
		t.Fatal("secret status must report mail.password as configured")
	}
	// 릴레이 주소를 바꾸면 비밀번호를 다시 입력해야 한다(다른 연동과 같은 규칙).
	if err := database.UpdateSettingsAndSecrets(ctx, map[string]any{mail.SettingKey: map[string]any{"enabled": true, "smtp_host": "other.internal", "from_address": "jupiq@corp.internal"}}, nil, adminID); err != ErrSettingsSecretReentry {
		t.Fatalf("host change without password must require re-entry: %v", err)
	}

	// 수신자 조회: 활성이고 주소가 있는 사용자만.
	if _, err := database.Pool.Exec(ctx, `UPDATE users SET email=$2 WHERE id=$1`, adminID, marker+"@corp.internal"); err != nil {
		t.Fatal(err)
	}
	emails, err := database.UserEmails(ctx, []int64{adminID, -1})
	if err != nil {
		t.Fatal(err)
	}
	if emails[adminID] != marker+"@corp.internal" || len(emails) != 1 {
		t.Fatalf("email lookup: %v", emails)
	}
	admins, err := database.UsersWithGlobalPermission(ctx, "hubs:write")
	if err != nil {
		t.Fatal(err)
	}
	if !containsInt64(admins, adminID) {
		t.Fatalf("super admin (*) must hold hubs:write: %v", admins)
	}

	// 발송 기록: 성공과 실패가 모두 남고 본문 열은 없다.
	sentID, err := database.RecordMailDelivery(ctx, mail.Delivery{Event: mail.EventTest, Recipient: marker + "@corp.internal", Subject: "시험", ActorUserID: &adminID})
	if err != nil {
		t.Fatal(err)
	}
	failedID, err := database.RecordMailDelivery(ctx, mail.Delivery{Event: mail.EventHubHealth, Recipient: marker + "@corp.internal", Subject: "Hub 실패", Reference: "hub:1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.FinishMailDelivery(ctx, sentID, mail.StatusSent, 1, ""); err != nil {
		t.Fatal(err)
	}
	if err := database.FinishMailDelivery(ctx, failedID, mail.StatusFailed, 2, "connection refused"); err != nil {
		t.Fatal(err)
	}
	if err := database.FinishMailDelivery(ctx, failedID, "queued", 1, ""); err == nil {
		t.Fatal("finish must refuse to move a delivery back to queued")
	}
	page, err := database.ListMailDeliveries(ctx, "", 200)
	if err != nil {
		t.Fatal(err)
	}
	var sawSent, sawFailed bool
	for _, item := range page.Items {
		switch item.ID {
		case sentID:
			sawSent = item.Status == mail.StatusSent && item.Attempts == 1 && item.ActorUserID != nil && *item.ActorUserID == adminID
		case failedID:
			sawFailed = item.Status == mail.StatusFailed && item.Attempts == 2 && item.ErrorMessage == "connection refused" && item.Reference == "hub:1"
		}
	}
	if !sawSent || !sawFailed || page.Summary[mail.StatusSent] < 1 || page.Summary[mail.StatusFailed] < 1 {
		t.Fatalf("both outcomes must be listed with a summary: %+v", page)
	}
	if only, err := database.ListMailDeliveries(ctx, mail.StatusFailed, 200); err != nil || len(only.Items) == 0 || only.Items[0].Status != mail.StatusFailed {
		t.Fatalf("status filter: %v %+v", err, only)
	}

	// 만료 임박 키: 7일 안의 활성 키만, 한 번만.
	soon := time.Now().Add(3 * 24 * time.Hour)
	later := time.Now().Add(30 * 24 * time.Hour)
	if _, _, err := database.CreateAPIKey(ctx, adminID, marker+"-soon", []string{"hubs:read"}, &soon, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.CreateAPIKey(ctx, adminID, marker+"-later", []string{"hubs:read"}, &later, nil); err != nil {
		t.Fatal(err)
	}
	expiring, err := database.ExpiringAPIKeys(ctx, 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if keys := expiring[adminID]; len(keys) != 1 || keys[0].Name != marker+"-soon" || keys[0].Prefix == "" {
		t.Fatalf("only the key expiring within the window must be returned: %+v", expiring)
	}
	again, err := database.ExpiringAPIKeys(ctx, 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(again[adminID]) != 0 {
		t.Fatalf("a key must be reported once: %+v", again)
	}

	// 보낼 주소가 없는 소유자의 키는 표시하지 않는다. Notify는 주소가 0개면
	// mail_deliveries도 로그도 남기지 않고 돌아가므로, 여기서 안내한 것으로
	// 표시하면 그 키는 어디에도 흔적 없이 조용히 만료된다.
	silentID := createTestUser(ctx, t, database, marker+"-silent", "")
	inactiveID := createTestUser(ctx, t, database, marker+"-inactive", marker+"-inactive@corp.internal")
	if _, err := database.Pool.Exec(ctx, `UPDATE users SET active=false WHERE id=$1`, inactiveID); err != nil {
		t.Fatal(err)
	}
	// UpdateProfile은 입력을 다듬지 않으므로 공백류만 든 주소가 실제로 저장된다.
	// 공백 하나만이 아니라 탭·줄바꿈·NBSP까지 세운다 — Go의 TrimSpace가 지우는
	// 문자는 전부 "보낼 곳이 없다"로 읽혀야 한다(btrim은 공백만 지워 여기서 갈렸다).
	blankForms := map[string]string{
		"spaces": "   ",
		"tab":    "\t",
		"crlf":   "\r\n",
		"nbsp":   "\u00a0",
		"mixed":  " \t\r\n\v\f\u0085\u00a0\u2028\u3000",
	}
	unreachableOwners := []int64{silentID, inactiveID}
	for name, address := range blankForms {
		if strings.TrimSpace(address) != "" {
			t.Fatalf("test data %s must be blank after TrimSpace: %q", name, address)
		}
		unreachableOwners = append(unreachableOwners, createTestUser(ctx, t, database, marker+"-blank-"+name, address))
	}
	blankOwners := unreachableOwners[2:]
	// 반대쪽 경계: 공백에 둘러싸인 멀쩡한 주소는 resolve가 다듬어 실제로 보내니
	// 조건이 이것까지 걸러서는 안 된다.
	paddedID := createTestUser(ctx, t, database, marker+"-padded", "\t "+marker+"-padded@corp.internal \r\n")
	for _, owner := range append([]int64{paddedID}, unreachableOwners...) {
		if _, _, err := database.CreateAPIKey(ctx, owner, fmt.Sprintf("%s-unreachable-%d", marker, owner), []string{"hubs:read"}, &soon, nil); err != nil {
			t.Fatal(err)
		}
	}
	unreachable, err := database.ExpiringAPIKeys(ctx, 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, owner := range unreachableOwners {
		if len(unreachable[owner]) != 0 {
			t.Fatalf("keys of owner %d with no deliverable address must not be reported: %+v", owner, unreachable)
		}
	}
	if keys := unreachable[paddedID]; len(keys) != 1 {
		t.Fatalf("a whitespace-padded but deliverable address must still be notified: %+v", unreachable)
	}
	for _, owner := range unreachableOwners {
		var notified *time.Time
		if err := database.Pool.QueryRow(ctx, `SELECT expiry_notified_at FROM api_keys WHERE user_id=$1`, owner).Scan(&notified); err != nil {
			t.Fatal(err)
		}
		if notified != nil {
			t.Fatalf("expiry_notified_at must stay NULL for owner %d: %v", owner, *notified)
		}
	}

	// 관리자가 주소를 채우면 그때 안내 기회가 살아 있어 한 번 나온다.
	if _, err := database.Pool.Exec(ctx, `UPDATE users SET email=$2 WHERE id=$1`, silentID, marker+"-silent@corp.internal"); err != nil {
		t.Fatal(err)
	}
	reachable, err := database.ExpiringAPIKeys(ctx, 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if keys := reachable[silentID]; len(keys) != 1 || keys[0].Name != fmt.Sprintf("%s-unreachable-%d", marker, silentID) {
		t.Fatalf("filling in the address must hand the key over once: %+v", reachable)
	}
	if len(reachable[inactiveID]) != 0 {
		t.Fatalf("an inactive owner must stay out of the notification: %+v", reachable)
	}
	for _, owner := range blankOwners {
		if len(reachable[owner]) != 0 {
			t.Fatalf("blank-address owner %d must stay out of the notification: %+v", owner, reachable)
		}
	}
	if last, err := database.ExpiringAPIKeys(ctx, 7*24*time.Hour); err != nil || len(last[silentID]) != 0 {
		t.Fatalf("the key must still be reported only once: %v %+v", err, last)
	}
}

// createTestUser는 통합 테스트용 지역 계정을 만든다. Seed와 달리 email을 직접
// 정해 수신자 해석 경로를 구분해 볼 수 있게 한다.
func createTestUser(ctx context.Context, t *testing.T, database *Store, username, email string) int64 {
	t.Helper()
	var id int64
	if err := database.Pool.QueryRow(ctx, `
		INSERT INTO users(username, display_name, email, auth_source)
		VALUES($1,$1,$2,'local') RETURNING id`, username, email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
