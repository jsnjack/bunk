package termcorpus

import (
	"bytes"
	"testing"
	"unicode/utf8"
)

func TestGenerate(t *testing.T) {
	for _, kind := range Kinds {
		t.Run(kind, func(t *testing.T) {
			a, b := Generate(kind, 64<<10), Generate(kind, 64<<10)
			if len(a) < 64<<10 {
				t.Fatalf("got %d bytes, want at least %d", len(a), 64<<10)
			}
			if !bytes.Equal(a, b) {
				t.Fatal("two calls gave different streams")
			}
			if !utf8.Valid(a) {
				t.Fatal("stream is not valid UTF-8")
			}
			if bytes.Count(a, []byte("\x1b[")) == 0 && kind != "cjk" && kind != "emoji" {
				t.Fatal("stream holds no control sequences")
			}
		})
	}
}
