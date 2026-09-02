package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/hkjang/jupiq/internal/secure"
	"github.com/hkjang/jupiq/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrNotFound         = errors.New("not found")
	ErrIdentityConflict = errors.New("identity conflicts with an existing username")
	ErrInvalidPassword  = errors.New("invalid current password")
	ErrLocalAuthOnly    = errors.New("password change is only available for local accounts")
)

type Store struct {
	Pool   *pgxpool.Pool
	Cipher *secure.Cipher
}

func Open(ctx context.Context, dsn string, cipher *secure.Cipher) (*Store, error) {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse PostgreSQL DSN: %w", err)
	}
	config.MaxConns = 20
	config.MinConns = 2
	config.MaxConnLifetime = 30 * time.Minute
	config.MaxConnIdleTime = 5 * time.Minute
	config.HealthCheckPeriod = time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping PostgreSQL: %w", err)
	}
	s := &Store{Pool: pool, Cipher: cipher}
	if err := s.Migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() { s.Pool.Close() }

func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.Pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("initialize migrations: %w", err)
	}
	entries, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		return err
	}
	sort.Strings(entries)
	for _, name := range entries {
		var applied bool
		err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name=$1)`, name).Scan(&applied)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		sql, err := migrations.FS.ReadFile(name)
		if err != nil {
			return err
		}
		tx, err := s.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, string(sql)); err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO schema_migrations(name) VALUES($1)`, name)
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Seed(ctx context.Context, username, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var userID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO users(username, display_name, password_hash, auth_source)
		VALUES($1,$1,$2,'local')
		ON CONFLICT (lower(username)) DO UPDATE SET username=EXCLUDED.username
		RETURNING id`, username, string(hash)).Scan(&userID)
	if err != nil {
		return fmt.Errorf("seed bootstrap administrator: %w", err)
	}
	permissions, _ := json.Marshal([]string{"*"})
	var roleID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO roles(role_key,name,description,permissions,system)
		VALUES('super_admin','최고 관리자','모든 관리 권한',$1,true)
		ON CONFLICT(role_key) DO UPDATE SET permissions=EXCLUDED.permissions, updated_at=now()
		RETURNING id`, permissions).Scan(&roleID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO user_roles(user_id,role_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, userID, roleID); err != nil {
		return err
	}
	userPermissions, _ := json.Marshal([]string{"profile:read", "profile:keys", "dashboard:read"})
	if _, err := tx.Exec(ctx, `INSERT INTO roles(role_key,name,description,permissions,system) VALUES('user','사용자','개인화 및 본인 사용량 조회',$1,true) ON CONFLICT(role_key) DO NOTHING`, userPermissions); err != nil {
		return err
	}
	defaults := map[string]any{
		"system":        map[string]any{"locale": "ko-KR", "timezone": "Asia/Seoul", "service_name": "jupiq", "raw_retention_days": 30, "page_size": 20},
		"workflow":      map[string]any{"approval_enabled": false, "manager_review_enabled": false, "require_reason": false, "request_types": []string{}},
		"auth.oidc":     map[string]any{"enabled": false, "issuer_url": "", "client_id": "", "redirect_url": "", "scopes": []string{"openid", "profile", "email"}, "username_claim": "preferred_username", "auto_create_users": true, "verify_tls": true},
		"ai":            map[string]any{"enabled": false, "provider": "openai-compatible", "base_url": "", "model": "", "max_tokens": 4096},
		"prometheus":    map[string]any{"enabled": false, "base_url": "", "verify_tls": true},
		"kubernetes":    map[string]any{"enabled": false, "base_url": "", "verify_tls": true},
		"notifications": map[string]any{"portal_enabled": true, "email_enabled": false, "webhook_enabled": false},
		"features":      map[string]any{"gpu_monitoring": false, "llm_usage_monitoring": false},
		"llm_usage":     map[string]any{"source": "prometheus", "pod_username_regex": "^jupyter-(?P<username>[a-zA-Z0-9._-]+)", "path_matcher": "/v1/chat/completions", "label_mappings": map[string]string{}, "promql": map[string]string{"calls": "sum(increase(http_requests_total{path=\"/v1/chat/completions\"}[1m])) by (pod,path,status,model,hub)"}, "input_cost_per_million": 0, "output_cost_per_million": 0, "stale_seconds": 300, "retention_days": 30},
		"security":      map[string]any{"key_rotation_days": 90, "key_max_lifetime_days": 365, "key_permissions": []string{}, "break_glass_enabled": false, "dangerous_action_reason": true},
	}
	for key, value := range defaults {
		blob, _ := json.Marshal(value)
		if _, err := tx.Exec(ctx, `INSERT INTO settings(setting_key,value) VALUES($1,$2) ON CONFLICT DO NOTHING`, key, blob); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) Ping(ctx context.Context) error { return s.Pool.Ping(ctx) }

func pageBounds(page, pageSize int) (int, int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 200 {
		pageSize = 200
	}
	return page, pageSize, (page - 1) * pageSize
}

func normalizeJSON(value any) []byte {
	if value == nil {
		return nil
	}
	b, _ := json.Marshal(value)
	return b
}

func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.Department, &u.AuthSource, &u.ExternalSubject, &u.Active, &u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt)
	return u, err
}

func dbNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if strings.EqualFold(v, value) {
			return true
		}
	}
	return false
}
