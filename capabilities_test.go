package main

import (
	"os"
	"strings"
	"testing"
)

func TestCapabilityStreamOrdering(t *testing.T) {
	for _, screen := range []string{"", "\x1b[?1049h"} {
		t.Run("screen "+screen, func(t *testing.T) {
			p := regressionPane(40, 5)
			p.FgProcess = "ssh"
			f, err := os.CreateTemp(t.TempDir(), "replies")
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := f.Close(); err != nil {
					t.Error(err)
				}
			}()
			p.PTY = f
			input := screen + "\x1bP+q544e;436f;524742\x1b\\" +
				"\x1b[?1h\x1bP+q6b75\x1b\\\x1b[?1l\x1bP+q6b75\x1b\\" +
				"\x1b=\x1bP+q6b656e74\x1b\\\x1b>\x1bP+q6b656e74\x1b\\" +
				"\x1b[>1u\x1bP+q6b6273\x1b\\\x1b[<u\x1bP+q6b6273\x1b\\END"
			for _, b := range []byte(input) {
				p.Feed([]byte{b})
			}
			want := "\x1bP1+r544e=787465726d2d323536636f6c6f72\x1b\\" +
				"\x1bP1+r436f=323536\x1b\\\x1bP1+r524742=38\x1b\\" +
				"\x1bP1+r6b75=1b4f41\x1b\\\x1bP1+r6b75=1b5b41\x1b\\" +
				"\x1bP1+r6b656e74=1b4f4d\x1b\\\x1bP1+r6b656e74=0d\x1b\\" +
				"\x1bP1+r6b6273=1b5b31323775\x1b\\\x1bP1+r6b6273=08\x1b\\"
			waitWritten(t, p.Session)
			got, err := os.ReadFile(f.Name())
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != want {
				t.Fatalf("reply %q, want %q", got, want)
			}
			var row strings.Builder
			for x := 0; x < 3; x++ {
				row.WriteRune(p.Term.Cell(x, 0).Char)
			}
			if row.String() != "END" {
				t.Fatalf("query polluted display: %q", row.String())
			}
		})
	}
}
