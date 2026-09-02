package store

import (
	"encoding/json"
	"time"
)

type User struct {
	ID              int64      `json:"id"`
	Username        string     `json:"username"`
	DisplayName     string     `json:"display_name"`
	Email           string     `json:"email"`
	Department      string     `json:"department"`
	AuthSource      string     `json:"auth_source"`
	ExternalSubject *string    `json:"-"`
	Active          bool       `json:"active"`
	LastLoginAt     *time.Time `json:"last_login_at"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	Roles           []string   `json:"roles"`
	Permissions     []string   `json:"permissions"`
}

type UserCredential struct {
	User
	PasswordHash *string
}

type Role struct {
	ID          int64     `json:"id"`
	Key         string    `json:"key"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Permissions []string  `json:"permissions"`
	System      bool      `json:"system"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Hub struct {
	ID                     int64           `json:"id"`
	Name                   string          `json:"name"`
	BaseURL                string          `json:"base_url"`
	Network                string          `json:"network"`
	Enabled                bool            `json:"enabled"`
	VerifyTLS              bool            `json:"verify_tls"`
	CollectIntervalSeconds int             `json:"collect_interval_seconds"`
	TokenConfigured        bool            `json:"token_configured"`
	Status                 string          `json:"status"`
	Version                string          `json:"version"`
	LastSeenAt             *time.Time      `json:"last_seen_at"`
	LastError              string          `json:"last_error"`
	Snapshot               json.RawMessage `json:"snapshot"`
	CreatedAt              time.Time       `json:"created_at"`
	UpdatedAt              time.Time       `json:"updated_at"`
}

type HubWrite struct {
	Name                   string `json:"name"`
	BaseURL                string `json:"base_url"`
	Network                string `json:"network"`
	Enabled                *bool  `json:"enabled,omitempty"`
	VerifyTLS              *bool  `json:"verify_tls,omitempty"`
	CollectIntervalSeconds int    `json:"collect_interval_seconds"`
	APIToken               string `json:"api_token,omitempty"`
}

type ManagedUser struct {
	ID             int64           `json:"id"`
	HubID          int64           `json:"hub_id"`
	HubName        string          `json:"hub_name"`
	Username       string          `json:"username"`
	DisplayName    string          `json:"display_name"`
	Department     string          `json:"department"`
	Admin          bool            `json:"admin"`
	Active         bool            `json:"active"`
	LastActivityAt *time.Time      `json:"last_activity_at"`
	Raw            json.RawMessage `json:"raw,omitempty"`
	SyncedAt       time.Time       `json:"synced_at"`
}

type Server struct {
	ID             int64           `json:"id"`
	HubID          int64           `json:"hub_id"`
	HubName        string          `json:"hub_name"`
	Username       string          `json:"username"`
	ServerName     string          `json:"server_name"`
	Status         string          `json:"status"`
	StartedAt      *time.Time      `json:"started_at"`
	LastActivityAt *time.Time      `json:"last_activity_at"`
	URL            string          `json:"url"`
	NodeName       string          `json:"node_name"`
	PodName        string          `json:"pod_name"`
	Image          string          `json:"image"`
	CPUCores       *float64        `json:"cpu_cores"`
	MemoryBytes    *int64          `json:"memory_bytes"`
	GPUCount       *int            `json:"gpu_count"`
	Raw            json.RawMessage `json:"raw,omitempty"`
	SyncedAt       time.Time       `json:"synced_at"`
}

type Resource struct {
	ID          int64           `json:"id"`
	Kind        string          `json:"kind"`
	Name        string          `json:"name"`
	Status      string          `json:"status"`
	OwnerUserID *int64          `json:"owner_user_id,omitempty"`
	Data        json.RawMessage `json:"data"`
	CreatedBy   *int64          `json:"created_by,omitempty"`
	UpdatedBy   *int64          `json:"updated_by,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

type ResourceWrite struct {
	Name        string          `json:"name"`
	Status      string          `json:"status"`
	OwnerUserID *int64          `json:"owner_user_id,omitempty"`
	Data        json.RawMessage `json:"data"`
}

type APIKey struct {
	ID         int64      `json:"id"`
	UserID     int64      `json:"user_id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scopes     []string   `json:"scopes"`
	Status     string     `json:"status"`
	LastUsedAt *time.Time `json:"last_used_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	CreatedAt  time.Time  `json:"created_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

type AuditEvent struct {
	ActorUserID   *int64
	ActorUsername string
	Action        string
	ResourceType  string
	ResourceID    string
	Before        any
	After         any
	IPAddress     string
	UserAgent     string
	Result        string
	Reason        string
	RequestID     string
}

type Page struct {
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
	Total    int `json:"total"`
}
