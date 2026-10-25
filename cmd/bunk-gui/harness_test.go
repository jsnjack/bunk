package main

// Test harness for window tests. Every window opens on a private headless
// GNOME compositor (scripts/headless-gui.sh), never on the desktop:
//
//   - TestMain re-runs the test binary under the wrapper unless BUNK_HEADLESS
//     is set already or -short is given.
//   - Window tests skip with -short (package builds have no compositor) and
//     fail when BUNK_HEADLESS is unset.
//   - bunk-gui is built once per test run (with -race when the tests are),
//     driven by a -do script per scenario, and checked through its -report
//     JSON and pixels of its PNG dumps.
//   - Every scenario gets a compositor of its own (the wrapper again, nested),
//     so scenarios that call t.Parallel run side by side without sharing
//     keyboard focus, pointer or clipboard. At most guiParallel run at once.

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const wrapper = "../../scripts/headless-gui.sh"

func TestMain(m *testing.M) {
	runHelper()    // a pane's helper program (helper_test.go), not a test run
	recordAllocs() // frame allocation tests count with mallocsUnder
	flag.Parse()
	if !testing.Short() && os.Getenv("BUNK_HEADLESS") == "" {
		if w, err := filepath.Abs(wrapper); err == nil {
			if _, err := os.Stat(w); err == nil {
				cmd := exec.Command(w, os.Args...)
				cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
				err := cmd.Run()
				var ee *exec.ExitError
				if errors.As(err, &ee) {
					os.Exit(ee.ExitCode())
				}
				if err != nil {
					fmt.Fprintln(os.Stderr, "headless-gui:", err)
					os.Exit(1)
				}
				os.Exit(0)
			}
		}
	}
	code := m.Run()
	if guiDir != "" {
		_ = os.RemoveAll(guiDir)
	}
	os.Exit(code)
}

func needWindow(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("opens a window; skipped with -short")
	}
	if os.Getenv("BUNK_HEADLESS") == "" {
		t.Fatal("window tests run only on the headless compositor: use scripts/headless-gui.sh (BUNK_HEADLESS unset)")
	}
}

var (
	guiOnce sync.Once
	guiDir  string
	guiBin  string
	guiErr  error
)

// guiBinary builds bunk-gui once for the test run.
func guiBinary(t *testing.T) string {
	t.Helper()
	guiOnce.Do(func() {
		guiDir, guiErr = os.MkdirTemp("", "bunk-gui-test-")
		if guiErr != nil {
			return
		}
		guiBin = filepath.Join(guiDir, "bunk-gui")
		args := []string{"build", "-o", guiBin}
		if raceEnabled {
			// gotk4's marshalling trips checkptr, which -race turns on.
			args = append(args, "-race", "-gcflags=all=-d=checkptr=0")
		}
		out, err := exec.Command("go", append(args, ".")...).CombinedOutput()
		if err != nil {
			guiErr = fmt.Errorf("go build: %v\n%s", err, out)
		}
	})
	if guiErr != nil {
		t.Fatal(guiErr)
	}
	return guiBin
}

// scenario is one bunk-gui launch.
type scenario struct {
	args   []string          // flags besides -do, -report and -config
	config string            // config.toml contents
	script []string          // "TIME ACTION" lines
	env    []string          // extra environment
	files  map[string]string // more files in the scenario's directory (the panes' cwd)
	limit  time.Duration
}

type guiRun struct {
	*runReport
	t   *testing.T
	dir string
}

// guiSlots limits how many scenarios run at once (BUNK_GUI_PARALLEL).
var guiSlots = func() chan struct{} {
	n := 4
	if v, err := strconv.Atoi(os.Getenv("BUNK_GUI_PARALLEL")); err == nil && v > 0 {
		n = v
	}
	return make(chan struct{}, n)
}()

// runGUI launches bunk-gui with the scenario on a compositor of its own and
// returns its report. The child shell is /bin/sh with a plain prompt and
// no history file, so pane text does not depend on the user's shell setup.
func runGUI(t *testing.T, sc scenario) *guiRun {
	t.Helper()
	needWindow(t)
	bin := guiBinary(t)
	w, err := filepath.Abs(wrapper)
	must(t, err)
	guiSlots <- struct{}{}
	defer func() { <-guiSlots }()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	rep := filepath.Join(dir, "report.json")
	do := filepath.Join(dir, "script.do")
	must(t, os.WriteFile(cfg, []byte(sc.config), 0o600))
	must(t, os.WriteFile(do, []byte(strings.Join(sc.script, "\n")+"\n"), 0o600))
	for name, text := range sc.files {
		must(t, os.WriteFile(filepath.Join(dir, name), []byte(text), 0o700))
	}
	limit := sc.limit
	if limit == 0 {
		limit = 30 * time.Second
	}
	args := append([]string{"-wait-focus", "-do", "@" + do, "-report", rep, "-config", cfg, "-timeout", limit.String()}, sc.args...)
	cmd := exec.Command(w, append([]string{bin}, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // a timeout kills the compositor too
	cmd.Dir = dir
	// An interactive sh reads $ENV; PS1 itself would not survive the
	// wrapper, which is a bash script.
	shrc := filepath.Join(dir, "shrc")
	must(t, os.WriteFile(shrc, []byte("PS1='$ '\n"), 0o600))
	cmd.Env = append(os.Environ(), "SHELL=/bin/sh", "ENV="+shrc, "HISTFILE=/dev/null", "BUNK_TEST_DIR="+dir)
	cmd.Env = append(cmd.Env, sc.env...)
	var stdout *os.File
	stdout, err = os.Create(filepath.Join(dir, "stdout.txt"))
	must(t, err)
	defer func() { _ = stdout.Close() }()
	stderr, err := os.Create(filepath.Join(dir, "stderr.txt"))
	must(t, err)
	defer func() { _ = stderr.Close() }()
	cmd.Stdout, cmd.Stderr = stdout, stderr
	start := time.Now()
	must(t, cmd.Start())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-time.After(limit + 15*time.Second):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		err = <-done
		t.Errorf("bunk-gui did not quit within %v", limit+15*time.Second)
	}
	t.Logf("bunk-gui ran %.1fs", time.Since(start).Seconds())
	g := &guiRun{t: t, dir: dir}
	data, rerr := os.ReadFile(rep)
	if err != nil || rerr != nil {
		g.logOutput()
		t.Fatalf("bunk-gui: %v; report: %v", err, rerr)
	}
	g.runReport = &runReport{}
	if err := json.Unmarshal(data, g.runReport); err != nil {
		t.Fatalf("report: %v", err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			g.logOutput()
		}
	})
	if len(g.Errors) > 0 {
		t.Errorf("script errors: %q", g.Errors)
	}
	// GLib warnings and criticals (forwarded to slog as WARN/ERROR) mean a
	// misuse of GTK even when the scenario's checks pass.
	errlog, _ := os.ReadFile(filepath.Join(dir, "stderr.txt"))
	for _, line := range strings.Split(string(errlog), "\n") {
		if strings.Contains(line, " WARN ") || strings.Contains(line, " ERROR ") || strings.Contains(line, "CRITICAL") ||
			strings.Contains(line, "DATA RACE") {
			t.Errorf("bunk-gui logged: %s", line)
		}
	}
	return g
}

func (g *guiRun) logOutput() {
	for _, name := range []string{"stdout.txt", "stderr.txt"} {
		data, _ := os.ReadFile(filepath.Join(g.dir, name))
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(lines) > 80 {
			lines = lines[len(lines)-80:]
		}
		g.t.Logf("%s (last %d lines):\n%s", name, len(lines), strings.Join(lines, "\n"))
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// file is a path in the scenario's directory, for shot/dump actions.
func (g *guiRun) file(name string) string { return filepath.Join(g.dir, name) }

func (g *guiRun) snap(label string) *uiSnapshot {
	g.t.Helper()
	for _, s := range g.Snapshots {
		if s.Label == label {
			return s
		}
	}
	g.t.Fatalf("no snapshot %q", label)
	return nil
}

func (g *guiRun) mark(name string) markReport {
	g.t.Helper()
	for _, m := range g.Marks {
		if m.Name == name {
			return m
		}
	}
	g.t.Fatalf("no mark %q", name)
	return markReport{}
}

// between returns the counter differences from mark a to mark b.
func (g *guiRun) between(a, b string) markReport {
	g.t.Helper()
	ma, mb := g.mark(a), g.mark(b)
	return markReport{
		Name: a + ".." + b, TMs: mb.TMs - ma.TMs, Frames: mb.Frames - ma.Frames,
		ClockFrames: mb.ClockFrames - ma.ClockFrames, RenderMallocs: mb.RenderMallocs - ma.RenderMallocs,
		Mallocs: mb.Mallocs - ma.Mallocs, KeyRenders: mb.KeyRenders - ma.KeyRenders,
	}
}

func (s *uiSnapshot) pane(t *testing.T, id int) paneReport {
	t.Helper()
	for _, p := range s.Panes {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("snapshot %q has no pane %d", s.Label, id)
	return paneReport{}
}

func (s *uiSnapshot) tab(t *testing.T, index int) tabReport {
	t.Helper()
	if index < 1 || index > len(s.Tabs) {
		t.Fatalf("snapshot %q has no tab %d (%d tabs)", s.Label, index, len(s.Tabs))
	}
	return s.Tabs[index-1]
}

func (s *uiSnapshot) focused(t *testing.T) paneReport { t.Helper(); return s.pane(t, s.FocusedPane) }

// text joins a pane's rows.
func (p paneReport) text() string { return strings.Join(p.Text, "\n") }

// ---- pixels ----

type rgb struct{ r, g, b int }

func hex(c uint32) rgb { return rgb{int(c >> 16 & 0xff), int(c >> 8 & 0xff), int(c & 0xff)} }

func (c rgb) String() string { return fmt.Sprintf("#%02x%02x%02x", c.r, c.g, c.b) }

func (c rgb) dist(d rgb) int {
	return max(abs(c.r-d.r), abs(c.g-d.g), abs(c.b-d.b))
}

func loadPNG(t *testing.T, path string) image.Image {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return img
}

func at(img image.Image, x, y int) rgb {
	r, g, b, _ := img.At(x, y).RGBA()
	return rgb{int(r >> 8), int(g >> 8), int(b >> 8)}
}

// count returns how many pixels in the device rect are within tol of c.
func count(img image.Image, x, y, w, h int, c rgb, tol int) int {
	n := 0
	for yy := y; yy < y+h; yy++ {
		for xx := x; xx < x+w; xx++ {
			if at(img, xx, yy).dist(c) <= tol {
				n++
			}
		}
	}
	return n
}

// blendCount counts pixels in the rect that are c drawn over bg with at
// least half coverage (anti-aliased glyph pixels included).
func blendCount(img image.Image, x, y, w, h int, c, bg rgb) int {
	n := 0
	d := [3]float64{float64(c.r - bg.r), float64(c.g - bg.g), float64(c.b - bg.b)}
	dd := d[0]*d[0] + d[1]*d[1] + d[2]*d[2]
	for yy := y; yy < y+h; yy++ {
		for xx := x; xx < x+w; xx++ {
			p := at(img, xx, yy)
			v := [3]float64{float64(p.r - bg.r), float64(p.g - bg.g), float64(p.b - bg.b)}
			k := (v[0]*d[0] + v[1]*d[1] + v[2]*d[2]) / dd
			if k < 0.5 || k > 1.01 {
				continue
			}
			e := 0.0
			for i := range 3 {
				e = max(e, abs64(v[i]-k*d[i]))
			}
			if e <= 4 {
				n++
			}
		}
	}
	return n
}

func abs64(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// inner shrinks a device rect by m pixels on each side.
func inner(r *rectReport, m int) (x, y, w, h int) {
	return r.DX + m, r.DY + m, r.DW - 2*m, r.DH - 2*m
}
