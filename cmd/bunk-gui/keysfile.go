package main

// A line-based editor for the [keys] table of config.toml, so the shortcuts
// window can save bindings and leave everything else (comments, order,
// blank lines, other tables) byte for byte as the user wrote it.

import (
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

var (
	tableHeader = regexp.MustCompile(`^\s*\[`)
	keysHeader  = regexp.MustCompile(`^\s*\[\s*keys\s*\]\s*(#.*)?$`)
	keyLine     = regexp.MustCompile(`^(\s*)("?)([A-Za-z0-9_-]+)("?)\s*=`)
	wasComment  = regexp.MustCompile(`#\s*was\s+(.+?)\s*$`)
)

// keysEdit is one change to the [keys] table: action = value, where value
// is TOML (e.g. "\"ctrl+j\"" or "[\"f1\", \"f2\"]"). was, when set, is
// kept in a comment after the value: the binding to restore when the user
// turns a disabled action back on.
type keysEdit struct {
	action, value, was string
}

func (e keysEdit) line(indent string) string {
	s := indent + e.action + " = " + e.value
	if e.was != "" {
		s += "  # was " + e.was
	}
	return s
}

// patchKeys applies edits to a config file's text. Lines of the [keys]
// table that set an edited action are replaced (with the rest of a
// multi-line array); missing actions are added after the table's last
// key line; a missing table is added at the end. Nothing else changes.
func patchKeys(content string, edits []keysEdit) string {
	if len(edits) == 0 {
		return content
	}
	lines := strings.SplitAfter(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	start, end := -1, len(lines) // [keys] header line, and the next table's
	for i, l := range lines {
		t := strings.TrimRight(l, "\r\n")
		if start < 0 && keysHeader.MatchString(t) {
			start = i
			continue
		}
		if start >= 0 && tableHeader.MatchString(t) {
			end = i
			break
		}
	}
	pending := map[string]keysEdit{}
	var order []string
	for _, e := range edits {
		if _, ok := pending[e.action]; !ok {
			order = append(order, e.action)
		}
		pending[e.action] = e
	}

	if start < 0 {
		var b strings.Builder
		b.WriteString(content)
		if content != "" && !strings.HasSuffix(content, "\n") {
			b.WriteString("\n")
		}
		if content != "" {
			b.WriteString("\n")
		}
		b.WriteString("[keys]\n")
		for _, a := range order {
			b.WriteString(pending[a].line("") + "\n")
		}
		return b.String()
	}

	var out []string
	out = append(out, lines[:start+1]...)
	lastKey := start // index in out of the last key line of the table
	indent := ""
	for i := start + 1; i < end; i++ {
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
		for depth := bracketDepth(t[len(m[0]):]); depth > 0 && j+1 < end; {
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
		nl := "\n"
		if strings.HasSuffix(lines[j], "\r\n") {
			nl = "\r\n"
		} else if !strings.HasSuffix(lines[j], "\n") {
			nl = ""
		}
		// Keep the user's comment on the line unless it is ours.
		if e.was == "" {
			if c := trailingComment(t[len(m[0]):]); c != "" && !wasComment.MatchString(c) && j == i {
				out = append(out, e.line(indent)+"  "+c+nl)
				lastKey = len(out) - 1
				i = j
				continue
			}
		}
		out = append(out, e.line(indent)+nl)
		lastKey = len(out) - 1
		i = j
	}
	if len(pending) > 0 {
		var add []string
		for _, a := range order {
			if e, ok := pending[a]; ok {
				add = append(add, e.line(indent)+"\n")
			}
		}
		if !strings.HasSuffix(out[lastKey], "\n") {
			out[lastKey] += "\n"
		}
		out = append(out[:lastKey+1], append(add, out[lastKey+1:]...)...)
	}
	out = append(out, lines[end:]...)
	return strings.Join(out, "")
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
			return strings.TrimSpace(s[i:])
		}
	}
	return ""
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
