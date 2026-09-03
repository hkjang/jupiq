package api

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http/httptest"
	"testing"
)

type chunkReader struct {
	chunks [][]byte
}

func (r *chunkReader) Read(buffer []byte) (int, error) {
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	chunk := r.chunks[0]
	r.chunks = r.chunks[1:]
	return copy(buffer, chunk), nil
}

type flushRecorder struct {
	*httptest.ResponseRecorder
	flushes int
}

func (r *flushRecorder) Flush() {
	r.flushes++
	r.ResponseRecorder.Flush()
}

func TestCopyFlushedStreamFlushesEveryChunk(t *testing.T) {
	recorder := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	reader := &chunkReader{chunks: [][]byte{[]byte("data: one\n\n"), []byte("data: two\n\n")}}
	written, err := copyFlushedStream(recorder, recorder, reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if written != int64(len("data: one\n\ndata: two\n\n")) || recorder.Body.String() != "data: one\n\ndata: two\n\n" {
		t.Fatalf("unexpected stream output: written=%d body=%q", written, recorder.Body.String())
	}
	if recorder.flushes != 2 {
		t.Fatalf("flush count=%d, want one flush per upstream chunk", recorder.flushes)
	}
}

func TestCopyFlushedStreamEnforcesLimit(t *testing.T) {
	recorder := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	reader := &chunkReader{chunks: [][]byte{[]byte("abcdef")}}
	written, err := copyFlushedStream(recorder, recorder, reader, 3)
	if err == nil || written != 3 || recorder.Body.String() != "abc" {
		t.Fatalf("limit result: written=%d body=%q err=%v", written, recorder.Body.String(), err)
	}
	if errors.Is(err, io.EOF) {
		t.Fatalf("limit exhaustion was reported as EOF: %v", err)
	}
}

func TestValidateAIChatInputEnforcesConfiguredModelAndPayload(t *testing.T) {
	cfg := aiConfig{Model: "approved-model", MaxTokens: 4096}
	valid := aiChatInput{Messages: json.RawMessage(`[{"role":"user","content":"hello"}]`)}
	if code, err := validateAIChatInput(&valid, cfg); err != nil {
		t.Fatalf("valid input rejected (%s): %v", code, err)
	}
	if valid.Model != cfg.Model || valid.MaxTokens != cfg.MaxTokens {
		t.Fatalf("defaults not applied: %#v", valid)
	}
	for name, input := range map[string]aiChatInput{
		"other model":      {Model: "unapproved-model", Messages: valid.Messages},
		"empty messages":   {Messages: json.RawMessage(`[]`)},
		"object messages":  {Messages: json.RawMessage(`{"role":"user"}`)},
		"temperature high": {Messages: valid.Messages, Temperature: floatPointer(2.1)},
		"temperature nan":  {Messages: valid.Messages, Temperature: floatPointer(math.NaN())},
	} {
		if _, err := validateAIChatInput(&input, cfg); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func floatPointer(value float64) *float64 { return &value }
