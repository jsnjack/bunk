package restore

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// ErrClaimed is the error of Claim for a slot another window holds.
var ErrClaimed = errors.New("restore: the window is open elsewhere")

// Store is a directory of window slots.
type Store struct{ dir string }

// DefaultDir is $XDG_STATE_HOME/bunk-gui/windows, with XDG_STATE_HOME
// defaulting to ~/.local/state; "" when neither is known.
func DefaultDir(getenv func(string) string) string {
	base := getenv("XDG_STATE_HOME")
	if base == "" || !filepath.IsAbs(base) {
		home := getenv("HOME")
		if home == "" {
			return ""
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "bunk-gui", "windows")
}

// Open returns the store in dir, creating the directory (mode 0700: the
// scrollback may hold secrets).
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("restore: %w", err)
	}
	return &Store{dir: dir}, nil
}

// Dir returns the store's directory.
func (s *Store) Dir() string { return s.dir }

// Slot is a window's place in the store, held while the window runs.
type Slot struct {
	store *Store
	id    string
	lock  *os.File
}

// ID names the slot; Claim takes it.
func (sl *Slot) ID() string { return sl.id }

// New holds a new, empty slot.
func (s *Store) New() (*Slot, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, fmt.Errorf("restore: new slot: %w", err)
	}
	return s.Claim(hex.EncodeToString(b[:]))
}

// validID reports whether id may name a slot: hex digits only, so it
// never leaves the store's directory.
func validID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Claim holds the slot id until Release or Remove. It fails with
// ErrClaimed while another window holds it.
func (s *Store) Claim(id string) (*Slot, error) {
	if !validID(id) {
		return nil, fmt.Errorf("restore: bad window id %q", id)
	}
	f, err := os.OpenFile(filepath.Join(s.dir, id+".lock"), os.O_RDWR|os.O_CREATE|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("restore: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrClaimed
		}
		return nil, fmt.Errorf("restore: lock %s: %w", id, err)
	}
	return &Slot{store: s, id: id, lock: f}, nil
}

// held reports whether a running window holds the slot id.
func (s *Store) held(id string) bool {
	sl, err := s.Claim(id)
	if err != nil {
		return errors.Is(err, ErrClaimed)
	}
	sl.Release()
	return false
}

// Saved is a snapshot no window holds.
type Saved struct {
	ID     string
	Window Window
}

// Saved lists the snapshots that no running window holds, newest first.
// Unreadable snapshots and those of another Version are left out.
func (s *Store) Saved() []Saved {
	var out []Saved
	ids := s.ids()
	for _, id := range s.lockIDs() {
		// A window that ended before its first save left only its lock.
		if !slices.Contains(ids, id) {
			if sl, err := s.Claim(id); err == nil {
				_ = sl.Remove()
			}
		}
	}
	for _, id := range ids {
		if s.held(id) {
			continue
		}
		w, err := s.load(id)
		if err != nil || len(w.Tabs) == 0 {
			continue
		}
		out = append(out, Saved{ID: id, Window: w})
	}
	slices.SortStableFunc(out, func(a, b Saved) int { return b.Window.Saved.Compare(a.Window.Saved) })
	return out
}

// Running returns how many slots other than own running windows hold.
func (s *Store) Running(own *Slot) int {
	n := 0
	for _, id := range s.lockIDs() {
		if (own == nil || id != own.id) && s.held(id) {
			n++
		}
	}
	return n
}

func (s *Store) ids() []string { return s.list(".json") }

func (s *Store) lockIDs() []string { return s.list(".lock") }

func (s *Store) list(ext string) []string {
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	var ids []string
	for _, e := range ents {
		if id, ok := strings.CutSuffix(e.Name(), ext); ok && validID(id) {
			ids = append(ids, id)
		}
	}
	return ids
}

func (s *Store) load(id string) (Window, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, id+".json"))
	if err != nil {
		return Window{}, fmt.Errorf("restore: %w", err)
	}
	var w Window
	if err := json.Unmarshal(data, &w); err != nil {
		return Window{}, fmt.Errorf("restore: %s: %w", id, err)
	}
	if w.V != Version {
		return Window{}, fmt.Errorf("restore: %s: version %d", id, w.V)
	}
	// A tab whose tree is broken would build no panes.
	w.Tabs = slices.DeleteFunc(w.Tabs, func(t Tab) bool { return !t.Root.Valid() })
	if w.Selected < 0 || w.Selected >= len(w.Tabs) {
		w.Selected = 0
	}
	return w, nil
}

// Discard removes the snapshot id that no window holds.
func (s *Store) Discard(id string) error {
	sl, err := s.Claim(id)
	if err != nil {
		return err
	}
	return sl.Remove()
}

// Load reads the slot's snapshot.
func (sl *Slot) Load() (Window, error) { return sl.store.load(sl.id) }

// Save writes w as the slot's snapshot, stamped now. It replaces the file
// whole, so a crash leaves the old one or the new one.
func (sl *Slot) Save(w Window) error {
	w.V, w.Saved = Version, time.Now()
	data, err := json.MarshalIndent(w, "", "\t")
	if err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	return writeFile(filepath.Join(sl.store.dir, sl.id+".json"), data)
}

// historyDir holds the slot's scrollback files.
func (sl *Slot) historyDir() string { return filepath.Join(sl.store.dir, sl.id) }

// validName reports whether name may name a history file: no path parts.
func validName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\`) && len(name) <= 64
}

// SaveHistory writes a pane's scrollback under name.
func (sl *Slot) SaveHistory(name string, data []byte) error {
	if !validName(name) {
		return fmt.Errorf("restore: bad history name %q", name)
	}
	if err := os.MkdirAll(sl.historyDir(), 0o700); err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	return writeFile(filepath.Join(sl.historyDir(), name), data)
}

// History reads the scrollback saved under name.
func (sl *Slot) History(name string) ([]byte, error) {
	if !validName(name) {
		return nil, fmt.Errorf("restore: bad history name %q", name)
	}
	data, err := os.ReadFile(filepath.Join(sl.historyDir(), name))
	if err != nil {
		return nil, fmt.Errorf("restore: %w", err)
	}
	return data, nil
}

// KeepHistory removes the slot's history files whose names keep lacks.
func (sl *Slot) KeepHistory(keep map[string]bool) {
	ents, err := os.ReadDir(sl.historyDir())
	if err != nil {
		return
	}
	for _, e := range ents {
		if !keep[e.Name()] {
			_ = os.Remove(filepath.Join(sl.historyDir(), e.Name()))
		}
	}
}

// Release lets go of the slot and keeps its files, for a later start.
func (sl *Slot) Release() {
	if sl.lock != nil {
		_ = sl.lock.Close() // closing the descriptor drops the lock
		sl.lock = nil
	}
}

// Remove deletes the slot's files and lets go of it.
func (sl *Slot) Remove() error {
	err := errors.Join(
		ignoreMissing(os.RemoveAll(sl.historyDir())),
		ignoreMissing(os.Remove(filepath.Join(sl.store.dir, sl.id+".json"))),
		ignoreMissing(os.Remove(filepath.Join(sl.store.dir, sl.id+".lock"))),
	)
	sl.Release()
	if err != nil {
		return fmt.Errorf("restore: remove %s: %w", sl.id, err)
	}
	return nil
}

func ignoreMissing(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// writeFile replaces path with data through a temporary file and a rename.
func writeFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("restore: write %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("restore: %w", err)
	}
	return nil
}
