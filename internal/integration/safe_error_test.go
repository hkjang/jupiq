package integration

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// Preflight failures are Korean, so the 500 byte cap must stop on a rune
// boundary. A mid-rune cut leaves invalid UTF-8 that the JSON encoder replaces
// with U+FFFD, so the administrator sees a broken character in the drawer.
func TestSafeConnectionErrorNeverSplitsAMultiByteRune(t *testing.T) {
	for pad := 0; pad < 6; pad++ {
		message := strings.Repeat("x", 490+pad) + strings.Repeat("허용되지 않는 대상입니다", 10)
		got := safeConnectionError(errors.New(message))
		if !utf8.ValidString(got) {
			t.Fatalf("pad=%d produced invalid UTF-8: %q", pad, got)
		}
		if len(got) > 500 {
			t.Fatalf("pad=%d exceeded the byte cap: %d bytes", pad, len(got))
		}
		if !strings.HasPrefix(message, got) {
			t.Fatalf("pad=%d changed the retained prefix", pad)
		}
		encoded, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("pad=%d could not be encoded: %v", pad, err)
		}
		if strings.Contains(string(encoded), `�`) {
			t.Fatalf("pad=%d reached the drawer with a replacement character: %s", pad, encoded)
		}
	}
}

func TestSafeConnectionErrorMasksBearerAndKeepsShortMessages(t *testing.T) {
	got := safeConnectionError(errors.New(`Get "https://hub.internal": Bearer secret-token 거부됨`))
	if strings.Contains(got, "Bearer") {
		t.Fatalf("Bearer was not masked: %q", got)
	}
	if !strings.Contains(got, "인증") || !strings.HasSuffix(got, "거부됨") {
		t.Fatalf("message within the cap was altered: %q", got)
	}
}
