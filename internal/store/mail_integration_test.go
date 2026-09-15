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
		_, _ = database.Pool.Exec(ctx, `DELETE FROM users WHERE username=$1`, marker)
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
}
