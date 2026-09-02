package api

import (
	"context"
	"errors"
	"time"

	"github.com/hkjang/jupiq/internal/store"
)

type apiKeyPolicy struct {
	RotationDays    int      `json:"key_rotation_days"`
	MaxLifetimeDays int      `json:"key_max_lifetime_days"`
	Permissions     []string `json:"key_permissions"`
}

func (s *Server) loadAPIKeyPolicy(ctx context.Context) apiKeyPolicy {
	policy := apiKeyPolicy{RotationDays: 90, MaxLifetimeDays: 365}
	var saved apiKeyPolicy
	if s.Store.GetSetting(ctx, "security", &saved) == nil {
		if saved.RotationDays > 0 {
			policy.RotationDays = saved.RotationDays
		}
		if saved.MaxLifetimeDays > 0 {
			policy.MaxLifetimeDays = saved.MaxLifetimeDays
		}
		policy.Permissions = saved.Permissions
	}
	if policy.RotationDays > policy.MaxLifetimeDays {
		policy.RotationDays = policy.MaxLifetimeDays
	}
	return policy
}

func applyAPIKeyPolicy(policy apiKeyPolicy, scopes []string, expiresAt **time.Time) error {
	if len(policy.Permissions) > 0 && !store.ScopesWithinPermissions(scopes, policy.Permissions) {
		return errors.New("관리자가 허용한 API 키 권한 범위를 초과했습니다")
	}
	now := time.Now().UTC()
	if *expiresAt == nil {
		expires := now.Add(time.Duration(policy.RotationDays) * 24 * time.Hour)
		*expiresAt = &expires
	}
	if !(*expiresAt).After(now) {
		return errors.New("API 키 만료일은 현재보다 이후여야 합니다")
	}
	maximum := now.Add(time.Duration(policy.MaxLifetimeDays) * 24 * time.Hour)
	if (*expiresAt).After(maximum) {
		return errors.New("API 키 만료일이 관리자의 최대 유효기간을 초과했습니다")
	}
	return nil
}
