package secure

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

type Cipher struct {
	aead cipher.AEAD
	key  []byte
}

func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != 32 {
		return nil, errors.New("AES-256 key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead, key: append([]byte(nil), key...)}, nil
}

func (c *Cipher) Encrypt(plaintext []byte, context string) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return c.aead.Seal(nonce, nonce, plaintext, []byte(context)), nil
}

func (c *Cipher) Decrypt(blob []byte, context string) ([]byte, error) {
	if len(blob) < c.aead.NonceSize() {
		return nil, errors.New("encrypted value is truncated")
	}
	nonce, ciphertext := blob[:c.aead.NonceSize()], blob[c.aead.NonceSize():]
	plaintext, err := c.aead.Open(nil, nonce, ciphertext, []byte(context))
	if err != nil {
		return nil, fmt.Errorf("decrypt %s: %w", context, err)
	}
	return plaintext, nil
}

func (c *Cipher) EncryptString(value, context string) (string, error) {
	b, err := c.Encrypt([]byte(value), context)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (c *Cipher) DecryptString(value, context string) (string, error) {
	b, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	p, err := c.Decrypt(b, context)
	return string(p), err
}

func (c *Cipher) Derive(label string) []byte {
	h := hmac.New(sha256.New, c.key)
	h.Write([]byte("jupiq:" + label))
	return h.Sum(nil)
}

func RandomToken(bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
