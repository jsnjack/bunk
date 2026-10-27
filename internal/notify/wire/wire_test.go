package wire

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestEncodeDecode(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		r    Report
		line string
	}{
		{"socket report", Report{Session: "tok", Event: "working"}, `{"v":1,"session":"tok","event":"working"}` + "\n"},
		{"no token", Report{Event: "done", Message: "ok"}, `{"v":1,"event":"done","message":"ok"}` + "\n"},
		{"remote fields", Report{Session: "tok", Event: "needs-you", Message: "Allow <npm>?", Agent: "claude", Host: "build01",
			Repo: "bunk", Branch: "main", Cwd: "/home/u/bunk"},
			`{"v":1,"session":"tok","event":"needs-you","message":"Allow <npm>?","agent":"claude","host":"build01","repo":"bunk","branch":"main","cwd":"/home/u/bunk"}` + "\n"},
		{"cleaned fields", Report{Event: "idle", Agent: " cla\x1bude ", Branch: "a\xffb"},
			`{"v":1,"event":"idle","agent":"cla ude","branch":"a` + "�" + `b"}` + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line, err := tc.r.Encode()
			if err != nil {
				t.Fatal(err)
			}
			if string(line) != tc.line {
				t.Fatalf("Encode = %q, want %q", line, tc.line)
			}
			r, err := Decode(line)
			if err != nil {
				t.Fatal(err)
			}
			again, err := r.Encode()
			if err != nil || !bytes.Equal(again, line) {
				t.Fatalf("round trip: %q, %v", again, err)
			}
		})
	}
}

func TestEncodeRejects(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		r    Report
	}{
		{"no event", Report{Session: "tok"}},
		{"long token", Report{Session: strings.Repeat("a", MaxToken+1), Event: "idle"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if line, err := tc.r.Encode(); err == nil {
				t.Fatalf("Encode = %q, want an error", line)
			}
		})
	}
	// Every field at its longest, escaped at twice its size, fits one line.
	quotes := func(n int) string { return strings.Repeat(`"`, n) }
	r := Report{Session: strings.Repeat("f", MaxToken), Event: "needs-you", Message: quotes(5000),
		Agent: quotes(500), Host: quotes(500), Repo: quotes(500), Branch: quotes(500), Cwd: quotes(500)}
	line, err := r.Encode()
	if err != nil || len(line) > MaxLine {
		t.Fatalf("worst case: %d bytes, %v", len(line), err)
	}
}

func TestDecodeRejects(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, line string
	}{
		{"empty", ""},
		{"not JSON", "working tok"},
		{"no version", `{"session":"tok","event":"working"}`},
		{"version 2", `{"v":2,"event":"working"}`},
		{"no event", `{"v":1,"session":"tok"}`},
		{"long token", `{"v":1,"session":"` + strings.Repeat("a", MaxToken+1) + `","event":"working"}`},
		{"trailing data", `{"v":1,"event":"working"} {}`},
		{"too long", `{"v":1,"event":"idle","message":"` + strings.Repeat("x", MaxLine) + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if r, err := Decode([]byte(tc.line)); err == nil {
				t.Fatalf("Decode(%.60q) = %+v, want an error", tc.line, r)
			}
		})
	}
	r, err := Decode([]byte(`{"v":1,"event":"done","future":"field"}`))
	if err != nil || r.Event != "done" {
		t.Fatalf("unknown fields must be ignored: %+v, %v", r, err)
	}
}

func TestCleanText(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("é", MaxText) // 2 bytes each
	for _, tc := range []struct {
		name, in, want string
	}{
		{"plain", "Allow npm install?", "Allow npm install?"},
		{"controls", "a\x1b]0;x\x07b\r\nc", "a ]0;x b  c"},
		{"invalid UTF-8", "a\xffb", "a�b"},
		{"trimmed", "  \t x \n", "x"},
		{"cut on a character boundary", "a" + long, "a" + strings.Repeat("é", (MaxText-1)/2)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := CleanText(tc.in)
			if got != tc.want {
				t.Fatalf("CleanText = %.80q, want %.80q", got, tc.want)
			}
			if len(got) > MaxText || !utf8.ValidString(got) || CleanText(got) != got {
				t.Fatalf("CleanText result %d bytes, valid %v, idempotent %v", len(got), utf8.ValidString(got), CleanText(got) == got)
			}
		})
	}
	if got := cleanField(strings.Repeat("b", 300)); len(got) != MaxField {
		t.Fatalf("cleanField kept %d bytes, want %d", len(got), MaxField)
	}
}

func TestInBand(t *testing.T) {
	t.Parallel()
	line, err := Report{Session: "tok", Event: "needs-you", Message: "Allow?", Agent: "claude"}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	seq := InBand(line)
	payload, ok := bytes.CutPrefix(seq, []byte("\x1b]1337;"))
	if !ok || !bytes.HasSuffix(payload, []byte("\a")) || bytes.ContainsAny(payload[:len(payload)-1], "\x1b\a\n") {
		t.Fatalf("InBand = %q", seq)
	}
	got, ok := FromUserVar(bytes.TrimSuffix(payload, []byte("\a")))
	if !ok || !bytes.Equal(got, bytes.TrimSuffix(line, []byte("\n"))) {
		t.Fatalf("FromUserVar = %q, %v; want %q", got, ok, line)
	}
	for _, tc := range []struct {
		name, payload string
	}{
		{"an image", "File=inline=1:AAAA"},
		{"another variable", "SetUserVar=other=e30="},
		{"bad base64", "SetUserVar=" + UserVar + "=not base64!"},
		{"oversized", "SetUserVar=" + UserVar + "=" + strings.Repeat("A", 2*MaxLine)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := FromUserVar([]byte(tc.payload)); ok {
				t.Fatalf("FromUserVar(%.40q) = %q, want false", tc.payload, got)
			}
		})
	}
}

func TestWrap(t *testing.T) {
	t.Parallel()
	seq := []byte("\x1b]1337;SetUserVar=bunk_notify=e30=\a")
	env := func(vars ...string) func(string) string {
		return func(key string) string {
			for i := 0; i+1 < len(vars); i += 2 {
				if vars[i] == key {
					return vars[i+1]
				}
			}
			return ""
		}
	}
	for _, tc := range []struct {
		name   string
		getenv func(string) string
		want   string
	}{
		{"plain terminal", env(), string(seq)},
		{"tmux", env("TMUX", "/tmp/tmux-1000/default,1,0"), "\x1bPtmux;\x1b\x1b]1337;SetUserVar=bunk_notify=e30=\a\x1b\\"},
		{"screen", env("STY", "1.pts-0.host"), "\x1bP" + string(seq) + "\x1b\\"},
		{"tmux inside screen", env("STY", "1.pts", "TMUX", "/tmp/t,1,0"), "\x1bPtmux;\x1b\x1b]1337;SetUserVar=bunk_notify=e30=\a\x1b\\"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Wrap(seq, tc.getenv); string(got) != tc.want {
				t.Fatalf("Wrap = %q, want %q", got, tc.want)
			}
		})
	}
}
