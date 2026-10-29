package notify

// merge.go - installing bunk's hooks into a settings file that holds other
// settings and hooks too: Claude Code's and Gemini CLI's settings.json,
// and a Codex hooks.json with hooks of the user's own.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// BackupSuffix names the copy of a settings file InstallHooks keeps before
// it changes it.
const BackupSuffix = ".bunk-backup"

// rawField is one key of a JSON object as written.
type rawField struct {
	key   string
	value json.RawMessage
}

// parseObject reads a JSON object with its keys in their order.
func parseObject(data []byte) ([]rawField, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, errors.New("not a JSON object")
	}
	var out []rawField
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("bad JSON: %w", err)
		}
		key, _ := tok.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, fmt.Errorf("bad JSON: %w", err)
		}
		out = append(out, rawField{key, v})
	}
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("bad JSON: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("bad JSON: text after the object")
	}
	return out, nil
}

// toObject turns fields into an object that marshals in their order.
func toObject(fields []rawField) object {
	o := make(object, len(fields))
	for i, f := range fields {
		o[i] = field{f.key, f.value}
	}
	return o
}

func setField(fields []rawField, key string, v json.RawMessage) []rawField {
	for i := range fields {
		if fields[i].key == key {
			fields[i].value = v
			return fields
		}
	}
	return append(fields, rawField{key, v})
}

func getField(fields []rawField, key string) (json.RawMessage, bool) {
	for _, f := range fields {
		if f.key == key {
			return f.value, true
		}
	}
	return nil, false
}

// mergeSettingsHooks returns data, a settings file in the settings form
// (event → matcher groups → handlers under "hooks"), with every handler
// that runs bunk's notify CLI replaced by the hooks of snippet. Other
// keys, events, groups and handlers stay, in their order; the result is
// indented as data was.
func mergeSettingsHooks(data []byte, snippet string) ([]byte, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		data = []byte("{}")
	}
	top, err := parseObject(data)
	if err != nil {
		return nil, err
	}
	var events []rawField
	if raw, ok := getField(top, "hooks"); ok && string(bytes.TrimSpace(raw)) != "null" {
		if events, err = parseObject(raw); err != nil {
			return nil, fmt.Errorf(`"hooks": %w`, err)
		}
	}
	var kept []rawField
	for _, ev := range events {
		var groups []json.RawMessage
		if err := json.Unmarshal(ev.value, &groups); err != nil {
			return nil, fmt.Errorf(`"hooks"."%s" is not a list`, ev.key)
		}
		groups, err := dropBunkHandlers(groups)
		if err != nil {
			return nil, fmt.Errorf(`"hooks"."%s": %w`, ev.key, err)
		}
		if len(groups) > 0 {
			v, err := json.Marshal(groups)
			if err != nil {
				return nil, fmt.Errorf("merge hooks: %w", err)
			}
			kept = append(kept, rawField{ev.key, v})
		}
	}
	snip, err := parseObject([]byte(snippet))
	if err != nil {
		return nil, fmt.Errorf("snippet: %w", err)
	}
	raw, _ := getField(snip, "hooks")
	ours, err := parseObject(raw)
	if err != nil {
		return nil, fmt.Errorf("snippet: %w", err)
	}
	for _, ev := range ours {
		var add []json.RawMessage
		if err := json.Unmarshal(ev.value, &add); err != nil {
			return nil, fmt.Errorf("snippet: %w", err)
		}
		var have []json.RawMessage
		if v, ok := getField(kept, ev.key); ok {
			if err := json.Unmarshal(v, &have); err != nil {
				return nil, fmt.Errorf("merge hooks: %w", err)
			}
		}
		v, err := json.Marshal(append(have, add...))
		if err != nil {
			return nil, fmt.Errorf("merge hooks: %w", err)
		}
		kept = setField(kept, ev.key, v)
	}
	hooks, err := json.Marshal(toObject(kept))
	if err != nil {
		return nil, fmt.Errorf("merge hooks: %w", err)
	}
	compact, err := json.Marshal(toObject(setField(top, "hooks", hooks)))
	if err != nil {
		return nil, fmt.Errorf("merge hooks: %w", err)
	}
	var out bytes.Buffer
	if err := json.Indent(&out, compact, "", indentOf(data)); err != nil {
		return nil, fmt.Errorf("merge hooks: %w", err)
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// dropBunkHandlers removes the handlers that run bunk's notify CLI from
// matcher groups, and groups left without a handler. A group that is not
// an object, or whose "hooks" is not a list, stays as it is.
func dropBunkHandlers(groups []json.RawMessage) ([]json.RawMessage, error) {
	var out []json.RawMessage
	for _, g := range groups {
		fields, err := parseObject(g)
		if err != nil {
			out = append(out, g)
			continue
		}
		raw, ok := getField(fields, "hooks")
		var handlers []json.RawMessage
		if !ok || json.Unmarshal(raw, &handlers) != nil {
			out = append(out, g)
			continue
		}
		var keep []json.RawMessage
		for _, h := range handlers {
			var c struct {
				Command string `json:"command"`
			}
			if json.Unmarshal(h, &c) == nil && bunkCommand(c.Command) {
				continue
			}
			keep = append(keep, h)
		}
		switch {
		case len(keep) == len(handlers):
			out = append(out, g)
		case len(keep) > 0:
			v, err := json.Marshal(keep)
			if err != nil {
				return nil, fmt.Errorf("merge hooks: %w", err)
			}
			v, err = json.Marshal(toObject(setField(fields, "hooks", v)))
			if err != nil {
				return nil, fmt.Errorf("merge hooks: %w", err)
			}
			out = append(out, v)
		}
	}
	return out, nil
}

// indentOf returns the indentation of data's first indented line: a tab
// or the spaces before it, else two spaces.
func indentOf(data []byte) string {
	for line := range bytes.Lines(data) {
		trimmed := bytes.TrimLeft(line, " \t")
		if n := len(line) - len(trimmed); n > 0 && len(bytes.TrimSpace(trimmed)) > 0 {
			if line[0] == '\t' {
				return "\t"
			}
			return string(line[:n])
		}
	}
	return "  "
}

// mergeInto merges snippet's hooks into the settings file at path, which
// may be missing, and returns the backup of its old contents, "" when
// there was nothing to keep. A symbolic link stays: its target changes.
func mergeInto(path, snippet string) (backup string, err error) {
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	}
	old, err := readFile(path)
	if err != nil {
		return "", err
	}
	merged, err := mergeSettingsHooks(old, snippet)
	if err != nil {
		return "", err
	}
	if bytes.Equal(merged, old) {
		return "", nil
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	if old != nil {
		backup = path + BackupSuffix
		if err := writeAtomic(backup, old, mode); err != nil {
			return "", err
		}
	}
	return backup, writeAtomic(path, merged, mode)
}

// writeAtomic replaces path with data through a temporary file in its
// directory.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".bunk-hooks-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr, os.Chmod(tmp.Name(), mode)); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
