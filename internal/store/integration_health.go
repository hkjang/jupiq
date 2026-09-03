package store

import (
	"context"
	"time"
)

type IntegrationHealth struct {
	Type                string     `json:"type"`
	Status              string     `json:"status"`
	Version             string     `json:"version,omitempty"`
	LatencyMS           *int64     `json:"latency_ms,omitempty"`
	LastAttemptAt       *time.Time `json:"last_attempt_at,omitempty"`
	LastSuccessAt       *time.Time `json:"last_success_at,omitempty"`
	LastError           string     `json:"last_error,omitempty"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
}

func (s *Store) RecordIntegrationHealth(ctx context.Context, integrationType string, success bool, latencyMS int64, version, errorMessage string) error {
	status := "failure"
	if success {
		status = "success"
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO integration_health(integration_type,status,version,latency_ms,last_attempt_at,last_success_at,last_error,consecutive_failures) VALUES($1,$2,$3,$4,now(),CASE WHEN $5 THEN now() END,$6,CASE WHEN $5 THEN 0 ELSE 1 END) ON CONFLICT(integration_type) DO UPDATE SET status=EXCLUDED.status,version=EXCLUDED.version,latency_ms=EXCLUDED.latency_ms,last_attempt_at=now(),last_success_at=CASE WHEN $5 THEN now() ELSE integration_health.last_success_at END,last_error=EXCLUDED.last_error,consecutive_failures=CASE WHEN $5 THEN 0 ELSE integration_health.consecutive_failures+1 END,updated_at=now()`, integrationType, status, version, latencyMS, success, truncate(errorMessage, 1000))
	return err
}

func (s *Store) ListIntegrationHealth(ctx context.Context) (map[string]IntegrationHealth, error) {
	rows, err := s.Pool.Query(ctx, `SELECT integration_type,status,version,latency_ms,last_attempt_at,last_success_at,last_error,consecutive_failures FROM integration_health ORDER BY integration_type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]IntegrationHealth{}
	for rows.Next() {
		var item IntegrationHealth
		if err := rows.Scan(&item.Type, &item.Status, &item.Version, &item.LatencyMS, &item.LastAttemptAt, &item.LastSuccessAt, &item.LastError, &item.ConsecutiveFailures); err != nil {
			return nil, err
		}
		result[item.Type] = item
	}
	return result, rows.Err()
}
