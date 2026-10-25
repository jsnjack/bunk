// Package leakcheck measures what a test process holds: open file
// descriptors, goroutines, child processes and live heap. Leak and soak
// tests take a Snapshot before a workload and call Check after it, which
// waits for the counts to drop back and reports anything left over.
//
// It reads /proc, so it works on Linux only; Supported reports that.
package leakcheck

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"time"
)

// TB is the part of testing.TB that Check uses.
type TB interface {
	Helper()
	Errorf(format string, args ...any)
}

// Soak reports whether the long soak versions run: SOAK=1 in the
// environment (make soak).
func Soak() bool { return os.Getenv("SOAK") == "1" }

// Supported reports whether this system has the /proc files Take reads.
func Supported() bool {
	_, err := os.Stat("/proc/self/fd")
	return err == nil
}

// Snapshot is what the process holds at one moment.
type Snapshot struct {
	FDs        map[int]string // open descriptor → its target, as /proc shows it
	Goroutines int
	Heap       uint64       // live heap bytes after a full GC
	Children   map[int]Proc // child processes by PID
}

// Proc is one child process.
type Proc struct {
	State byte   // as in /proc/<pid>/stat: 'R', 'S', 'Z' (zombie), …
	Comm  string // process name
}

// Take returns the current counts. It runs a full GC first, so Heap holds
// live objects only.
func Take() Snapshot {
	return Snapshot{FDs: FDs(), Goroutines: runtime.NumGoroutine(), Heap: LiveHeap(), Children: Children()}
}

// LiveHeap runs two GCs (the second frees what finalizers released) and
// returns the bytes of live heap objects.
func LiveHeap() uint64 {
	runtime.GC()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc
}

// FDs lists the open file descriptors of this process and their targets.
// The descriptor FDs itself reads the directory with is left out.
func FDs() map[int]string {
	dir := "/proc/self/fd"
	f, err := os.Open(dir)
	if err != nil {
		return nil
	}
	names, err := f.Readdirnames(-1)
	self := int(f.Fd())
	if cerr := f.Close(); cerr != nil && err == nil {
		err = cerr
	}
	if err != nil {
		return nil
	}
	fds := make(map[int]string, len(names))
	for _, name := range names {
		fd, err := strconv.Atoi(name)
		if err != nil || fd == self {
			continue
		}
		target, err := os.Readlink(filepath.Join(dir, name))
		if err != nil {
			continue // closed while we looked
		}
		fds[fd] = target
	}
	return fds
}

// Children lists the processes whose parent is this process.
func Children() map[int]Proc {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	self := os.Getpid()
	kids := map[int]Proc{}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue // exited while we looked
		}
		ppid, p, ok := parseStat(data)
		if ok && ppid == self {
			kids[pid] = p
		}
	}
	return kids
}

// parseStat reads the state and parent PID from /proc/<pid>/stat. The
// name is in parentheses and may hold spaces and parentheses itself.
func parseStat(data []byte) (ppid int, p Proc, ok bool) {
	open := bytes.IndexByte(data, '(')
	end := bytes.LastIndexByte(data, ')')
	if open < 0 || end < open {
		return 0, Proc{}, false
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 2 || len(fields[0]) != 1 {
		return 0, Proc{}, false
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, Proc{}, false
	}
	return ppid, Proc{State: fields[0][0], Comm: string(data[open+1 : end])}, true
}

// Limits says how far a later Snapshot may exceed its baseline.
type Limits struct {
	FDs        int           // extra open descriptors
	Goroutines int           // extra goroutines
	Heap       uint64        // extra live heap bytes
	Settle     time.Duration // how long Check waits for the counts to drop
}

// Problems compares now with base and describes each excess: descriptors,
// goroutines and heap over the limits, and child processes that were not
// there in base (a zombie is a child nobody waited for).
func Problems(base, now Snapshot, lim Limits) []string {
	var out []string
	if extra := len(now.FDs) - len(base.FDs); extra > lim.FDs {
		var leaked []string
		for fd, target := range now.FDs {
			if base.FDs[fd] != target {
				leaked = append(leaked, fmt.Sprintf("%d→%s", fd, target))
			}
		}
		slices.Sort(leaked)
		if len(leaked) > 20 {
			leaked = append(leaked[:20], "…")
		}
		out = append(out, fmt.Sprintf("%d file descriptors open, %d at the baseline (limit +%d); new: %s",
			len(now.FDs), len(base.FDs), lim.FDs, strings.Join(leaked, ", ")))
	}
	if extra := now.Goroutines - base.Goroutines; extra > lim.Goroutines {
		out = append(out, fmt.Sprintf("%d goroutines, %d at the baseline (limit +%d)",
			now.Goroutines, base.Goroutines, lim.Goroutines))
	}
	if now.Heap > base.Heap+lim.Heap {
		out = append(out, fmt.Sprintf("live heap %s, %s at the baseline (limit +%s)",
			MiB(now.Heap), MiB(base.Heap), MiB(lim.Heap)))
	}
	var zombies, running []string
	for pid, p := range now.Children {
		if _, old := base.Children[pid]; old {
			continue
		}
		desc := fmt.Sprintf("%d (%s)", pid, p.Comm)
		if p.State == 'Z' {
			zombies = append(zombies, desc)
		} else {
			running = append(running, desc)
		}
	}
	slices.Sort(zombies)
	slices.Sort(running)
	if len(zombies) > 0 {
		out = append(out, fmt.Sprintf("%d zombie children: %s", len(zombies), strings.Join(zombies, ", ")))
	}
	if len(running) > 0 {
		out = append(out, fmt.Sprintf("%d child processes still running: %s", len(running), strings.Join(running, ", ")))
	}
	return out
}

// Check waits up to lim.Settle for the process to come back within lim of
// base, then reports what is still over through tb.Errorf, with the
// goroutine stacks when goroutines leaked. It returns the last Snapshot.
func Check(tb TB, what string, base Snapshot, lim Limits) Snapshot {
	tb.Helper()
	deadline := time.Now().Add(lim.Settle)
	pause := 10 * time.Millisecond
	for {
		now := Take()
		problems := Problems(base, now, lim)
		if len(problems) == 0 {
			return now
		}
		if time.Now().After(deadline) {
			for _, p := range problems {
				tb.Errorf("%s: %s", what, p)
			}
			if now.Goroutines-base.Goroutines > lim.Goroutines {
				tb.Errorf("%s: goroutines now:\n%s", what, Stacks())
			}
			return now
		}
		time.Sleep(pause)
		pause = min(2*pause, 200*time.Millisecond)
	}
}

// Stacks returns the goroutine stacks, grouped by identical stack.
func Stacks() string {
	var b bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&b, 1); err != nil {
		return "goroutine profile: " + err.Error()
	}
	return b.String()
}

// MiB formats a byte count in MiB.
func MiB(n uint64) string { return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20)) }
