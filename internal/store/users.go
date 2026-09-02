package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hkjang/jupiq/internal/secure"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

const userColumns = `id,username,display_name,email,department,auth_source,external_subject,active,last_login_at,created_at,updated_at`

func (s *Store) GetUserByUsername(ctx context.Context, username string) (UserCredential, error) {
	var u UserCredential
	err := s.Pool.QueryRow(ctx, `SELECT `+userColumns+`,password_hash FROM users WHERE lower(username)=lower($1)`, username).Scan(
		&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.Department, &u.AuthSource, &u.ExternalSubject, &u.Active, &u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt, &u.PasswordHash,
	)
	if err != nil {
		return u, dbNotFound(err)
	}
	u.Roles, u.Permissions, err = s.userAccess(ctx, u.ID)
	return u, err
}

func (s *Store) GetUser(ctx context.Context, id int64) (User, error) {
	u, err := scanUser(s.Pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id=$1`, id))
	if err != nil {
		return u, dbNotFound(err)
	}
	u.Roles, u.Permissions, err = s.userAccess(ctx, u.ID)
	return u, err
}

func (s *Store) userAccess(ctx context.Context, userID int64) ([]string, []string, error) {
	rows, err := s.Pool.Query(ctx, `SELECT role_key,permissions FROM roles r JOIN user_roles ur ON ur.role_id=r.id WHERE ur.user_id=$1 ORDER BY role_key`, userID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	roles, permissions := []string{}, []string{}
	for rows.Next() {
		var role string
		var raw []byte
		if err := rows.Scan(&role, &raw); err != nil {
			return nil, nil, err
		}
		roles = append(roles, role)
		var values []string
		_ = json.Unmarshal(raw, &values)
		for _, permission := range values {
			if !contains(permissions, permission) {
				permissions = append(permissions, permission)
			}
		}
	}
	return roles, permissions, rows.Err()
}

func (s *Store) ListLocalUsers(ctx context.Context, page, pageSize int, search string) ([]User, Page, error) {
	page, pageSize, offset := pageBounds(page, pageSize)
	pattern := "%" + search + "%"
	var total int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE $1='' OR username ILIKE $2 OR display_name ILIKE $2 OR email ILIKE $2`, search, pattern).Scan(&total); err != nil {
		return nil, Page{}, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+userColumns+` FROM users WHERE $1='' OR username ILIKE $2 OR display_name ILIKE $2 OR email ILIKE $2 ORDER BY username LIMIT $3 OFFSET $4`, search, pattern, pageSize, offset)
	if err != nil {
		return nil, Page{}, err
	}
	defer rows.Close()
	users := make([]User, 0)
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, Page{}, err
		}
		u.Roles, u.Permissions, err = s.userAccess(ctx, u.ID)
		if err != nil {
			return nil, Page{}, err
		}
		users = append(users, u)
	}
	return users, Page{Page: page, PageSize: pageSize, Total: total}, rows.Err()
}

func (s *Store) UpsertOIDCUser(ctx context.Context, subject, username, displayName, email, department string, autoCreate bool) (User, error) {
	var subjectUserID int64
	err := s.Pool.QueryRow(ctx, `SELECT id FROM users WHERE external_subject=$1 AND auth_source='oidc'`, subject).Scan(&subjectUserID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return User{}, err
	}
	if subjectUserID == 0 {
		var usernameOwnerID int64
		usernameErr := s.Pool.QueryRow(ctx, `SELECT id FROM users WHERE lower(username)=lower($1)`, username).Scan(&usernameOwnerID)
		if usernameErr != nil && !errors.Is(usernameErr, pgx.ErrNoRows) {
			return User{}, usernameErr
		}
		create, decisionErr := oidcLinkDecision(subjectUserID, usernameOwnerID, autoCreate)
		if decisionErr != nil {
			return User{}, decisionErr
		}
		if create {
			err = s.Pool.QueryRow(ctx, `INSERT INTO users(username,display_name,email,department,auth_source,external_subject) VALUES($1,$2,$3,$4,'oidc',$5) RETURNING id`, username, displayName, email, department, subject).Scan(&subjectUserID)
		}
	} else {
		_, err = s.Pool.Exec(ctx, `UPDATE users SET username=$2,display_name=$3,email=$4,department=$5,updated_at=now() WHERE id=$1`, subjectUserID, username, displayName, email, department)
	}
	if err != nil {
		return User{}, err
	}
	var defaultRoleID int64
	if err := s.Pool.QueryRow(ctx, `SELECT id FROM roles WHERE role_key='user'`).Scan(&defaultRoleID); err == nil {
		_, _ = s.Pool.Exec(ctx, `INSERT INTO user_roles(user_id,role_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, subjectUserID, defaultRoleID)
	}
	return s.GetUser(ctx, subjectUserID)
}

func oidcLinkDecision(subjectUserID, usernameOwnerID int64, autoCreate bool) (bool, error) {
	if subjectUserID != 0 {
		return false, nil
	}
	if usernameOwnerID != 0 {
		return false, ErrIdentityConflict
	}
	if !autoCreate {
		return false, ErrNotFound
	}
	return true, nil
}

func (s *Store) TouchLogin(ctx context.Context, userID int64) error {
	_, err := s.Pool.Exec(ctx, `UPDATE users SET last_login_at=now(),updated_at=now() WHERE id=$1`, userID)
	return err
}

func (s *Store) UpdateProfile(ctx context.Context, userID int64, displayName, email, department string) (User, error) {
	result, err := s.Pool.Exec(ctx, `UPDATE users SET display_name=$2,email=$3,department=$4,updated_at=now() WHERE id=$1`, userID, displayName, email, department)
	if err != nil {
		return User{}, err
	}
	if result.RowsAffected() == 0 {
		return User{}, ErrNotFound
	}
	return s.GetUser(ctx, userID)
}

func (s *Store) ChangeLocalPassword(ctx context.Context, userID int64, currentPassword, newPassword string) error {
	if len(newPassword) < 12 {
		return fmt.Errorf("새 비밀번호는 12자 이상이어야 합니다")
	}
	var hash *string
	var authSource string
	if err := s.Pool.QueryRow(ctx, `SELECT password_hash,auth_source FROM users WHERE id=$1 AND active`, userID).Scan(&hash, &authSource); err != nil {
		return dbNotFound(err)
	}
	if authSource != "local" || hash == nil {
		return ErrLocalAuthOnly
	}
	if bcrypt.CompareHashAndPassword([]byte(*hash), []byte(currentPassword)) != nil {
		return ErrInvalidPassword
	}
	if bcrypt.CompareHashAndPassword([]byte(*hash), []byte(newPassword)) == nil {
		return fmt.Errorf("새 비밀번호는 현재 비밀번호와 달라야 합니다")
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `UPDATE users SET password_hash=$2,updated_at=now() WHERE id=$1`, userID, string(newHash))
	return err
}

func (s *Store) RevokeOtherSessions(ctx context.Context, userID int64, keepJTI string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE sessions SET revoked_at=COALESCE(revoked_at,now()) WHERE user_id=$1 AND jti<>$2 AND revoked_at IS NULL`, userID, keepJTI)
	return err
}

func (s *Store) CreateSession(ctx context.Context, jti string, userID int64, expires time.Time, ip, userAgent string) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO sessions(jti,user_id,expires_at,ip_address,user_agent) VALUES($1,$2,$3,$4,$5)`, jti, userID, expires, ip, userAgent)
	return err
}

func (s *Store) SessionActive(ctx context.Context, jti string, userID int64) (bool, error) {
	var active bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE jti=$1 AND user_id=$2 AND revoked_at IS NULL AND expires_at>now())`, jti, userID).Scan(&active)
	return active, err
}

func (s *Store) RevokeSession(ctx context.Context, jti string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE sessions SET revoked_at=COALESCE(revoked_at,now()) WHERE jti=$1`, jti)
	return err
}

func (s *Store) ListRoles(ctx context.Context) ([]Role, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id,role_key,name,description,permissions,system,created_at,updated_at FROM roles ORDER BY role_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	roles := []Role{}
	for rows.Next() {
		var r Role
		var raw []byte
		if err := rows.Scan(&r.ID, &r.Key, &r.Name, &r.Description, &raw, &r.System, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &r.Permissions)
		roles = append(roles, r)
	}
	return roles, rows.Err()
}

func (s *Store) SaveRole(ctx context.Context, role Role) (Role, error) {
	if role.Key == "" || role.Name == "" {
		return Role{}, errors.New("role key and name are required")
	}
	raw, _ := json.Marshal(role.Permissions)
	if role.ID == 0 {
		err := s.Pool.QueryRow(ctx, `INSERT INTO roles(role_key,name,description,permissions) VALUES($1,$2,$3,$4) RETURNING id,created_at,updated_at`, role.Key, role.Name, role.Description, raw).Scan(&role.ID, &role.CreatedAt, &role.UpdatedAt)
		return role, err
	}
	err := s.Pool.QueryRow(ctx, `UPDATE roles SET role_key=$2,name=$3,description=$4,permissions=$5,updated_at=now() WHERE id=$1 RETURNING system,created_at,updated_at`, role.ID, role.Key, role.Name, role.Description, raw).Scan(&role.System, &role.CreatedAt, &role.UpdatedAt)
	return role, dbNotFound(err)
}

func (s *Store) DeleteRole(ctx context.Context, id int64) error {
	result, err := s.Pool.Exec(ctx, `DELETE FROM roles WHERE id=$1 AND system=false`, id)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetUserRoles(ctx context.Context, userID int64, roleIDs []int64) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM user_roles WHERE user_id=$1`, userID); err != nil {
		return err
	}
	for _, roleID := range roleIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO user_roles(user_id,role_id) VALUES($1,$2)`, userID, roleID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) CreateAPIKey(ctx context.Context, userID int64, name string, scopes []string, expiresAt *time.Time, rotatedFrom *int64) (APIKey, string, error) {
	random, err := secure.RandomToken(32)
	if err != nil {
		return APIKey{}, "", err
	}
	plain := "jqk_" + random
	prefix := plain
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}
	rawScopes, _ := json.Marshal(scopes)
	var key APIKey
	err = s.Pool.QueryRow(ctx, `INSERT INTO api_keys(user_id,name,prefix,secret_hash,scopes,expires_at,rotated_from_id) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id,status,created_at`, userID, name, prefix, secure.HashToken(plain), rawScopes, expiresAt, rotatedFrom).Scan(&key.ID, &key.Status, &key.CreatedAt)
	if err != nil {
		return APIKey{}, "", err
	}
	key.UserID, key.Name, key.Prefix, key.Scopes, key.ExpiresAt = userID, name, prefix, scopes, expiresAt
	return key, plain, nil
}

func (s *Store) ListAPIKeys(ctx context.Context, userID int64) ([]APIKey, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id,user_id,name,prefix,scopes,status,last_used_at,expires_at,created_at,revoked_at FROM api_keys WHERE user_id=$1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []APIKey{}
	for rows.Next() {
		var k APIKey
		var scopes []byte
		if err := rows.Scan(&k.ID, &k.UserID, &k.Name, &k.Prefix, &scopes, &k.Status, &k.LastUsedAt, &k.ExpiresAt, &k.CreatedAt, &k.RevokedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(scopes, &k.Scopes)
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func (s *Store) AuthenticateAPIKey(ctx context.Context, plain string) (User, []string, int64, error) {
	var userID, keyID int64
	var scopesRaw []byte
	err := s.Pool.QueryRow(ctx, `SELECT user_id,id,scopes FROM api_keys WHERE secret_hash=$1 AND status='active' AND (expires_at IS NULL OR expires_at>now())`, secure.HashToken(plain)).Scan(&userID, &keyID, &scopesRaw)
	if err != nil {
		return User{}, nil, 0, dbNotFound(err)
	}
	_, _ = s.Pool.Exec(ctx, `UPDATE api_keys SET last_used_at=now() WHERE id=$1`, keyID)
	u, err := s.GetUser(ctx, userID)
	var scopes []string
	_ = json.Unmarshal(scopesRaw, &scopes)
	return u, scopes, keyID, err
}

func (s *Store) RevokeAPIKey(ctx context.Context, userID, id int64, status string) error {
	if status != "rotated" {
		status = "revoked"
	}
	result, err := s.Pool.Exec(ctx, `UPDATE api_keys SET status=$3,revoked_at=now() WHERE id=$1 AND user_id=$2 AND status='active'`, id, userID, status)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetAPIKey(ctx context.Context, userID, id int64) (APIKey, error) {
	keys, err := s.ListAPIKeys(ctx, userID)
	if err != nil {
		return APIKey{}, err
	}
	for _, key := range keys {
		if key.ID == id {
			return key, nil
		}
	}
	return APIKey{}, ErrNotFound
}

func (s *Store) RecordAudit(ctx context.Context, event AuditEvent) error {
	if event.Result == "" {
		event.Result = "success"
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO audit_logs(actor_user_id,actor_username,action,resource_type,resource_id,before_value,after_value,ip_address,user_agent,result,reason,request_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, event.ActorUserID, event.ActorUsername, event.Action, event.ResourceType, event.ResourceID, normalizeJSON(event.Before), normalizeJSON(event.After), event.IPAddress, event.UserAgent, event.Result, event.Reason, event.RequestID)
	return err
}

func (s *Store) ListAudit(ctx context.Context, page, pageSize int) ([]map[string]any, Page, error) {
	page, pageSize, offset := pageBounds(page, pageSize)
	var total int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs`).Scan(&total); err != nil {
		return nil, Page{}, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT id,actor_user_id,actor_username,action,resource_type,resource_id,before_value,after_value,ip_address,result,reason,request_id,created_at FROM audit_logs ORDER BY created_at DESC LIMIT $1 OFFSET $2`, pageSize, offset)
	if err != nil {
		return nil, Page{}, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var actorID *int64
		var actor, action, rt, rid, ip, result, reason, requestID string
		var before, after []byte
		var created time.Time
		if err := rows.Scan(&id, &actorID, &actor, &action, &rt, &rid, &before, &after, &ip, &result, &reason, &requestID, &created); err != nil {
			return nil, Page{}, err
		}
		item := map[string]any{"id": id, "actor_user_id": actorID, "actor_username": actor, "action": action, "resource_type": rt, "resource_id": rid, "ip_address": ip, "result": result, "reason": reason, "request_id": requestID, "created_at": created}
		if before != nil {
			item["before"] = json.RawMessage(before)
		}
		if after != nil {
			item["after"] = json.RawMessage(after)
		}
		items = append(items, item)
	}
	return items, Page{Page: page, PageSize: pageSize, Total: total}, rows.Err()
}

func EnsurePermission(permissions []string, required string) bool {
	for _, permission := range permissions {
		if permission == "*" || permission == required {
			return true
		}
		if len(permission) > 2 && permission[len(permission)-2:] == ":*" && len(required) >= len(permission)-1 && required[:len(permission)-1] == permission[:len(permission)-1] {
			return true
		}
	}
	return false
}

func ValidateScopes(scopes []string) error {
	if len(scopes) == 0 {
		return fmt.Errorf("at least one scope is required")
	}
	if len(scopes) > 64 {
		return fmt.Errorf("too many scopes")
	}
	for _, scope := range scopes {
		if len(scope) > 100 {
			return fmt.Errorf("scope is too long")
		}
	}
	return nil
}

func ScopesWithinPermissions(scopes, permissions []string) bool {
	if len(scopes) == 0 {
		return false
	}
	for _, scope := range scopes {
		if !EnsurePermission(permissions, scope) {
			return false
		}
	}
	return true
}
