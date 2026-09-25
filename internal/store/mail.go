package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/hkjang/jupiq/internal/mail"
)

// mailDeliveryRetention은 발송 기록을 남겨 두는 기간이다. 하루에 몇 통이라
// 부담은 작지만 무한히 쌓이지는 않게 한다.
const mailDeliveryRetention = 180 * 24 * time.Hour

// MailSettings는 mail 문서와 SMTP 비밀번호를 한 MVCC 스냅샷으로 돌려준다.
// 문서가 없으면 빈 값이고(꺼짐), 비밀번호가 없으면 빈 문자열이다.
func (s *Store) MailSettings(ctx context.Context) (json.RawMessage, string, error) {
	settings, password, _, err := s.GetSettingsAndSecret(ctx, []string{mail.SettingKey}, mail.SecretKey)
	if err != nil {
		return nil, "", err
	}
	return settings[mail.SettingKey], password, nil
}

// UserEmails는 계정 id를 메일 주소로 바꾼다. 메일 기능이 자기 사용자 표를
// 갖지 않도록, users 테이블을 빌려 쓰는 유일한 자리다.
func (s *Store) UserEmails(ctx context.Context, userIDs []int64) (map[int64]string, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id,email FROM users WHERE id=ANY($1) AND active AND email<>''`, userIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	emails := map[int64]string{}
	for rows.Next() {
		var id int64
		var email string
		if err := rows.Scan(&id, &email); err != nil {
			return nil, err
		}
		emails[id] = strings.TrimSpace(email)
	}
	return emails, rows.Err()
}

// UsersWithGlobalPermission은 전역 범위 역할로 그 권한을 가진 활성 사용자
// id다. 알림의 수신자를 정할 때 쓴다. 특정 Hub·부서로 제한된 바인딩은 대상이
// 그 범위에 드는지 알 수 없으므로 세지 않는다.
func (s *Store) UsersWithGlobalPermission(ctx context.Context, permission string) ([]int64, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT u.id, r.permissions
		FROM users u
		JOIN user_roles ur ON ur.user_id=u.id
		JOIN roles r ON r.id=ur.role_id
		WHERE u.active AND ur.scope_mode='global'
		ORDER BY u.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	seen := map[int64]struct{}{}
	for rows.Next() {
		var id int64
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		if _, done := seen[id]; done {
			continue
		}
		var permissions []string
		_ = json.Unmarshal(raw, &permissions)
		if EnsurePermission(permissions, permission) {
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

func (s *Store) RecordMailDelivery(ctx context.Context, delivery mail.Delivery) (int64, error) {
	var id int64
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO mail_deliveries(event,recipient,subject,reference,actor_user_id,status,attempts)
		VALUES($1,$2,$3,$4,$5,$6,0) RETURNING id`,
		delivery.Event, delivery.Recipient, truncate(delivery.Subject, 300), truncate(delivery.Reference, 300), delivery.ActorUserID, mail.StatusQueued).Scan(&id)
	return id, err
}

func (s *Store) FinishMailDelivery(ctx context.Context, id int64, status string, attempts int, errorMessage string) error {
	if status != mail.StatusSent && status != mail.StatusFailed {
		return errors.New("mail delivery status must be sent or failed")
	}
	_, err := s.Pool.Exec(ctx, `UPDATE mail_deliveries SET status=$2,attempts=GREATEST(attempts,$3),error_message=$4,updated_at=now() WHERE id=$1`,
		id, status, attempts, truncate(errorMessage, 1000))
	return err
}

// MailDeliveryPage는 발송 기록 목록과 상태별 집계다.
type MailDeliveryPage struct {
	Items   []mail.Delivery `json:"items"`
	Total   int             `json:"total"`
	Summary map[string]int  `json:"summary"`
}

// ListMailDeliveries는 최신순 발송 기록이다. status가 비어 있지 않으면 그
// 상태만 고른다.
func (s *Store) ListMailDeliveries(ctx context.Context, status string, limit int) (MailDeliveryPage, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	page := MailDeliveryPage{Items: []mail.Delivery{}, Summary: map[string]int{}}
	rows, err := s.Pool.Query(ctx, `
		SELECT id,event,recipient,subject,reference,actor_user_id,status,attempts,error_message,created_at,updated_at
		FROM mail_deliveries WHERE $1='' OR status=$1
		ORDER BY created_at DESC, id DESC LIMIT $2`, strings.TrimSpace(status), limit)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var item mail.Delivery
		if err := rows.Scan(&item.ID, &item.Event, &item.Recipient, &item.Subject, &item.Reference, &item.ActorUserID, &item.Status, &item.Attempts, &item.ErrorMessage, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return page, err
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	counts, err := s.Pool.Query(ctx, `SELECT status, count(*) FROM mail_deliveries GROUP BY 1`)
	if err != nil {
		return page, err
	}
	defer counts.Close()
	for counts.Next() {
		var key string
		var count int
		if err := counts.Scan(&key, &count); err != nil {
			return page, err
		}
		page.Summary[key] = count
		page.Total += count
	}
	return page, counts.Err()
}

// PruneMailDeliveries는 오래된 발송 기록을 지운다.
func (s *Store) PruneMailDeliveries(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM mail_deliveries WHERE created_at < now() - $1::interval`, mailDeliveryRetention)
	return err
}

// ExpiringAPIKeys는 아직 안내하지 않은, 만료가 within 안으로 다가온 활성
// 키를 소유자별로 묶어 돌려준다. 돌려준 키는 안내한 것으로 표시하므로 릴레이가
// 죽어 있어도 같은 키에 되풀이 보내지 않는다 — 발송 실패는 기록에 남는다.
// 표시하는 것은 보낼 주소가 있는 소유자의 키뿐이다 — 조건은 UserEmails가
// 수신자를 해석할 때와 같게 두어(활성이고, 다듬은 email이 비어 있지 않음) 두
// 경로가 같은 계정을 같게 읽는다. 주소가 없으면 Notify가 기록도 로그도 없이
// 돌아가므로, 표시부터 하면 그 키는 흔적 없이 조용히 만료된다 — 나중에 주소를
// 채우면 그때 한 번 안내한다.
func (s *Store) ExpiringAPIKeys(ctx context.Context, within time.Duration) (map[int64][]mail.ExpiringKey, error) {
	rows, err := s.Pool.Query(ctx, `
		UPDATE api_keys SET expiry_notified_at=now()
		WHERE status='active' AND expiry_notified_at IS NULL
		  AND expires_at IS NOT NULL AND expires_at > now() AND expires_at <= now() + $1::interval
		  AND user_id IN (SELECT id FROM users WHERE active AND btrim(email)<>'')
		RETURNING id,user_id,name,prefix,expires_at`, within)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byUser := map[int64][]mail.ExpiringKey{}
	for rows.Next() {
		var key mail.ExpiringKey
		var userID int64
		if err := rows.Scan(&key.ID, &userID, &key.Name, &key.Prefix, &key.ExpiresAt); err != nil {
			return nil, err
		}
		byUser[userID] = append(byUser[userID], key)
	}
	return byUser, rows.Err()
}
