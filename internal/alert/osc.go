package alert

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// The longest title and body OSC777 and DesktopText give, in bytes.
// Host terminals and notification servers accept far longer text; these
// keep a notification short.
const (
	titleBytes = 120
	bodyBytes  = 360
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
	b.WriteString(cleanText(a.Title, titleBytes, true))
	b.WriteByte(';')
	b.WriteString(cleanText(body, bodyBytes, true))
	b.WriteString("\x1b\\")
	return []byte(b.String())
}

// DesktopText returns a's title and body for a desktop notification
// server: one line of title, and the notice and where the agent runs on
// two lines. Control and bidirectional formatting characters go, both are
// cut to fit, and the body is escaped for servers that read it as markup,
// as GNOME Shell does.
func DesktopText(a Alert) (title, body string) {
	body = cleanText(a.Notice, bodyBytes, false)
	if where := cleanText(a.Where, titleBytes, false); where != "" {
		body += "\n" + where
	}
	return cleanText(a.Title, titleBytes, false), markupEscaper.Replace(body)
}

var markupEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// cleanText makes untrusted text one line of valid UTF-8 without control
// or bidirectional formatting characters, with runs of blanks as one
// space, and cuts it to limit bytes at a character boundary, ending with
// "…" when cut. With noSemicolons, a ';' becomes ','.
func cleanText(s string, limit int, noSemicolons bool) string {
	var b strings.Builder
	space := false
	for i, r := range s {
		switch {
		case r == utf8.RuneError && !strings.HasPrefix(s[i:], "\uFFFD"):
			continue // a byte that is not UTF-8
		case unicode.IsControl(r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) || r == ' ':
			space = b.Len() > 0
			continue
		case bidiControl(r):
			continue
		case r == ';' && noSemicolons:
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
