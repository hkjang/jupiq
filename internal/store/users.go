package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
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
	err = s.populateUserAccess(ctx, &u.User)
	return u, err
}

func (s *Store) GetUser(ctx context.Context, id int64) (User, error) {
	u, err := scanUser(s.Pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id=$1`, id))
	if err != nil {
		return u, dbNotFound(err)
	}
	err = s.populateUserAccess(ctx, &u)
	return u, err
}

func (s *Store) populateUserAccess(ctx context.Context, user *User) error {
	rows, err := s.Pool.Query(ctx, `
		SELECT r.id,r.role_key,r.name,r.permissions,ur.scope_mode,c.scope_type,c.scope_value
		FROM roles r
		JOIN user_roles ur ON ur.role_id=r.id
		LEFT JOIN user_role_scope_clauses c ON c.user_id=ur.user_id AND c.role_id=ur.role_id
		WHERE ur.user_id=$1
		ORDER BY r.role_key,c.scope_type,c.scope_value`, user.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	user.Roles = []string{}
	user.Permissions = []string{}
	user.GlobalPermissions = []string{}
	user.ScopedPermissions = []string{}
	user.RoleBindings = []RoleBinding{}
	user.PermissionGrants = []PermissionGrant{}
	bindings := map[int64]int{}
	for rows.Next() {
		var roleID int64
		var roleKey, roleName, scopeMode string
		var raw []byte
		var scopeType, scopeValue *string
		if err := rows.Scan(&roleID, &roleKey, &roleName, &raw, &scopeMode, &scopeType, &scopeValue); err != nil {
			return err
		}
		index, exists := bindings[roleID]
		if !exists {
			var permissions []string
			_ = json.Unmarshal(raw, &permissions)
			index = len(user.RoleBindings)
			bindings[roleID] = index
			user.RoleBindings = append(user.RoleBindings, RoleBinding{RoleID: roleID, RoleKey: roleKey, RoleName: roleName, ScopeMode: scopeMode, Scopes: []ScopeClause{}, Permissions: permissions})
			user.Roles = append(user.Roles, roleKey)
			for _, permission := range permissions {
				if !contains(user.Permissions, permission) {
					user.Permissions = append(user.Permissions, permission)
				}
				if scopeMode == "global" && !contains(user.GlobalPermissions, permission) {
					user.GlobalPermissions = append(user.GlobalPermissions, permission)
				}
				if scopeMode == "restricted" && !contains(user.ScopedPermissions, permission) {
					user.ScopedPermissions = append(user.ScopedPermissions, permission)
				}
			}
		}
		if scopeType != nil && scopeValue != nil {
			value := strings.TrimSpace(*scopeValue)
			if value != "" {
				user.RoleBindings[index].Scopes = append(user.RoleBindings[index].Scopes, ScopeClause{Type: *scopeType, Value: value})
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, binding := range user.RoleBindings {
		if binding.ScopeMode != "restricted" {
			continue
		}
		grant := PermissionGrant{Permissions: binding.Permissions, HubIDs: []int64{}, Departments: []string{}}
		for _, clause := range binding.Scopes {
			switch clause.Type {
			case "hub":
				if id, ok := ParseHubScope(clause.Value); ok && !containsInt64(grant.HubIDs, id) {
					grant.HubIDs = append(grant.HubIDs, id)
				}
			case "department":
				value := strings.TrimSpace(clause.Value)
				if value != "" && !containsFold(grant.Departments, value) {
					grant.Departments = append(grant.Departments, value)
				}
			}
		}
		if len(grant.HubIDs) > 0 || len(grant.Departments) > 0 {
			user.PermissionGrants = append(user.PermissionGrants, grant)
		}
	}
	return nil
}

func containsInt64(values []int64, candidate int64) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func containsFold(values []string, candidate string) bool {
	for _, value := range values {
		if strings.EqualFold(value, candidate) {
			return true
		}
	}
	return false
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
		if err := s.populateUserAccess(ctx, &u); err != nil {
			return nil, Page{}, err
		}
		users = append(users, u)
	}
	return users, Page{Page: page, PageSize: pageSize, Total: total}, rows.Err()
}

func (s *Store) UpsertOIDCUser(ctx context.Context, subject, username, displayName, email, department string, autoCreate bool) (User, error) {
	var subjectUserID int64
	created := false
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
			created = err == nil
		}
	} else {
		_, err = s.Pool.Exec(ctx, `UPDATE users SET username=$2,display_name=$3,email=$4,department=$5,updated_at=now() WHERE id=$1`, subjectUserID, username, displayName, email, department)
	}
	if err != nil {
		return User{}, err
	}
	if created {
		var defaultRoleID int64
		if err := s.Pool.QueryRow(ctx, `SELECT id FROM roles WHERE role_key='user'`).Scan(&defaultRoleID); err == nil {
			_, _ = s.Pool.Exec(ctx, `INSERT INTO user_roles(user_id,role_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, subjectUserID, defaultRoleID)
		}
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

func (s *Store) UpdateProfile(ctx context.Context, userID int64, displayName, email string) (User, error) {
	// Department is organization-owned identity data synchronized by an
	// administrator or OIDC. A user may not move themselves between scopes via
	// the personal profile endpoint.
	result, err := s.Pool.Exec(ctx, `UPDATE users SET display_name=$2,email=$3,updated_at=now() WHERE id=$1`, userID, displayName, email)
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

func (s *Store) GetRole(ctx context.Context, id int64) (Role, error) {
	var role Role
	var raw []byte
	err := s.Pool.QueryRow(ctx, `SELECT id,role_key,name,description,permissions,system,created_at,updated_at FROM roles WHERE id=$1`, id).Scan(
		&role.ID, &role.Key, &role.Name, &role.Description, &raw, &role.System, &role.CreatedAt, &role.UpdatedAt,
	)
	if err != nil {
		return Role{}, dbNotFound(err)
	}
	if err := json.Unmarshal(raw, &role.Permissions); err != nil {
		return Role{}, err
	}
	return role, nil
}

func (s *Store) SaveRole(ctx context.Context, role Role) (Role, error) {
	return s.saveRole(ctx, role, 0)
}

// SaveRoleAuthorized validates both the role being replaced and its replacement
// against the actor's current global permissions while holding the same RBAC
// mutation lock used for the write. This prevents a role changed after an HTTP
// preflight from crossing the caller's grant ceiling.
func (s *Store) SaveRoleAuthorized(ctx context.Context, role Role, actorID int64) (Role, error) {
	return s.saveRole(ctx, role, actorID)
}

func (s *Store) saveRole(ctx context.Context, role Role, actorID int64) (Role, error) {
	role.Key = strings.TrimSpace(role.Key)
	role.Name = strings.TrimSpace(role.Name)
	role.Description = strings.TrimSpace(role.Description)
	if role.Key == "" || role.Name == "" {
		return Role{}, errors.New("role key and name are required")
	}
	if len(role.Key) > 100 || len(role.Name) > 256 || len(role.Description) > 4096 {
		return Role{}, errors.New("role fields exceed the allowed length")
	}
	if err := ValidateScopes(role.Permissions); err != nil {
		return Role{}, err
	}
	raw, _ := json.Marshal(role.Permissions)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Role{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, rbacMutationLockKey); err != nil {
		return Role{}, err
	}
	actorPermissions, err := roleMutationActorCeiling(ctx, tx, actorID)
	if err != nil {
		return Role{}, err
	}
	if actorID > 0 && !permissionsWithinCeiling(role.Permissions, actorPermissions) {
		return Role{}, ErrRoleGrantCeiling
	}
	if role.ID == 0 {
		err := tx.QueryRow(ctx, `INSERT INTO roles(role_key,name,description,permissions) VALUES($1,$2,$3,$4) RETURNING id,created_at,updated_at`, role.Key, role.Name, role.Description, raw).Scan(&role.ID, &role.CreatedAt, &role.UpdatedAt)
		if err != nil {
			return Role{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return Role{}, err
		}
		return role, nil
	}
	var current Role
	var currentRaw []byte
	err = tx.QueryRow(ctx, `SELECT role_key,name,description,permissions,system,created_at,updated_at FROM roles WHERE id=$1 FOR UPDATE`, role.ID).Scan(
		&current.Key, &current.Name, &current.Description, &currentRaw, &current.System, &current.CreatedAt, &current.UpdatedAt,
	)
	if err != nil {
		return Role{}, dbNotFound(err)
	}
	if err := json.Unmarshal(currentRaw, &current.Permissions); err != nil {
		return Role{}, err
	}
	if actorID > 0 && !permissionsWithinCeiling(current.Permissions, actorPermissions) {
		return Role{}, ErrRoleGrantCeiling
	}
	if current.System {
		role.Key = current.Key
	}
	if current.Key == "super_admin" && !wildcardOnly(role.Permissions) {
		return Role{}, ErrImmutableRole
	}
	if roleHasWildcard(current.Permissions) && !roleHasWildcard(role.Permissions) {
		blocked, err := wildcardRoleRemovalWouldLockOut(ctx, tx, role.ID)
		if err != nil {
			return Role{}, err
		}
		if blocked {
			return Role{}, ErrLastSuperAdmin
		}
	}
	raw, _ = json.Marshal(role.Permissions)
	err = tx.QueryRow(ctx, `UPDATE roles SET role_key=$2,name=$3,description=$4,permissions=$5,updated_at=now() WHERE id=$1 RETURNING system,created_at,updated_at`, role.ID, role.Key, role.Name, role.Description, raw).Scan(&role.System, &role.CreatedAt, &role.UpdatedAt)
	if err != nil {
		return Role{}, dbNotFound(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Role{}, err
	}
	return role, nil
}

func (s *Store) DeleteRole(ctx context.Context, id int64) error {
	return s.deleteRole(ctx, id, 0)
}

func (s *Store) DeleteRoleAuthorized(ctx context.Context, id, actorID int64) error {
	return s.deleteRole(ctx, id, actorID)
}

func (s *Store) deleteRole(ctx context.Context, id, actorID int64) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, rbacMutationLockKey); err != nil {
		return err
	}
	actorPermissions, err := roleMutationActorCeiling(ctx, tx, actorID)
	if err != nil {
		return err
	}
	var system bool
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT permissions,system FROM roles WHERE id=$1 FOR UPDATE`, id).Scan(&raw, &system); err != nil {
		return dbNotFound(err)
	}
	if system {
		return ErrNotFound
	}
	var permissions []string
	if err := json.Unmarshal(raw, &permissions); err != nil {
		return err
	}
	if actorID > 0 && !permissionsWithinCeiling(permissions, actorPermissions) {
		return ErrRoleGrantCeiling
	}
	if roleHasWildcard(permissions) {
		blocked, err := wildcardRoleRemovalWouldLockOut(ctx, tx, id)
		if err != nil {
			return err
		}
		if blocked {
			return ErrLastSuperAdmin
		}
	}
	result, err := tx.Exec(ctx, `DELETE FROM roles WHERE id=$1 AND system=false`, id)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

func (s *Store) SetUserRoles(ctx context.Context, userID int64, roleIDs []int64) error {
	bindings := make([]RoleBinding, 0, len(roleIDs))
	for _, roleID := range roleIDs {
		bindings = append(bindings, RoleBinding{RoleID: roleID, ScopeMode: "global"})
	}
	return s.setUserRoleBindings(ctx, userID, bindings, true, 0)
}

func (s *Store) SetUserRoleBindings(ctx context.Context, userID int64, bindings []RoleBinding) error {
	return s.setUserRoleBindings(ctx, userID, bindings, false, 0)
}

func (s *Store) SetUserRolesAuthorized(ctx context.Context, userID int64, roleIDs []int64, actorID int64) error {
	bindings := make([]RoleBinding, 0, len(roleIDs))
	for _, roleID := range roleIDs {
		bindings = append(bindings, RoleBinding{RoleID: roleID, ScopeMode: "global"})
	}
	return s.setUserRoleBindings(ctx, userID, bindings, true, actorID)
}

func (s *Store) SetUserRoleBindingsAuthorized(ctx context.Context, userID int64, bindings []RoleBinding, actorID int64) error {
	return s.setUserRoleBindings(ctx, userID, bindings, false, actorID)
}

func (s *Store) setUserRoleBindings(ctx context.Context, userID int64, bindings []RoleBinding, legacy bool, actorID int64) error {
	if len(bindings) > 64 {
		return errors.New("too many role bindings")
	}
	normalized, roleIDs, err := normalizeRoleBindings(bindings)
	if err != nil {
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, rbacMutationLockKey); err != nil {
		return err
	}
	// Hub existence and the eventual scope-clause insert must be atomic with
	// DeleteHub's scope cleanup. Keep the global order RBAC -> inventory.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, integrationInventoryLockKey); err != nil {
		return err
	}
	actorPermissions, err := roleMutationActorCeiling(ctx, tx, actorID)
	if err != nil {
		return err
	}
	var userActive bool
	if err := tx.QueryRow(ctx, `SELECT active FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&userActive); err != nil {
		return dbNotFound(err)
	}
	if legacy {
		var hasRestricted bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM user_roles WHERE user_id=$1 AND scope_mode='restricted')`, userID).Scan(&hasRestricted); err != nil {
			return err
		}
		if hasRestricted {
			return ErrScopeRequired
		}
	}
	var selectedCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM roles WHERE id=ANY($1::bigint[])`, roleIDs).Scan(&selectedCount); err != nil {
		return err
	}
	if selectedCount != len(roleIDs) {
		return ErrNotFound
	}
	if actorID > 0 {
		currentPermissions, err := assignedPermissions(ctx, tx, userID, false)
		if err != nil {
			return err
		}
		if !permissionsWithinCeiling(currentPermissions, actorPermissions) {
			return ErrRoleGrantCeiling
		}
		selectedPermissions, err := rolePermissions(ctx, tx, roleIDs)
		if err != nil {
			return err
		}
		if !permissionsWithinCeiling(selectedPermissions, actorPermissions) {
			return ErrRoleGrantCeiling
		}
	}
	for _, binding := range normalized {
		for _, clause := range binding.Scopes {
			if clause.Type != "hub" {
				continue
			}
			hubID, _ := ParseHubScope(clause.Value)
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hubs WHERE id=$1)`, hubID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return ErrNotFound
			}
		}
	}
	globalRoleIDs := make([]int64, 0, len(normalized))
	for _, binding := range normalized {
		if binding.ScopeMode == "global" {
			globalRoleIDs = append(globalRoleIDs, binding.RoleID)
		}
	}
	var willHaveWildcard bool
	if err := tx.QueryRow(ctx, `SELECT COALESCE(bool_or(permissions ? '*'),false) FROM roles WHERE id=ANY($1::bigint[])`, globalRoleIDs).Scan(&willHaveWildcard); err != nil {
		return err
	}
	var hasWildcard bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM user_roles ur JOIN roles r ON r.id=ur.role_id WHERE ur.user_id=$1 AND ur.scope_mode='global' AND r.permissions ? '*')`, userID).Scan(&hasWildcard); err != nil {
		return err
	}
	if userActive && hasWildcard && !willHaveWildcard {
		var anotherActiveAdmin bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM user_roles ur JOIN roles r ON r.id=ur.role_id JOIN users u ON u.id=ur.user_id WHERE ur.user_id<>$1 AND u.active AND ur.scope_mode='global' AND r.permissions ? '*')`, userID).Scan(&anotherActiveAdmin); err != nil {
			return err
		}
		if !anotherActiveAdmin {
			return ErrLastSuperAdmin
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM user_roles WHERE user_id=$1`, userID); err != nil {
		return err
	}
	for _, binding := range normalized {
		if _, err := tx.Exec(ctx, `INSERT INTO user_roles(user_id,role_id,scope_mode) VALUES($1,$2,$3)`, userID, binding.RoleID, binding.ScopeMode); err != nil {
			return err
		}
		for _, clause := range binding.Scopes {
			if _, err := tx.Exec(ctx, `INSERT INTO user_role_scope_clauses(user_id,role_id,scope_type,scope_value) VALUES($1,$2,$3,$4)`, userID, binding.RoleID, clause.Type, clause.Value); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

func normalizeRoleBindings(bindings []RoleBinding) ([]RoleBinding, []int64, error) {
	normalized := make([]RoleBinding, 0, len(bindings))
	roleIDs := make([]int64, 0, len(bindings))
	seenRoles := map[int64]struct{}{}
	for _, input := range bindings {
		if input.RoleID <= 0 {
			return nil, nil, errors.New("invalid role id")
		}
		if _, exists := seenRoles[input.RoleID]; exists {
			return nil, nil, errors.New("duplicate role binding")
		}
		seenRoles[input.RoleID] = struct{}{}
		mode := strings.TrimSpace(input.ScopeMode)
		if mode != "global" && mode != "restricted" {
			return nil, nil, errors.New("scope_mode must be global or restricted")
		}
		binding := RoleBinding{RoleID: input.RoleID, ScopeMode: mode, Scopes: []ScopeClause{}}
		seenClauses := map[string]struct{}{}
		if mode == "global" && len(input.Scopes) > 0 {
			return nil, nil, errors.New("global role binding cannot contain scope clauses")
		}
		if len(input.Scopes) > 128 {
			return nil, nil, errors.New("too many scope clauses")
		}
		for _, inputClause := range input.Scopes {
			clause := ScopeClause{Type: strings.TrimSpace(inputClause.Type), Value: strings.TrimSpace(inputClause.Value)}
			switch clause.Type {
			case "hub":
				id, ok := ParseHubScope(clause.Value)
				if !ok {
					return nil, nil, errors.New("hub scope must be a positive Hub ID")
				}
				clause.Value = strconv.FormatInt(id, 10)
			case "department":
				if clause.Value == "" || len(clause.Value) > 256 {
					return nil, nil, errors.New("department scope must be 1-256 bytes")
				}
			default:
				return nil, nil, errors.New("scope type must be hub or department")
			}
			key := clause.Type + "\x00" + strings.ToLower(clause.Value)
			if _, exists := seenClauses[key]; exists {
				return nil, nil, errors.New("duplicate scope clause")
			}
			seenClauses[key] = struct{}{}
			binding.Scopes = append(binding.Scopes, clause)
		}
		if mode == "restricted" && len(binding.Scopes) == 0 {
			return nil, nil, errors.New("restricted role binding requires at least one scope clause")
		}
		normalized = append(normalized, binding)
		roleIDs = append(roleIDs, binding.RoleID)
	}
	return normalized, roleIDs, nil
}

const rbacMutationLockKey int64 = 0x6a75706971524241 // "jupiqRBA"

func roleMutationActorCeiling(ctx context.Context, tx pgx.Tx, actorID int64) ([]string, error) {
	if actorID <= 0 {
		return nil, nil
	}
	var active bool
	if err := tx.QueryRow(ctx, `SELECT active FROM users WHERE id=$1`, actorID).Scan(&active); err != nil {
		return nil, dbNotFound(err)
	}
	if !active {
		return nil, ErrRoleGrantCeiling
	}
	permissions, err := assignedPermissions(ctx, tx, actorID, true)
	if err != nil {
		return nil, err
	}
	if !EnsurePermission(permissions, "roles:write") {
		return nil, ErrRoleGrantCeiling
	}
	return permissions, nil
}

func assignedPermissions(ctx context.Context, tx pgx.Tx, userID int64, globalOnly bool) ([]string, error) {
	query := `SELECT r.permissions FROM roles r JOIN user_roles ur ON ur.role_id=r.id WHERE ur.user_id=$1`
	if globalOnly {
		query += ` AND ur.scope_mode='global'`
	}
	rows, err := tx.Query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	permissions := []string{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var current []string
		if err := json.Unmarshal(raw, &current); err != nil {
			return nil, err
		}
		for _, permission := range current {
			if !contains(permissions, permission) {
				permissions = append(permissions, permission)
			}
		}
	}
	return permissions, rows.Err()
}

func rolePermissions(ctx context.Context, tx pgx.Tx, roleIDs []int64) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT permissions FROM roles WHERE id=ANY($1::bigint[])`, roleIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	permissions := []string{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var current []string
		if err := json.Unmarshal(raw, &current); err != nil {
			return nil, err
		}
		for _, permission := range current {
			if !contains(permissions, permission) {
				permissions = append(permissions, permission)
			}
		}
	}
	return permissions, rows.Err()
}

func permissionsWithinCeiling(candidate, ceiling []string) bool {
	if len(candidate) == 0 {
		return true
	}
	return ScopesWithinPermissions(candidate, ceiling)
}

func roleHasWildcard(permissions []string) bool {
	for _, permission := range permissions {
		if permission == "*" {
			return true
		}
	}
	return false
}

func wildcardOnly(permissions []string) bool {
	return len(permissions) == 1 && permissions[0] == "*"
}

type roleMutationTx interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func wildcardRoleRemovalWouldLockOut(ctx context.Context, tx roleMutationTx, roleID int64) (bool, error) {
	var assignedToActiveUser, anotherWildcardRoleAssigned bool
	err := tx.QueryRow(ctx, `SELECT
		EXISTS(SELECT 1 FROM user_roles ur JOIN users u ON u.id=ur.user_id WHERE ur.role_id=$1 AND ur.scope_mode='global' AND u.active),
		EXISTS(SELECT 1 FROM user_roles ur JOIN users u ON u.id=ur.user_id JOIN roles r ON r.id=ur.role_id WHERE ur.role_id<>$1 AND ur.scope_mode='global' AND u.active AND r.permissions ? '*')`, roleID).Scan(&assignedToActiveUser, &anotherWildcardRoleAssigned)
	return assignedToActiveUser && !anotherWildcardRoleAssigned, err
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
