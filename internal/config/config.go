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

func Load() (Config, error) {
	c := Config{
		PostgresDSN:       strings.TrimSpace(os.Getenv("POSTGRES_DSN")),
		BootstrapAdmin:    strings.TrimSpace(os.Getenv("BOOTSTRAP_ADMIN")),
		BootstrapPassword: os.Getenv("BOOTSTRAP_ADMIN_PASSWORD"),
	}
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
	keyText := strings.TrimSpace(os.Getenv("ENCRYPTION_KEY"))
	if keyText == "" {
		missing = append(missing, "ENCRYPTION_KEY")
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("required environment variables are missing: %s", strings.Join(missing, ", "))
	}
	key, err := ParseEncryptionKey(keyText)
	if err != nil {
		return Config{}, err
	}
	c.EncryptionKey = key
	if len(c.BootstrapPassword) < 12 {
		return Config{}, errors.New("BOOTSTRAP_ADMIN_PASSWORD must contain at least 12 characters")
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
