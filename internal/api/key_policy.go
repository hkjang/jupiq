package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hkjang/jupiq/internal/store"
)

type apiKeyPolicy struct {
	RotationDays    int      `json:"key_rotation_days"`
	MaxLifetimeDays int      `json:"key_max_lifetime_days"`
	Permissions     []string `json:"key_permissions"`
}

func (s *Server) loadAPIKeyPolicy(ctx context.Context) (apiKeyPolicy, error) {
	defaults := apiKeyPolicy{RotationDays: 90, MaxLifetimeDays: 365}
	var saved apiKeyPolicy
	err := s.Store.GetSetting(ctx, "security", &saved)
	if errors.Is(err, store.ErrNotFound) {
		return defaults, nil
	}
	if err != nil {
		return apiKeyPolicy{}, fmt.Errorf("API 키 정책을 읽을 수 없습니다: %w", err)
	}
	if err := validateAPIKeyPolicy(saved); err != nil {
		return apiKeyPolicy{}, err
	}
	return saved, nil
}

func validateAPIKeyPolicy(policy apiKeyPolicy) error {
	if policy.RotationDays < 1 || policy.RotationDays > 3650 || policy.MaxLifetimeDays < 1 || policy.MaxLifetimeDays > 3650 {
		return errors.New("API 키 정책 일수가 허용 범위를 벗어났습니다")
	}
	if policy.RotationDays > policy.MaxLifetimeDays {
		return errors.New("API 키 회전 주기는 최대 유효기간보다 클 수 없습니다")
	}
	if len(policy.Permissions) > 0 {
		if err := store.ValidateScopes(policy.Permissions); err != nil {
			return fmt.Errorf("API 키 허용 권한이 잘못되었습니다: %w", err)
		}
	}
	return nil
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
