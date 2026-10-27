package session

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"bunk/internal/graphics"
	"bunk/internal/input"
	"bunk/internal/vt10x"
)

// Fuzz inputs are cut to these sizes so seed runs in go test stay fast.
const (
	maxFuzzInput = 16 << 10
	maxFuzzCols  = 100
	maxFuzzRows  = 30
)

// fuzzTimeLimit is how long one fuzz input may run. The fuzzer does not
// time inputs out itself; a stuck worker only shows as zero execs per second.
const fuzzTimeLimit = 10 * time.Second

// watchdog crashes the fuzz worker when an input runs longer than
// fuzzTimeLimit, so the fuzzer reports and keeps it. Call the result to stop
// the watchdog.
func watchdog() (stop func()) {
	timer := time.AfterFunc(fuzzTimeLimit, func() {
		panic(fmt.Sprintf("fuzz input ran for over %v", fuzzTimeLimit))
	})
	return func() { timer.Stop() }
}

// fuzzChunks splits data at lengths taken from cuts, cycling through them.
// Without cuts it returns data whole.
func fuzzChunks(data, cuts []byte) [][]byte {
	var chunks [][]byte
	for off, i := 0, 0; off < len(data); i++ {
		n := len(data) - off
		if len(cuts) > 0 {
			n = min(n, 1+int(cuts[i%len(cuts)]))
		}
		chunks = append(chunks, data[off:off+n])
		off += n
	}
	return chunks
}

// fuzzStreamSeeds reach every framing state: text, split UTF-8, CSI, OSC,
// DCS, APC, charsets, cancellation and graphics.
var fuzzStreamSeeds = []string{
	"plain text\r\n界😀\xe6\x97",
	"A\x1b]8;;https://example.org/界\x1b\\B\x1b]0;title\x07",
	"\x1b[?1049h\x1b[?2004h\x1b[1;31mred\x1b[m\x1b[?1049l",
	"\x1bP+q536d756c78\x1b\\\x1bP$qm\x1b\\\x1b[>q\x1b[c\x1b[>c\x1b[=c",
	"\x1b(0q\x1b(B\x1b#8\x1b7\x1b8\x1bM\x1b\x1b[m",
	"\x1b]0;unfinished\x18recovered\x1b[12\x1aafter",
	"\x1b_Ga=T,f=24,s=1,v=1;/wAA\x1b\\\x1bPq#0!8~\x1b\\\x1b]1337;File=inline=1:AAAA\x07",
	"\x1b]7;file://host/tmp\x07\x1b]52;c;aGVsbG8=\x07\x1b]133;A\x1b\\\x1b]133;D;1\x07",
	"\x1b]9;Claude needs your permission\x07\x1b]777;notify;Codex;Approval\x1b\\\x1b]99;e=1:p=body;RG9uZQ==\x07",
	"\x1b]7;x\x1b\x1b\\\x1b]7;file://host/real\x07",
	"\x1b]10;?\x07\x1b]11;?\x1b\\\x1b]12;?\x07\x1b]10;#ff0000\x07\x1b]10;?\x07",
	"\x1b[6n\x1b[?6n\x1b[5n\x1b[14t\x1b[16t\x1b[18t\x1b[?2026$p\x1b[?1$p",
	"\x1b[>1u\x1b[?u\x1b[=5;2u\x1b[?u\x1b[<u\x1b[<9u\x1b[?u",
	"\x1bP+q6b637575313b6b55503b6b6634;4e6f;zz\x1b\\",
}

func FuzzPTYStream(f *testing.F) {
	for i, seed := range fuzzStreamSeeds {
		f.Add([]byte{byte(i % 3), 4, 1}, []byte(seed))
	}
	f.Add([]byte{0}, []byte("\x1b]0;"+strings.Repeat("x", oscMaxBuf+8)+"\x07after"))
	f.Fuzz(func(t *testing.T, cuts, data []byte) {
		if len(data) > 4*oscMaxBuf {
			return
		}
		var whole, chunked ptyStream
		want := append([]byte(nil), whole.scan(data)...)
		var got []byte
		in := 0
		for _, chunk := range fuzzChunks(data, cuts) {
			out := chunked.scan(chunk)
			got = append(got, out...)
			in += len(chunk)
			if err := checkStreamBounds(&chunked); err != nil {
				t.Fatalf("after chunk %q: %v", chunk, err)
			}
			// Every output byte is an input byte, or one CAN that replaces
			// a discarded graphics transfer of over 8 MiB.
			if len(got)+len(chunked.pending)+len(chunked.utf8) > in {
				t.Fatalf("%d bytes in, %d out and %d held", in, len(got), len(chunked.pending)+len(chunked.utf8))
			}
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("chunked output differs from whole output\nwhole:   %q\nchunked: %q", want, got)
		}
		if whole.state != chunked.state || !bytes.Equal(whole.pending, chunked.pending) || !bytes.Equal(whole.utf8, chunked.utf8) ||
			whole.discard != chunked.discard || whole.graphics != chunked.graphics {
			t.Fatalf("framing state differs: whole %v %q %q, chunked %v %q %q",
				whole.state, whole.pending, whole.utf8, chunked.state, chunked.pending, chunked.utf8)
		}
	})
}

// checkStreamBounds checks that s holds at most one control and one
// incomplete UTF-8 sequence.
func checkStreamBounds(s *ptyStream) error {
	limit := oscMaxBuf
	if s.graphics {
		limit = graphics.MaxSequenceBytes + 4
	}
	if len(s.pending) > limit {
		return fmt.Errorf("%d bytes pending, over the %d limit", len(s.pending), limit)
	}
	if len(s.utf8) > 3 {
		return fmt.Errorf("%d bytes of UTF-8 carried", len(s.utf8))
	}
	if s.state == ptyText && len(s.pending) != 0 {
		return fmt.Errorf("%q pending outside a control", s.pending)
	}
	return nil
}

// FuzzOSCScanner frames output as ReadLoop does and scans it for OSC
// sequences. Forwarded sequences reach the host terminal, so none may carry
// a control byte that ends or extends it early.
func FuzzOSCScanner(f *testing.F) {
	for i, seed := range fuzzStreamSeeds {
		f.Add([]byte{byte(i % 3), 9}, []byte(seed))
	}
	f.Fuzz(func(t *testing.T, cuts, data []byte) {
		if len(data) > maxFuzzInput {
			return
		}
		var stream ptyStream
		var scan oscScanner
		scan.status = func(num int, payload []byte) {
			if !statusOSCNums[num] {
				t.Fatalf("status callback for OSC %d", num)
			}
			if st, ok := parseStatusOSC(num, payload); ok {
				if err := checkNotice(st.notice); err != nil {
					t.Fatalf("OSC %d %q: %v", num, payload, err)
				}
			}
		}
		emit := func(seq []byte) {
			if err := checkForwardedOSC(seq); err != nil {
				t.Fatalf("forwarded %q: %v", seq, err)
			}
		}
		endsInESC := false
		for _, chunk := range fuzzChunks(data, cuts) {
			framed := stream.scan(chunk)
			scan.Scan(framed, emit)
			if len(framed) > 0 {
				endsInESC = framed[len(framed)-1] == 0x1b
			}
			// Framed output holds complete controls, so the scanner must end
			// every OSC where framing does. Only ESC followed by a held,
			// split UTF-8 character can end framed output; that character
			// comes next, so it never starts an OSC.
			split := scan.state == oscSeenESC && endsInESC && len(stream.utf8) > 0
			if scan.state != oscIdle && !split {
				t.Fatalf("scanner left in state %d after framed chunk %q", scan.state, chunk)
			}
		}
	})
}

// checkForwardedOSC checks a sequence the scanner hands to the host.
func checkForwardedOSC(seq []byte) error {
	if len(seq) > oscMaxBuf {
		return fmt.Errorf("%d bytes, over the %d limit", len(seq), oscMaxBuf)
	}
	body, ok := bytes.CutPrefix(seq, []byte("\x1b]"))
	if !ok {
		return fmt.Errorf("no OSC introducer")
	}
	switch {
	case bytes.HasSuffix(body, []byte("\x1b\\")):
		body = body[:len(body)-2]
	case bytes.HasSuffix(body, []byte("\x07")):
		body = body[:len(body)-1]
	default:
		return fmt.Errorf("no terminator")
	}
	numText, payload, _ := bytes.Cut(body, []byte(";"))
	num, err := strconv.Atoi(string(numText))
	if err != nil || !oscForwardNums[num] {
		return fmt.Errorf("OSC number %q is not forwarded", numText)
	}
	if !utf8.Valid(payload) {
		return fmt.Errorf("payload is not UTF-8")
	}
	for _, r := range string(payload) {
		if r < 0x20 && r != '\t' || r >= 0x7f && r <= 0x9f {
			return fmt.Errorf("payload holds control %U", r)
		}
	}
	return nil
}

// checkNotice checks an attention notice shown in tab labels.
func checkNotice(notice string) error {
	if !utf8.ValidString(notice) {
		return fmt.Errorf("notice %q is not UTF-8", notice)
	}
	if n := utf8.RuneCountInString(notice); n > noticeLimit {
		return fmt.Errorf("notice has %d runes, over %d", n, noticeLimit)
	}
	for _, r := range notice {
		if unicode.IsControl(r) {
			return fmt.Errorf("notice %q holds control %U", notice, r)
		}
	}
	return nil
}

// FuzzStatusOSC parses attention requests and command results directly.
func FuzzStatusOSC(f *testing.F) {
	for _, seed := range []struct {
		num     uint8
		payload string
	}{
		{0, "Claude needs your permission"}, {0, "4;1;50"}, {0, "line1\nline2\x9b"},
		{1, "e=1:p=body;" + "RG9uZQ=="}, {1, "i=1:p=close;"}, {1, "p=title;\x1b[31m"},
		{2, "D;1"}, {2, "D;99999999999999999999"}, {2, "C"},
		{3, "notify;Codex;Approval requested"}, {3, "notify;Command completed;x"},
		{0, strings.Repeat("界", noticeLimit+5)}, {0, "\xff\xfe" + strings.Repeat("e\u0301", 150)},
	} {
		f.Add(seed.num, []byte(seed.payload))
	}
	nums := []int{9, 99, 133, 777}
	f.Fuzz(func(t *testing.T, sel uint8, payload []byte) {
		num := nums[int(sel)%len(nums)]
		st, ok := parseStatusOSC(num, payload)
		if !ok {
			if st != (statusOSC{}) {
				t.Fatalf("rejected OSC %d %q returned %+v", num, payload, st)
			}
			return
		}
		if err := checkNotice(st.notice); err != nil {
			t.Fatalf("OSC %d %q: %v", num, payload, err)
		}
		if (st.notice != "") == (st.started || st.finished) {
			t.Fatalf("OSC %d %q: %+v is neither a notice nor a command boundary", num, payload, st)
		}
	})
}

// fuzzSession is a session without a child process, fed as ReadLoop feeds
// a real one. Replies to the application go to a pipe.
type fuzzSession struct {
	*Session
	stream  ptyStream
	replies bytes.Buffer
	done    chan struct{}
	reader  *os.File
}

// newFuzzSession returns a session whose replies are collected; call
// closeReplies before reading them.
func newFuzzSession(t *testing.T, cols, rows, scrollback int) *fuzzSession {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	s := newTestSession(cols, rows)
	s.Scrollback = Ring{MaxLines: scrollback}
	s.ScrollbackLines = scrollback
	s.PTY = w
	s.ThemeFG, s.ThemeBG, s.ThemeCursor = "rgb:d0d0/d0d0/d0d0", "rgb:1010/1010/1010", "rgb:ffff/ffff/ffff"
	s.agent = "claude"
	s.oscScan.status = s.onStatusOSC
	f := &fuzzSession{Session: s, done: make(chan struct{}), reader: r}
	go func() {
		defer close(f.done)
		io.Copy(&f.replies, r) //nolint:errcheck // the pipe closes when the test ends
	}()
	t.Cleanup(func() {
		f.closeReplies()
		r.Close() //nolint:errcheck // test pipe cleanup
	})
	return f
}

// feed passes one PTY read through framing, the OSC scanner and the
// emulator, as ReadLoop does.
func (f *fuzzSession) feed(chunk []byte, emit func([]byte)) {
	framed := f.stream.scan(chunk)
	if len(framed) == 0 {
		return
	}
	f.oscScan.Scan(framed, emit)
	f.Lock()
	f.CaptureAndWrite(framed)
	f.Unlock()
}

// closeReplies closes the reply pipe and waits until every reply is read.
func (f *fuzzSession) closeReplies() {
	if f.PTY != nil {
		f.WaitWritten(5 * time.Second) // a stuck write shows as missing replies
		f.PTY.Close()                  //nolint:errcheck // test pipe cleanup
		f.PTY = nil
		<-f.done
	}
}

// checkReplyStream returns an error unless replies is a run of complete
// escape sequences: CSI, or OSC, DCS and APC strings of printable ASCII.
func checkReplyStream(replies []byte) error {
	for i := 0; i < len(replies); {
		if replies[i] != 0x1b || i+1 == len(replies) {
			return fmt.Errorf("byte %d (%q) does not start an escape sequence", i, replies[i])
		}
		start := i
		i += 2
		switch replies[start+1] {
		case '[':
			for i < len(replies) && replies[i] >= 0x20 && replies[i] <= 0x3f {
				i++
			}
			if i == len(replies) || replies[i] < 0x40 || replies[i] > 0x7e {
				return fmt.Errorf("unterminated CSI reply %q", replies[start:])
			}
			i++
		case ']', 'P', '_':
			for i < len(replies) && replies[i] >= 0x20 && replies[i] <= 0x7e {
				i++
			}
			switch {
			case i < len(replies) && replies[i] == 0x07 && replies[start+1] == ']':
				i++
			case i+1 < len(replies) && replies[i] == 0x1b && replies[i+1] == '\\':
				i += 2
			default:
				return fmt.Errorf("string reply %q holds a control byte or no terminator", replies[start:min(len(replies), i+1)])
			}
		default:
			return fmt.Errorf("unknown reply introducer %q", replies[start:start+2])
		}
	}
	return nil
}

// maxReplyBytes bounds the replies to n bytes of output. The largest reply
// per input byte is a DECRQSS SGR report: about 85 bytes for "\x1bP$qm\x1b\\".
func maxReplyBytes(n int) int { return 16*n + 256 }

// fuzzSize maps fuzzer bytes to a grid size.
func fuzzSize(cols, rows uint8) (int, int) {
	return 1 + int(cols)%maxFuzzCols, 1 + int(rows)%maxFuzzRows
}

// FuzzQueryReplies runs output through the whole session: framing, the OSC
// scanner, query replies and the emulator. Replies go back to the
// application, so they must be complete sequences and bounded.
func FuzzQueryReplies(f *testing.F) {
	for i, seed := range fuzzStreamSeeds {
		f.Add(uint8(20+i), uint8(i), []byte{byte(i % 3), 5}, []byte(seed))
	}
	f.Fuzz(func(t *testing.T, cols, rows uint8, cuts, data []byte) {
		if len(data) > maxFuzzInput {
			return
		}
		defer watchdog()()
		c, r := fuzzSize(cols, rows)
		s := newFuzzSession(t, c, r, 50)
		emit := func(seq []byte) {
			if err := checkForwardedOSC(seq); err != nil {
				t.Fatalf("forwarded %q: %v", seq, err)
			}
		}
		for _, chunk := range fuzzChunks(data, cuts) {
			s.feed(chunk, emit)
		}
		s.closeReplies()
		replies := s.replies.Bytes()
		if err := checkReplyStream(replies); err != nil {
			t.Fatalf("malformed reply: %v\nreplies: %q", err, replies)
		}
		if len(replies) > maxReplyBytes(len(data)) {
			t.Fatalf("%d bytes of replies to %d bytes of output", len(replies), len(data))
		}
		if err := checkNotice(s.notice); err != nil {
			t.Fatal(err)
		}
		if len(s.KittyStack) > 32 {
			t.Fatalf("Kitty keyboard stack holds %d entries", len(s.KittyStack))
		}
	})
}

// gridRow returns virtual row v of the scrollback and live grid.
func gridRow(s *Session, v int) []vt10x.Glyph {
	cols, rows := s.Term.Size()
	if v < s.Scrollback.Count {
		return s.Scrollback.Get(v)
	}
	if v-s.Scrollback.Count < rows {
		return CaptureRow(s.Term, v-s.Scrollback.Count, cols)
	}
	return nil
}

// cellRunes returns the lower-case text of cells [from, to) of row, as
// search sees it: continuations skipped, empty cells as spaces.
func cellRunes(row []vt10x.Glyph, from, to int) []rune {
	var out []rune
	for x := from; x < to; x++ {
		ch, combining := ' ', ""
		if x < len(row) {
			if row[x].Width < 0 {
				continue
			}
			if row[x].Char != 0 {
				ch = row[x].Char
			}
			combining = row[x].Combining
		}
		out = append(out, unicode.ToLower(ch))
		for _, c := range combining {
			out = append(out, unicode.ToLower(c))
		}
	}
	return out
}

// containsRunes reports whether needle occurs in haystack.
func containsRunes(haystack, needle []rune) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if string(haystack[i:i+len(needle)]) == string(needle) {
			return true
		}
	}
	return false
}

// FuzzFindAndSelect searches and copies text on a grid built from fuzzed
// output, with scrollback.
func FuzzFindAndSelect(f *testing.F) {
	for i, seed := range fuzzStreamSeeds {
		f.Add(uint8(10+i), uint8(2+i), []byte(seed+"\r\nneedle Needle e\u0301\r\n"), "needle", int16(-1), int16(0), int16(40), int16(5))
	}
	f.Add(uint8(5), uint8(3), []byte("界界AB\r\nabcdefg界x"), "", int16(0), int16(-2), int16(3), int16(9))
	f.Add(uint8(8), uint8(2), []byte("e\u0301\u0301\u0301 x\r\n\x1b[31mRED\x1b[m"), "\u0301", int16(1), int16(0), int16(0), int16(2))
	f.Fuzz(func(t *testing.T, cols, rows uint8, data []byte, query string, r0, c0, r1, c1 int16) {
		if len(data) > maxFuzzInput || len(query) > 256 {
			return
		}
		defer watchdog()()
		c, r := fuzzSize(cols, rows)
		s := newTestSession(c, r)
		s.Scrollback = Ring{MaxLines: 20}
		s.Lock()
		s.Feed(data)
		total := s.Scrollback.Count + r
		s.Unlock()

		needle := []rune(strings.ToLower(query))
		matches := s.FindAll(query)
		if want := s.oracleFindAll(query); !slices.Equal(matches, want) {
			t.Fatalf("FindAll(%q) = %v, oracle %v", query, matches, want)
		}
		s.Lock()
		defer s.Unlock()
		if query == "" && len(matches) != 0 {
			t.Fatalf("empty query matched %d times", len(matches))
		}
		for i, m := range matches {
			if m.VRow < 0 || m.VRow >= total || m.Col < 0 || m.Length < 1 || m.Col+m.Length > c {
				t.Fatalf("match %+v outside the %dx%d grid", m, c, total)
			}
			if i > 0 && (m.VRow < matches[i-1].VRow || m.VRow == matches[i-1].VRow && m.Col < matches[i-1].Col) {
				t.Fatalf("match %+v after %+v", m, matches[i-1])
			}
			if text := cellRunes(gridRow(s, m.VRow), m.Col, m.Col+m.Length); !containsRunes(text, needle) {
				t.Fatalf("match %+v covers %q, not %q", m, string(text), query)
			}
		}
		if query != "" && len(matches) == 0 {
			for v := 0; v < total; v++ {
				if text := cellRunes(gridRow(s, v), 0, c); containsRunes(text, needle) {
					t.Fatalf("row %d holds %q but FindAll found nothing", v, query)
				}
			}
		}

		// Selections come from clamped pointer positions, but can outlive a
		// resize or scrollback trim; allow a margin around the grid.
		pos := func(row, col int16) SelPos {
			return SelPos{Row: int(row)%(total+6) - 3, Col: int(col)%(c+6) - 3}
		}
		s.SelActive, s.SelAnchor, s.SelCursor = true, pos(r0, c0), pos(r1, c1)
		text := s.SelText()
		if !utf8.ValidString(text) {
			t.Fatalf("selection text %q is not UTF-8", text)
		}
		start, end := s.SelNorm()
		lines := end.Row - start.Row + 1
		if len(text) > lines*(c*(utf8.UTFMax+1024)+1) {
			t.Fatalf("%d bytes of text from %d rows of %d cells", len(text), lines, c)
		}
	})
}

// FuzzXTGETTCAP answers capability queries directly. The reply echoes the
// query, so it must stay hex and one DCS string.
func FuzzXTGETTCAP(f *testing.F) {
	for _, name := range []string{"TN", "Co", "kcuu1", "kUP5", "kUP9", "kf24", "FE", "k;", "@8", "kbs", "Smulx", "cup", "\x1b"} {
		f.Add(hex.EncodeToString([]byte(name)), 1, uint32(vt10x.ModeAppCursor|vt10x.ModeAppKeypad), uint8(input.KeyBackspace))
	}
	f.Add("6B55", 0, uint32(0), uint8(0))
	f.Add("zz", 0, uint32(0), uint8(0))
	f.Add("", 0, uint32(0), uint8(0))
	f.Fuzz(func(t *testing.T, hexCap string, flags int, mode uint32, backspace uint8) {
		resp := xtgettcapResponseKey(hexCap, flags, vt10x.ModeFlag(mode), input.Key(backspace))
		name, err := hex.DecodeString(hexCap)
		if err != nil || len(name) == 0 {
			if resp != "" {
				t.Fatalf("malformed query %q answered %q", hexCap, resp)
			}
			return
		}
		body, ok := strings.CutSuffix(resp, "\x1b\\")
		if !ok {
			t.Fatalf("query %q: reply %q has no ST", hexCap, resp)
		}
		if value, ok := strings.CutPrefix(body, "\x1bP1+r"+hexCap+"="); ok {
			if _, err := hex.DecodeString(value); err != nil || value == "" {
				t.Fatalf("query %q: value %q is not hex", hexCap, value)
			}
		} else if body != "\x1bP0+r"+hexCap {
			t.Fatalf("query %q: reply %q", hexCap, resp)
		}
		if err := checkReplyStream([]byte(resp)); err != nil {
			t.Fatal(err)
		}
		if len(resp) > len(hexCap)+80 {
			t.Fatalf("query %q: %d-byte reply", hexCap, len(resp))
		}
	})
}
