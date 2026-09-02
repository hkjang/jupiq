package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/hkjang/jupiq/internal/secure"
	"github.com/jackc/pgx/v5"
)

// RotateAPIKey creates the replacement and revokes the old key in one
// transaction, so a database failure can never leave two active copies.
func (s *Store) RotateAPIKey(ctx context.Context, userID, oldID int64, newExpiresAt *time.Time) (APIKey, string, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return APIKey{}, "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var name string
	var scopesRaw []byte
	var oldExpiresAt *time.Time
	err = tx.QueryRow(ctx, `SELECT name,scopes,expires_at FROM api_keys WHERE id=$1 AND user_id=$2 AND status='active' FOR UPDATE`, oldID, userID).Scan(&name, &scopesRaw, &oldExpiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return APIKey{}, "", ErrNotFound
		}
		return APIKey{}, "", err
	}
	random, err := secure.RandomToken(32)
	if err != nil {
		return APIKey{}, "", err
	}
	plain := "jqk_" + random
	prefix := plain
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}
	var key APIKey
	err = tx.QueryRow(ctx, `INSERT INTO api_keys(user_id,name,prefix,secret_hash,scopes,expires_at,rotated_from_id) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id,status,created_at`, userID, name, prefix, secure.HashToken(plain), scopesRaw, newExpiresAt, oldID).Scan(&key.ID, &key.Status, &key.CreatedAt)
	if err != nil {
		return APIKey{}, "", err
	}
	result, err := tx.Exec(ctx, `UPDATE api_keys SET status='rotated',revoked_at=now() WHERE id=$1 AND user_id=$2 AND status='active'`, oldID, userID)
	if err != nil {
		return APIKey{}, "", err
	}
	if result.RowsAffected() != 1 {
		return APIKey{}, "", ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return APIKey{}, "", err
	}
	var scopes []string
	_ = json.Unmarshal(scopesRaw, &scopes)
	key.UserID, key.Name, key.Prefix, key.Scopes, key.ExpiresAt = userID, name, prefix, scopes, newExpiresAt
	return key, plain, nil
}
