package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestParseEncryptionKey(t *testing.T) {
	want := []byte("01234567890123456789012345678901")
	for _, input := range []string{string(want), base64.StdEncoding.EncodeToString(want)} {
		got, err := ParseEncryptionKey(input)
		if err != nil || string(got) != string(want) {
			t.Fatalf("ParseEncryptionKey(%q) = %x, %v", input, got, err)
		}
	}
	if _, err := ParseEncryptionKey("short"); err == nil {
		t.Fatal("expected invalid key error")
	}
}

const validKey = "01234567890123456789012345678901"

// setEnv writes all four runtime variables so a test never inherits a value
// from the shell that started `go test`.
func setEnv(t *testing.T, dsn, admin, password, key string) {
	t.Helper()
	t.Setenv("POSTGRES_DSN", dsn)
	t.Setenv("BOOTSTRAP_ADMIN", admin)
	t.Setenv("BOOTSTRAP_ADMIN_PASSWORD", password)
	t.Setenv("ENCRYPTION_KEY", key)
}

func TestLoadReadsAndTrimsEnvironment(t *testing.T) {
	setEnv(t, "  postgres://localhost/jupiq  ", "  admin  ", " keep me secret ", "  "+validKey+"  ")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if cfg.PostgresDSN != "postgres://localhost/jupiq" || cfg.BootstrapAdmin != "admin" {
		t.Fatalf("surrounding whitespace not trimmed: %q, %q", cfg.PostgresDSN, cfg.BootstrapAdmin)
	}
	if cfg.BootstrapPassword != " keep me secret " {
		t.Fatalf("password was altered: %q", cfg.BootstrapPassword)
	}
	if string(cfg.EncryptionKey) != validKey {
		t.Fatalf("EncryptionKey = %q", cfg.EncryptionKey)
	}
}

func TestLoadReportsEveryMissingVariableAtOnce(t *testing.T) {
	setEnv(t, "", "", "", "")
	_, err := Load()
	if err == nil {
		t.Fatal("expected an error when nothing is configured")
	}
	for _, name := range []string{"POSTGRES_DSN", "BOOTSTRAP_ADMIN", "BOOTSTRAP_ADMIN_PASSWORD", "ENCRYPTION_KEY"} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("Load() = %q, missing %s", err, name)
		}
	}
}

func TestLoadRejectsShortBootstrapPassword(t *testing.T) {
	setEnv(t, "postgres://localhost/jupiq", "admin", strings.Repeat("a", 11), validKey)
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "12 characters") {
		t.Fatalf("Load() = %v, want a minimum length error", err)
	}
	setEnv(t, "postgres://localhost/jupiq", "admin", strings.Repeat("a", 12), validKey)
	if _, err := Load(); err != nil {
		t.Fatalf("Load() rejected a 12 character password: %v", err)
	}
}

// A present-but-unusable key and a too-short password must both surface on the
// first boot, or the operator restarts once per problem to find them all.
func TestLoadReportsUnusableValuesTogether(t *testing.T) {
	setEnv(t, "", "admin", "short", "not-a-32-byte-key")
	_, err := Load()
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"POSTGRES_DSN", "ENCRYPTION_KEY must be exactly 32 bytes", "12 characters"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Load() = %q, missing %q", err, want)
		}
	}
}

func TestLoadReturnsNoConfigWhenInvalid(t *testing.T) {
	setEnv(t, "postgres://localhost/jupiq", "admin", "short", validKey)
	cfg, err := Load()
	if err == nil {
		t.Fatal("expected an error")
	}
	if cfg.PostgresDSN != "" || cfg.BootstrapAdmin != "" || cfg.BootstrapPassword != "" || cfg.EncryptionKey != nil {
		t.Fatalf("a rejected configuration leaked values: %+v", cfg)
	}
}
