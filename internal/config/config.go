package config

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

const ListenAddress = ":8080"

type Config struct {
	PostgresDSN       string
	BootstrapAdmin    string
	BootstrapPassword string
	EncryptionKey     []byte
}

// Load reads the four runtime environment variables. Every problem it finds is
// reported in one error so an operator fixes the whole set in a single restart
// instead of meeting the next one on the following boot. The bootstrap password
// is deliberately not trimmed: leading or trailing spaces belong to the secret.
func Load() (Config, error) {
	c := Config{
		PostgresDSN:       strings.TrimSpace(os.Getenv("POSTGRES_DSN")),
		BootstrapAdmin:    strings.TrimSpace(os.Getenv("BOOTSTRAP_ADMIN")),
		BootstrapPassword: os.Getenv("BOOTSTRAP_ADMIN_PASSWORD"),
	}
	keyText := strings.TrimSpace(os.Getenv("ENCRYPTION_KEY"))
	var missing []string
	if c.PostgresDSN == "" {
		missing = append(missing, "POSTGRES_DSN")
	}
	if c.BootstrapAdmin == "" {
		missing = append(missing, "BOOTSTRAP_ADMIN")
	}
	if c.BootstrapPassword == "" {
		missing = append(missing, "BOOTSTRAP_ADMIN_PASSWORD")
	}
	if keyText == "" {
		missing = append(missing, "ENCRYPTION_KEY")
	}
	var problems []string
	if len(missing) > 0 {
		problems = append(problems, fmt.Sprintf("required environment variables are missing: %s", strings.Join(missing, ", ")))
	}
	// A value that is present but unusable is worth reporting alongside the
	// missing ones; an absent value has already been named above.
	if keyText != "" {
		key, err := ParseEncryptionKey(keyText)
		if err != nil {
			problems = append(problems, err.Error())
		} else {
			c.EncryptionKey = key
		}
	}
	if c.BootstrapPassword != "" && len(c.BootstrapPassword) < 12 {
		problems = append(problems, "BOOTSTRAP_ADMIN_PASSWORD must contain at least 12 characters")
	}
	if len(problems) > 0 {
		return Config{}, errors.New(strings.Join(problems, "; "))
	}
	return c, nil
}

// ParseEncryptionKey accepts a 32-byte key encoded as base64, hex, or raw text.
func ParseEncryptionKey(value string) ([]byte, error) {
	for _, decode := range []func(string) ([]byte, error){base64.StdEncoding.DecodeString, base64.RawStdEncoding.DecodeString, hex.DecodeString} {
		if decoded, err := decode(value); err == nil && len(decoded) == 32 {
			return decoded, nil
		}
	}
	if len([]byte(value)) == 32 {
		return []byte(value), nil
	}
	return nil, errors.New("ENCRYPTION_KEY must be exactly 32 bytes (raw, base64, or hex encoded)")
}
