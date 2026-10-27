package session

import (
	"bunk/internal/vt10x"
	"encoding/hex"
	"testing"
)

func TestCapabilityValues(t *testing.T) {
	for _, tc := range []struct {
		names []string
		value string
	}{
		{[]string{"TN", "name"}, "xterm-256color"},
		{[]string{"Co", "colors"}, "256"},
		{[]string{"RGB"}, "8"},
		{[]string{"kcuu1", "ku"}, "\x1b[A"},
		{[]string{"kcud1", "kd"}, "\x1b[B"},
		{[]string{"kcuf1", "kr"}, "\x1b[C"},
		{[]string{"kcub1", "kl"}, "\x1b[D"},
		{[]string{"khome", "kh"}, "\x1b[H"},
		{[]string{"kend", "@7"}, "\x1b[F"},
		{[]string{"kich1", "kI"}, "\x1b[2~"},
		{[]string{"kdch1", "kD"}, "\x1b[3~"},
		{[]string{"kpp", "kP"}, "\x1b[5~"},
		{[]string{"knp", "kN"}, "\x1b[6~"},
		{[]string{"kbs", "kb"}, "\x08"},
		{[]string{"kcbt", "kB"}, "\x1b[Z"},
		{[]string{"kent", "@8"}, "\r"},
		{[]string{"kf1", "k1"}, "\x1bOP"},
		{[]string{"kf10", "k;"}, "\x1b[21~"},
		{[]string{"kf11", "F1"}, "\x1b[23~"},
		{[]string{"kf12", "F2"}, "\x1b[24~"},
		{[]string{"kf13", "F3"}, "\x1b[1;2P"},
		{[]string{"kf19", "F9"}, "\x1b[18;2~"},
		{[]string{"kf20", "FA"}, "\x1b[19;2~"},
		{[]string{"kf24", "FE"}, "\x1b[24;2~"},
		{[]string{"kUP", "kUP2"}, "\x1b[1;2A"},
		{[]string{"kUP3"}, "\x1b[1;3A"},
		{[]string{"kUP4"}, "\x1b[1;4A"},
		{[]string{"kUP5"}, "\x1b[1;5A"},
		{[]string{"kUP6"}, "\x1b[1;6A"},
		{[]string{"kUP7"}, "\x1b[1;7A"},
		{[]string{"kUP8"}, "\x1b[1;8A"},
		{[]string{"kDN"}, "\x1b[1;2B"},
		{[]string{"kLFT"}, "\x1b[1;2D"},
		{[]string{"kRIT"}, "\x1b[1;2C"},
		{[]string{"kHOM"}, "\x1b[1;2H"},
		{[]string{"kEND"}, "\x1b[1;2F"},
		{[]string{"kIC"}, "\x1b[2;2~"},
		{[]string{"kDC"}, "\x1b[3;2~"},
		{[]string{"kPRV"}, "\x1b[5;2~"},
		{[]string{"kNXT"}, "\x1b[6;2~"},
	} {
		for _, name := range tc.names {
			t.Run(name, func(t *testing.T) {
				encoded := hex.EncodeToString([]byte(name))
				want := "\x1bP1+r" + encoded + "=" + hex.EncodeToString([]byte(tc.value)) + "\x1b\\"
				if got := xtgettcapResponse(encoded, 0, 0); got != want {
					t.Fatalf("got %q, want %q", got, want)
				}
			})
		}
	}
}
func TestCapabilityModes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mode  vt10x.ModeFlag
		flags int
		want  string
	}{
		{"kcuu1", vt10x.ModeAppCursor, 0, "\x1bOA"},
		{"khome", vt10x.ModeAppCursor, 0, "\x1bOH"},
		{"kUP5", vt10x.ModeAppCursor, 0, "\x1b[1;5A"},
		{"kent", vt10x.ModeAppKeypad, 0, "\x1bOM"},
		{"kent", vt10x.ModeAppKeypad, 1, "\x1b[57414;1u"},
		{"kbs", 0, 1, "\x1b[127u"},
		{"kcbt", 0, 1, "\x1b[9;2u"},
	} {
		t.Run(tc.name+tc.want, func(t *testing.T) {
			encoded := hex.EncodeToString([]byte(tc.name))
			want := "\x1bP1+r" + encoded + "=" + hex.EncodeToString([]byte(tc.want)) + "\x1b\\"
			if got := xtgettcapResponse(encoded, tc.flags, tc.mode); got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
}
func TestCapabilityUnsupported(t *testing.T) {
	for _, name := range []string{"kf0", "kf25", "kf01", "kUP1", "kUP9", "kUP10", "cup", "unknown"} {
		t.Run(name, func(t *testing.T) {
			encoded := hex.EncodeToString([]byte(name))
			if got := xtgettcapResponse(encoded, 0, 0); got != "\x1bP0+r"+encoded+"\x1b\\" {
				t.Fatalf("unsupported capability accepted: %q", got)
			}
		})
	}
	for _, encoded := range []string{"", "a", "gg", "6b 75"} {
		t.Run("invalid "+encoded, func(t *testing.T) {
			if got := xtgettcapResponse(encoded, 0, 0); got != "" {
				t.Fatalf("malformed query reply: %q", got)
			}
		})
	}
}
