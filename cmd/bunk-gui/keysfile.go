package main

// A line-based editor for config.toml, so the shortcuts and preferences
// windows can save settings and leave everything else (comments, order,
// blank lines, other tables) byte for byte as the user wrote it. It edits
// top-level keys, a [table] such as [keys], and entries of an array of
// tables such as [[agents]].

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

var (
	tableHeader = regexp.MustCompile(`^\s*\[`)
	headerName  = regexp.MustCompile(`^\s*(\[\[?)\s*([A-Za-z0-9_.-]+)\s*\]\]?\s*(#.*)?$`)
	keysHeader  = regexp.MustCompile(`^\s*\[\s*keys\s*\]\s*(#.*)?$`)
	keyLine     = regexp.MustCompile(`^(\s*)("?)([A-Za-z0-9_-]+)("?)\s*=`)
	wasComment  = regexp.MustCompile(`#\s*was\s+(.+?)\s*$`)
)

// keysEdit is one change to a table: key = value, where value is TOML
// (e.g. "\"ctrl+j\"" or "[\"f1\", \"f2\"]"); remove deletes the key. was,
// when set, is kept in a comment after the value: in [keys], the binding
// to restore when the user turns a disabled action back on.
type keysEdit struct {
	action, value, was string
	remove             bool
}

func (e keysEdit) line(indent string) string {
	s := indent + e.action + " = " + e.value
	if e.was != "" {
		s += "  # was " + e.was
	}
	return s
}

// tomlString writes s as a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// tomlStrings writes a TOML array of strings.
func tomlStrings(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = tomlString(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

func tomlFloat(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

// section is a run of lines: the top level (head -1), a [table] or one
// [[array]] entry. Its body runs from head+1 to end.
type section struct {
	name       string
	array      bool
	head, end  int
	firstChild int // index among the entries of the same array
}

func splitLines(content string) []string {
	lines := strings.SplitAfter(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func sections(lines []string) []section {
	secs := []section{{head: -1, end: len(lines)}}
	count := map[string]int{}
	for i, l := range lines {
		m := headerName.FindStringSubmatch(strings.TrimRight(l, "\r\n"))
		if m == nil {
			continue
		}
		secs[len(secs)-1].end = i
		s := section{name: m[2], array: m[1] == "[[", head: i, end: len(lines)}
		if s.array {
			s.firstChild = count[s.name]
			count[s.name]++
		}
		secs = append(secs, s)
	}
	return secs
}

// editSection replaces, removes and adds key lines in one section.
// Replaced lines keep their indentation and their own comment (unless it
// is a "# was" comment); new keys follow the section's last key line, or
// its header, or for the top level go before the first table and the
// comments attached to it.
func editSection(lines []string, sec section, edits []keysEdit) []string {
	pending := map[string]keysEdit{}
	var order []string
	for _, e := range edits {
		if _, ok := pending[e.action]; !ok {
			order = append(order, e.action)
		}
		pending[e.action] = e
	}
	start := sec.head + 1
	var out []string
	out = append(out, lines[:start]...)
	lastKey := -1 // index in out of the section's last key line
	indent := ""
	for i := start; i < sec.end; i++ {
		l := lines[i]
		t := strings.TrimRight(l, "\r\n")
		m := keyLine.FindStringSubmatch(t)
		if m == nil {
			out = append(out, l)
			continue
		}
		indent = m[1]
		// A multi-line array continues until its brackets balance.
		j := i
		for depth := bracketDepth(t[len(m[0]):]); depth > 0 && j+1 < sec.end; {
			j++
			depth += bracketDepth(strings.TrimRight(lines[j], "\r\n"))
		}
		e, ok := pending[m[3]]
		if !ok {
			out = append(out, lines[i:j+1]...)
			lastKey = len(out) - 1
			i = j
			continue
		}
		delete(pending, m[3])
		single := j == i
		i = j
		if e.remove {
			continue
		}
		nl := "\n"
		if strings.HasSuffix(lines[j], "\r\n") {
			nl = "\r\n"
		} else if !strings.HasSuffix(lines[j], "\n") {
			nl = ""
		}
		// Keep the user's comment on the line, spaced as it was, unless it
		// is ours.
		line := e.line(indent)
		if c := commentWithSpace(t[len(m[0]):]); e.was == "" && single && c != "" && !wasComment.MatchString(c) {
			if c[0] == '#' {
				c = " " + c
			}
			line += c
		}
		out = append(out, line+nl)
		lastKey = len(out) - 1
	}
	var add []string
	for _, a := range order {
		if e, ok := pending[a]; ok && !e.remove {
			add = append(add, e.line(indent)+"\n")
		}
	}
	if len(add) > 0 {
		at := lastKey + 1
		switch {
		case lastKey >= 0:
		case sec.head >= 0:
			at = sec.head + 1
		default:
			// The top level without keys: before the first table, above
			// the comments attached to its header, then a blank line.
			at = len(out)
			for at > 0 && strings.HasPrefix(strings.TrimSpace(out[at-1]), "#") {
				at--
			}
			if at < len(out) || sec.end < len(lines) {
				add = append(add, "\n")
			}
		}
		if at > 0 && !strings.HasSuffix(out[at-1], "\n") {
			out[at-1] += "\n"
		}
		out = append(out[:at], append(add, out[at:]...)...)
	}
	return append(out, lines[sec.end:]...)
}

// patchTable edits a [table] ("" for the top level). A missing table is
// added at the end.
func patchTable(content, table string, edits []keysEdit) string {
	if len(edits) == 0 {
		return content
	}
	lines := splitLines(content)
	for _, sec := range sections(lines) {
		if sec.name == table && !sec.array {
			return strings.Join(editSection(lines, sec, edits), "")
		}
	}
	var b strings.Builder
	b.WriteString(content)
	if content != "" && !strings.HasSuffix(content, "\n") {
		b.WriteString("\n")
	}
	if content != "" {
		b.WriteString("\n")
	}
	b.WriteString("[" + table + "]\n")
	for _, e := range edits {
		if !e.remove {
			b.WriteString(e.line("") + "\n")
		}
	}
	return b.String()
}

// patchKeys applies edits to the [keys] table.
func patchKeys(content string, edits []keysEdit) string { return patchTable(content, "keys", edits) }

func arrayEntry(secs []section, array string, index int) (section, bool) {
	for _, s := range secs {
		if s.array && s.name == array && s.firstChild == index {
			return s, true
		}
	}
	return section{}, false
}

// patchArrayEntry edits the index-th [[array]] entry.
func patchArrayEntry(content, array string, index int, edits []keysEdit) string {
	lines := splitLines(content)
	sec, ok := arrayEntry(sections(lines), array, index)
	if !ok || len(edits) == 0 {
		return content
	}
	return strings.Join(editSection(lines, sec, edits), "")
}

// appendArrayEntry adds an [[array]] entry at the end of the file, after
// one blank line.
func appendArrayEntry(content, array string, edits []keysEdit) string {
	var b strings.Builder
	b.WriteString(content)
	if content != "" && !strings.HasSuffix(content, "\n") {
		b.WriteString("\n")
	}
	if content != "" && !strings.HasSuffix(content, "\n\n") {
		b.WriteString("\n")
	}
	b.WriteString("[[" + array + "]]\n")
	for _, e := range edits {
		if !e.remove {
			b.WriteString(e.line("") + "\n")
		}
	}
	return b.String()
}

// removeArrayEntry deletes the index-th [[array]] entry: its header, body
// and trailing blank lines, but not the comments attached to the next
// header. At the end of the file the blank line above it goes too.
func removeArrayEntry(content, array string, index int) string {
	lines := splitLines(content)
	sec, ok := arrayEntry(sections(lines), array, index)
	if !ok {
		return content
	}
	end := sec.end
	if end < len(lines) {
		for end > sec.head+1 && strings.HasPrefix(strings.TrimSpace(lines[end-1]), "#") {
			end--
		}
	}
	blank := func(i int) bool { return i >= 0 && i < len(lines) && strings.TrimSpace(lines[i]) == "" }
	start := sec.head
	// The entry's trailing blank lines go with it; so does the blank line
	// above it when nothing follows.
	if blank(start-1) && end == len(lines) {
		start--
	}
	return strings.Join(append(lines[:start:start], lines[end:]...), "")
}

// bracketDepth counts [ minus ] outside strings and comments.
func bracketDepth(s string) int {
	depth := 0
	var quote rune
	for i, r := range s {
		switch {
		case quote != 0:
			if r == quote && (quote == '\'' || i == 0 || s[i-1] != '\\') {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == '#':
			return depth
		case r == '[':
			depth++
		case r == ']':
			depth--
		}
	}
	return depth
}

// trailingComment returns the "# …" part of a value, outside strings.
func trailingComment(s string) string {
	if i := commentStart(s); i >= 0 {
		return strings.TrimSpace(s[i:])
	}
	return ""
}

// commentWithSpace returns the comment of a value with the blanks before
// it, as written; "" without one.
func commentWithSpace(s string) string {
	i := commentStart(s)
	if i < 0 {
		return ""
	}
	j := i
	for j > 0 && (s[j-1] == ' ' || s[j-1] == '\t') {
		j--
	}
	return strings.TrimRight(s[j:], " \t")
}

// commentStart is the index of the "#" that starts a value's comment,
// outside strings, or -1.
func commentStart(s string) int {
	var quote rune
	for i, r := range s {
		switch {
		case quote != 0:
			if r == quote && (quote == '\'' || i == 0 || s[i-1] != '\\') {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == '#':
			return i
		}
	}
	return -1
}

// keysWas reads the "# was …" comments of disabled actions in [keys]: the
// bindings to restore when they are turned back on.
func keysWas(content string) map[string][]string {
	was := map[string][]string{}
	in := false
	for _, l := range strings.Split(content, "\n") {
		t := strings.TrimRight(l, "\r")
		switch {
		case keysHeader.MatchString(t):
			in = true
			continue
		case tableHeader.MatchString(t):
			in = false
			continue
		}
		m := keyLine.FindStringSubmatch(t)
		if !in || m == nil {
			continue
		}
		c := wasComment.FindStringSubmatch(trailingComment(t[len(m[0]):]))
		if c == nil {
			continue
		}
		var v struct{ X any }
		if _, err := toml.Decode("X = "+c[1], &v); err != nil {
			continue
		}
		if vals, err := keyValues(v.X); err == nil && len(vals) > 0 {
			was[m[3]] = vals
		}
	}
	return was
}
