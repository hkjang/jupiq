package secure

import "testing"

func TestCipherRoundTripAndContext(t *testing.T) {
	c, err := NewCipher([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
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
