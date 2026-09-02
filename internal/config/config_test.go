package config

import (
	"encoding/base64"
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
