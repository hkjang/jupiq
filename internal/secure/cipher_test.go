package secure

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func testCipher(t *testing.T) *Cipher {
	t.Helper()
	c, err := NewCipher([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewCipherRequiresA32ByteKey(t *testing.T) {
	for _, size := range []int{0, 16, 31, 33, 64} {
		if _, err := NewCipher(bytes.Repeat([]byte("k"), size)); err == nil {
			t.Fatalf("NewCipher(%d bytes) accepted a non AES-256 key", size)
		}
	}
}

func TestCipherRoundTripAndContext(t *testing.T) {
	c := testCipher(t)
	enc, err := c.Encrypt([]byte("secret"), "hub:1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Decrypt(enc, "hub:1")
	if err != nil || string(got) != "secret" {
		t.Fatalf("round trip = %q, %v", got, err)
	}
	if _, err := c.Decrypt(enc, "hub:2"); err == nil {
		t.Fatal("expected context authentication failure")
	}
}

func TestEncryptStringRoundTripAndContext(t *testing.T) {
	c := testCipher(t)
	const secret = "hub-token-한글"
	first, err := c.EncryptString(secret, "oidc-state")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(first, "=") {
		t.Fatalf("EncryptString = %q, want unpadded base64", first)
	}
	if _, err := base64.RawURLEncoding.DecodeString(first); err != nil {
		t.Fatalf("EncryptString = %q, not raw URL base64: %v", first, err)
	}
	got, err := c.DecryptString(first, "oidc-state")
	if err != nil || got != secret {
		t.Fatalf("DecryptString = %q, %v, want %q", got, err, secret)
	}
	// A fresh nonce per call keeps the same plaintext from producing a
	// recognisable ciphertext across cookies and stored credentials.
	second, err := c.EncryptString(secret, "oidc-state")
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("EncryptString reused a nonce for the same plaintext")
	}
	if _, err := c.DecryptString(first, "hub:1"); err == nil {
		t.Fatal("expected context authentication failure")
	}
}

func TestDecryptStringRejectsMalformedValues(t *testing.T) {
	c := testCipher(t)
	enc, err := c.EncryptString("secret", "oidc-state")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		t.Fatal(err)
	}
	tampered := append([]byte(nil), raw...)
	tampered[len(tampered)-1] ^= 0xFF
	cases := map[string]string{
		"empty":                  "",
		"not base64":             "not*base64",
		"standard padding":       base64.StdEncoding.EncodeToString(raw),
		"shorter than the nonce": base64.RawURLEncoding.EncodeToString(raw[:c.aead.NonceSize()-1]),
		"tampered ciphertext":    base64.RawURLEncoding.EncodeToString(tampered),
		"truncated ciphertext":   base64.RawURLEncoding.EncodeToString(raw[:len(raw)-1]),
	}
	for name, value := range cases {
		got, err := c.DecryptString(value, "oidc-state")
		if err == nil {
			t.Fatalf("DecryptString(%s) accepted the value", name)
		}
		if got != "" {
			t.Fatalf("DecryptString(%s) = %q on error, want empty", name, got)
		}
	}
}

func TestDeriveIsDeterministicAndSeparatedByLabel(t *testing.T) {
	c := testCipher(t)
	key := c.Derive("jwt-signing-v1")
	if len(key) != 32 {
		t.Fatalf("Derive returned %d bytes, want 32", len(key))
	}
	if !bytes.Equal(key, c.Derive("jwt-signing-v1")) {
		t.Fatal("Derive is not deterministic; signed tokens would stop verifying")
	}
	if bytes.Equal(key, c.Derive("jwt-signing-v2")) {
		t.Fatal("Derive returned the same key for a different label")
	}
	if bytes.Equal(key, c.Derive("")) {
		t.Fatal("Derive returned the signing key for an empty label")
	}
	// A different encryption key must yield a different signing key, otherwise
	// rotating ENCRYPTION_KEY would leave old tokens valid.
	other, err := NewCipher([]byte("abcdefghijklmnopqrstuvwxyz012345"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(key, other.Derive("jwt-signing-v1")) {
		t.Fatal("Derive ignored the cipher key")
	}
}

func TestRandomTokenSizeAndUniqueness(t *testing.T) {
	seen := map[string]bool{}
	// Only the sizes the callers actually use are checked for uniqueness: a
	// one-byte token has 256 values, so repeats there are expected rather than
	// a defect.
	for _, size := range []int{1, 12, 24, 32, 48} {
		for i := 0; i < 16; i++ {
			token, err := RandomToken(size)
			if err != nil {
				t.Fatalf("RandomToken(%d) failed: %v", size, err)
			}
			decoded, err := base64.RawURLEncoding.DecodeString(token)
			if err != nil {
				t.Fatalf("RandomToken(%d) = %q, not raw URL base64: %v", size, token, err)
			}
			if len(decoded) != size {
				t.Fatalf("RandomToken(%d) decoded to %d bytes", size, len(decoded))
			}
			if size >= 12 {
				if seen[token] {
					t.Fatalf("RandomToken(%d) repeated %q", size, token)
				}
				seen[token] = true
			}
		}
	}
	for _, size := range []int{0, -1} {
		if token, err := RandomToken(size); err == nil {
			t.Fatalf("RandomToken(%d) = %q, want an error instead of an empty secret", size, token)
		}
	}
}

func TestHashTokenIsStableAndFullLength(t *testing.T) {
	sum := HashToken("jupiq-api-key")
	if len(sum) != 32 {
		t.Fatalf("HashToken returned %d bytes, want 32", len(sum))
	}
	if !bytes.Equal(sum, HashToken("jupiq-api-key")) {
		t.Fatal("HashToken is not deterministic; stored key hashes would stop matching")
	}
	if bytes.Equal(sum, HashToken("jupiq-api-keY")) {
		t.Fatal("HashToken collided on a one-character change")
	}
}
