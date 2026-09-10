package store

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Hub and integration failures are recorded as Korean text, so the byte cap has
// to stop on a rune boundary. A mid-rune cut reaches PostgreSQL as an invalid
// UTF-8 byte sequence and the write that marks the Hub degraded fails instead.
func TestTruncateNeverSplitsAMultiByteRune(t *testing.T) {
	message := strings.Repeat("허용되지 않는 대상입니다 ", 200)
	for max := 990; max <= 1010; max++ {
		got := truncate(message, max)
		if !utf8.ValidString(got) {
			t.Fatalf("max=%d produced invalid UTF-8: %q", max, got)
		}
		if len(got) > max {
			t.Fatalf("max=%d exceeded the byte cap: %d bytes", max, len(got))
		}
		if !strings.HasPrefix(message, got) {
			t.Fatalf("max=%d changed the retained prefix", max)
		}
		if max-len(got) > 3 {
			t.Fatalf("max=%d dropped %d bytes, more than one rune", max, max-len(got))
		}
	}
}

func TestTruncateKeepsShortMessagesAndScrubsInvalidInput(t *testing.T) {
	short := "원격 API가 HTTP 502를 반환했습니다"
	if got := truncate(short, 1000); got != short {
		t.Fatalf("message within the cap was modified: %q", got)
	}
	// A lone continuation byte can only reach PostgreSQL as an error, so it is
	// replaced rather than passed through.
	if got := truncate("ok\xffdone", 1000); !utf8.ValidString(got) {
		t.Fatalf("invalid input was not scrubbed: %q", got)
	}
	if got := truncate("", 1000); got != "" {
		t.Fatalf("empty message became %q", got)
	}
}
