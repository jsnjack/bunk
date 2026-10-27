package session

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

// Regressions found by the session fuzz targets. Each input is also kept in
// testdata/fuzz.

// TestOSCScannerEndsWhereFramingEnds: ptyStream ends an OSC at ESC ESC \ and
// at ESC BEL. The scanner used to treat the second ESC or the BEL as content
// and kept reading, so it swallowed the OSC 7, 52, 133 or attention request
// that followed.
func TestOSCScannerEndsWhereFramingEnds(t *testing.T) {
	const next = "\x1b]7;file://host/real\x07"
	for _, tc := range []struct{ name, junk string }{
		{"ESC ESC ST", "\x1b]7;x\x1b\x1b\\"},
		{"ESC BEL", "\x1b]7;x\x1b\x07"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stream ptyStream
			framed := stream.scan([]byte(tc.junk + next))
			var scan oscScanner
			var got [][]byte
			scan.Scan(framed, func(seq []byte) { got = append(got, bytes.Clone(seq)) })
			if len(got) != 1 || string(got[0]) != next {
				t.Fatalf("forwarded %q, want only %q", got, next)
			}
			if scan.state != oscIdle {
				t.Fatalf("scanner state %d after complete output", scan.state)
			}
		})
	}
}

// TestNoticeRuneLimit: a notice is at most noticeLimit runes, the ellipsis
// included. It used to keep noticeLimit runes and then add the ellipsis.
func TestNoticeRuneLimit(t *testing.T) {
	for _, text := range []string{strings.Repeat("x", noticeLimit+1), strings.Repeat("界", noticeLimit+50)} {
		st, ok := noticeOSC(text)
		if !ok {
			t.Fatal("notice rejected")
		}
		if n := utf8.RuneCountInString(st.notice); n != noticeLimit || !strings.HasSuffix(st.notice, "…") {
			t.Fatalf("notice of %d runes ends %q, want %d ending in an ellipsis", n, st.notice[len(st.notice)-6:], noticeLimit)
		}
	}
	if st, _ := noticeOSC(strings.Repeat("x", noticeLimit)); st.notice != strings.Repeat("x", noticeLimit) {
		t.Fatal("notice of exactly noticeLimit runes was cut")
	}
}

// TestFindAllEmptyQuery: an empty query used to index ends[-1] and panic.
func TestFindAllEmptyQuery(t *testing.T) {
	s := newTestSession(5, 3)
	s.Lock()
	s.Feed([]byte("abc"))
	s.Unlock()
	if m := s.FindAll(""); m != nil {
		t.Fatalf("FindAll(\"\") = %v, want nil", m)
	}
}

// TestPTYStreamCharsetThenEscape: an ESC after ESC ( or ESC # starts a new
// control, as it does in vt10x. Framing used to end the charset sequence at
// that ESC and pass the OSC after it through as text, so a query split
// across two reads went unanswered.
func TestPTYStreamCharsetThenEscape(t *testing.T) {
	const query = "\x1b]10;?\x07"
	for _, prefix := range []string{"\x1b#", "\x1b("} {
		input := prefix + query
		for split := 0; split <= len(input); split++ {
			var stream ptyStream
			first := string(stream.scan([]byte(input[:split])))
			second := string(stream.scan([]byte(input[split:])))
			if first+second != query || first != "" && first != query {
				t.Fatalf("%q split at %d: framed %q then %q, want %q in one piece", input, split, first, second, query)
			}
		}
	}
}

// TestOSCForwardedWithoutControls: an OSC without a ';' had its number found
// by trimming every trailing BEL, ESC and backslash, so "7" plus stray ESC
// bytes was forwarded to the host terminal verbatim.
func TestOSCForwardedWithoutControls(t *testing.T) {
	for _, seq := range []string{"\x1b]7\x1b\x1b\x07", "\x1b]7\x1b\x1b\x1b\\", "\x1b]7\\\x07", "\x1b]133\x1b\x1b\x07"} {
		var got [][]byte
		var scan oscScanner
		scan.Scan([]byte(seq), func(b []byte) { got = append(got, bytes.Clone(b)) })
		if len(got) != 0 {
			t.Errorf("%q forwarded as %q", seq, got)
		}
	}
	for _, seq := range []string{"\x1b]7\x07", "\x1b]133;A\x1b\\"} {
		var got [][]byte
		var scan oscScanner
		scan.Scan([]byte(seq), func(b []byte) { got = append(got, bytes.Clone(b)) })
		if len(got) != 1 || string(got[0]) != seq {
			t.Errorf("%q forwarded as %q, want it unchanged", seq, got)
		}
	}
}

// TestSelTextOutsideGrid: a selection that starts left of column 0 used to
// index cells[-3] and panic. Pointer positions are clamped, but a selection
// can outlive the grid it was made on.
func TestSelTextOutsideGrid(t *testing.T) {
	s := newTestSession(4, 2)
	s.Lock()
	defer s.Unlock()
	s.Feed([]byte("abcd\r\nefgh"))
	for _, tc := range []struct {
		anchor, cursor SelPos
		want           string
	}{
		{SelPos{Row: 0, Col: -3}, SelPos{Row: 0, Col: 1}, "ab"},
		{SelPos{Row: 0, Col: 2}, SelPos{Row: 1, Col: 9}, "cd\nefgh"},
		{SelPos{Row: -2, Col: 0}, SelPos{Row: 0, Col: 0}, "\n\na"},
	} {
		s.SelActive, s.SelAnchor, s.SelCursor = true, tc.anchor, tc.cursor
		if got := s.SelText(); got != tc.want {
			t.Errorf("SelText(%+v to %+v) = %q, want %q", tc.anchor, tc.cursor, got, tc.want)
		}
	}
}
