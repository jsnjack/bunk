// Package ptychild is a child program for tests of what front ends write to
// a session's PTY. A test binary runs itself as the child in a session
// when Env is set (Requested, Main):
//
//   - It puts its terminal in raw mode without echo, so the kernel buffers
//     its input until the buffer is full and writes to the PTY block.
//   - It prints Env numbered DECRQM queries (Query), then FloodDone.
//   - It reads none of its input until the file named by GoEnv exists;
//     without GoEnv it never reads.
//   - From then on it copies its input to the file named by OutEnv.
package ptychild

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"time"
)

const (
	// Env holds the number of queries to print, at most MaxQueries. A test
	// binary that finds it set runs Main instead of its tests.
	Env = "BUNK_PTYCHILD"
	// GoEnv names the file whose existence lets the child read its input.
	GoEnv = "BUNK_PTYCHILD_GO"
	// OutEnv names the file the child copies its input to.
	OutEnv = "BUNK_PTYCHILD_OUT"
	// FloodDone is printed after the queries, followed by their number.
	FloodDone = "flood-done"
	// MaxQueries is the largest mode number a DECRQM query may carry.
	MaxQueries = 65535
)

// Requested reports whether this process runs as the child.
func Requested() bool { return os.Getenv(Env) != "" }

// Environ returns the environment entries that run a test binary as the
// child: queries numbered queries, the go and out files ("" leaves them
// out), and no one-second sleep of the race detector at exit.
func Environ(queries int, goFile, outFile string) []string {
	env := []string{Env + "=" + strconv.Itoa(queries), "GORACE=" + os.Getenv("GORACE") + " atexit_sleep_ms=0"}
	if goFile != "" {
		env = append(env, GoEnv+"="+goFile)
	}
	if outFile != "" {
		env = append(env, OutEnv+"="+outFile)
	}
	return env
}

// Query is the n-th query: DECRQM for private mode n.
func Query(n int) string { return "\x1b[?" + strconv.Itoa(n) + "$p" }

// ParseReplies reads the replies to Query from the start of b. It returns
// their mode numbers in order and the bytes after the last whole reply.
func ParseReplies(b []byte) (modes []int, rest []byte) {
	for {
		// A reply: ESC [ ? digits ; status $ y.
		if len(b) < 4 || b[0] != 0x1b || b[1] != '[' || b[2] != '?' {
			return modes, b
		}
		i, n := 3, 0
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			n = n*10 + int(b[i]-'0')
			i++
		}
		if i == 3 || i+4 > len(b) || b[i] != ';' || b[i+2] != '$' || b[i+3] != 'y' {
			return modes, b
		}
		modes = append(modes, n)
		b = b[i+4:]
	}
}

// Main runs the child and returns its exit status.
func Main() int {
	n, err := strconv.Atoi(os.Getenv(Env))
	if err != nil || n < 0 || n > MaxQueries {
		fmt.Printf("ptychild: %s=%q: want 0 to %d\n", Env, os.Getenv(Env), MaxQueries)
		return 2
	}
	stty := exec.Command("stty", "raw", "-echo")
	stty.Stdin = os.Stdin
	if out, err := stty.CombinedOutput(); err != nil {
		fmt.Printf("ptychild: stty: %v: %s\n", err, out)
		return 1
	}
	w := bufio.NewWriterSize(os.Stdout, 64<<10)
	for i := 1; i <= n; i++ {
		if _, err := w.WriteString(Query(i)); err != nil {
			return 1
		}
	}
	if _, err := fmt.Fprintf(w, "%s %d\r\n", FloodDone, n); err != nil {
		return 1
	}
	if err := w.Flush(); err != nil {
		return 1
	}
	goFile := os.Getenv(GoEnv)
	if goFile == "" {
		for {
			time.Sleep(time.Hour) // never read; SIGHUP ends the child
		}
	}
	for {
		if _, err := os.Stat(goFile); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	out, err := os.Create(os.Getenv(OutEnv))
	if err != nil {
		fmt.Printf("ptychild: %v\r\n", err)
		return 1
	}
	_, err = io.Copy(out, os.Stdin) // until the session closes the PTY
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 1
	}
	return 0
}
