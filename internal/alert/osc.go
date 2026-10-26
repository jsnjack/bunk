package alert

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// The longest title and body OSC777 writes, in bytes. Host terminals
// accept OSC strings far longer; these keep a notification short.
const (
	oscTitleBytes = 120
	oscBodyBytes  = 360
)

// OSC777 encodes a as an urxvt notify sequence for a host terminal:
// ESC ] 777 ; notify ; TITLE ; BODY ESC \. foot, WezTerm, Ghostty, urxvt
// and VTE builds with the old Fedora notification patch show it on the
// desktop. The notice and the agent, repo and branch names come from
// programs, so control and bidirectional formatting characters go, a ';'
// becomes ',' (it would end the title, and some terminals split the body
// at it too), the body takes one line, and both are cut to fit.
func OSC777(a Alert) []byte {
	body := a.Notice
	if a.Where != "" {
		body += " · " + a.Where
	}
	var b strings.Builder
	b.WriteString("\x1b]777;notify;")
	b.WriteString(oscText(a.Title, oscTitleBytes))
	b.WriteByte(';')
	b.WriteString(oscText(body, oscBodyBytes))
	b.WriteString("\x1b\\")
	return []byte(b.String())
}

// oscText makes untrusted text safe inside an OSC string and cuts it to
// limit bytes at a character boundary, ending with "…" when cut.
func oscText(s string, limit int) string {
	var b strings.Builder
	space := false
	for i, r := range s {
		switch {
		case r == utf8.RuneError && !strings.HasPrefix(s[i:], "�"):
			continue // a byte that is not UTF-8
		case unicode.IsControl(r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) || r == ' ':
			space = b.Len() > 0
			continue
		case bidiControl(r):
			continue
		case r == ';':
			r = ','
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	out := b.String()
	if len(out) <= limit {
		return out
	}
	const more = "…"
	cut := limit - len(more)
	for cut > 0 && !utf8.RuneStart(out[cut]) {
		cut--
	}
	return strings.TrimRight(out[:cut], " ") + more
}

// bidiControl reports whether r changes the direction of the text after
// it: embeddings, overrides, isolates and the directional marks.
func bidiControl(r rune) bool {
	switch {
	case r == 0x061C, r == 0x200E, r == 0x200F:
		return true
	case r >= 0x202A && r <= 0x202E:
		return true
	case r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}
