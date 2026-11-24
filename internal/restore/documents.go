package restore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"
)

// The documents bunk-gui's panels showed, across its windows and after
// they close: documents.json beside the windows' directory, newest first.
// Every window records the documents it lists. The Documents page shows
// the list, a conversation that resumes gets its documents back, and an
// agent's SessionStart hook tells it which documents it presented.

// Doc is a document a panel showed.
type Doc struct {
	Path string `json:"path"`
	// Where is the repository, else the folder, of the pane that showed
	// it; the Documents page groups by it.
	Where string `json:"where,omitempty"`
	// Agent and Session are the agent that presented it and its
	// conversation (session.Status.AgentSession); "" when a person opened
	// it or the agent reported no session.
	Agent   string    `json:"agent,omitempty"`
	Session string    `json:"session,omitempty"`
	Shown   time.Time `json:"shown"`
}

// MaxDocs is how many documents the list keeps; the oldest go first.
const MaxDocs = 200

type docList struct {
	Docs []Doc `json:"docs"`
}

// DocumentsPath is $XDG_STATE_HOME/bunk-gui/documents.json, with
// XDG_STATE_HOME defaulting to ~/.local/state; "" when neither is known.
func DocumentsPath(getenv func(string) string) string {
	dir := DefaultDir(getenv)
	if dir == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(dir), "documents.json")
}

// LoadDocs reads the list at path, newest first. A missing or damaged
// file is an empty list: it only helps find documents again.
func LoadDocs(path string) []Doc {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var l docList
	if err := json.Unmarshal(data, &l); err != nil {
		return nil
	}
	return l.Docs
}

// RecordDoc adds d to the list at path, or updates the entry of the same
// file: where d leaves Where, or Agent and Session, empty, the entry keeps
// what it had, so a person reopening a document keeps the conversation
// that presented it. With keep, a file the list has already stays as it
// is, for documents a restored window lists again. The list stays sorted
// newest first and keeps MaxDocs. Windows record under an exclusive lock
// on documents.lock, and the file is replaced whole, so readers need no
// lock.
func RecordDoc(path string, d Doc, keep bool) error {
	if path == "" || d.Path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	lock, err := os.OpenFile(filepath.Join(dir, "documents.lock"), os.O_RDWR|os.O_CREATE|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	defer func() { _ = lock.Close() }() // and with it the lock
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("restore: lock documents: %w", err)
	}
	docs := LoadDocs(path)
	if i := slices.IndexFunc(docs, func(o Doc) bool { return o.Path == d.Path }); i >= 0 {
		if keep {
			return nil
		}
		old := docs[i]
		docs = slices.Delete(docs, i, i+1)
		if d.Where == "" {
			d.Where = old.Where
		}
		if d.Agent == "" && d.Session == "" {
			d.Agent, d.Session = old.Agent, old.Session
		}
	}
	docs = append(docs, d)
	// Newest first; two windows' records can arrive out of order.
	slices.SortStableFunc(docs, func(a, b Doc) int { return b.Shown.Compare(a.Shown) })
	if len(docs) > MaxDocs {
		docs = docs[:MaxDocs]
	}
	data, err := json.MarshalIndent(docList{Docs: docs}, "", "  ")
	if err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	return writeFile(path, data)
}

// SessionDocs are the documents of the list that agent presented in its
// conversation session, newest first.
func SessionDocs(docs []Doc, agent, session string) []Doc {
	if session == "" {
		return nil
	}
	var out []Doc
	for _, d := range docs {
		if d.Session == session && (agent == "" || d.Agent == "" || d.Agent == agent) {
			out = append(out, d)
		}
	}
	return out
}
