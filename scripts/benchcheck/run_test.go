package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeRun replaces Run's process handling: every suite "runs" instantly
// and reports one benchmark named after it.
type fakeRun struct {
	builds   []string
	calls    []string
	share    func(suite string, call int) float64
	quiet    func(call int) (mhz, sibling float64) // clock and SMT sibling load
	fail     map[string]error                      // build or run failures by suite name
	runFail  map[string]error
	listed   []string
	loadavgs []float64
}

func (f *fakeRun) config(t *testing.T, suites []Suite, count int) RunConfig {
	return RunConfig{
		Root: "/repo", Suites: suites, Count: count, CPU: "0", MaxLoad: 4,
		MinShare: 0.9, MinClock: 0.8, MaxSibling: 0.5, Retries: 2, maxMHz: 5100,
		Out: filepath.Join(t.TempDir(), "out", "bench.txt"), Log: &bytes.Buffer{},
		build: func(s Suite, dir, bin string) error {
			f.builds = append(f.builds, filepath.Join(dir, s.Pkg))
			return f.fail[s.Name]
		},
		exec: func(s Suite, dir, bin string, args []string) (runResult, error) {
			f.calls = append(f.calls, s.Name)
			if err := f.runFail[s.Name]; err != nil {
				return runResult{}, err
			}
			share := 1.0
			if f.share != nil {
				share = f.share(s.Name, len(f.calls))
			}
			out := fmt.Sprintf("goos: linux\npkg: bunk/%s\nBenchmark%s \t 100\t %d ns/op\t 0 B/op\t 0 allocs/op\nPASS\nok  \tbunk\t1s\n",
				s.Pkg, s.Name, 1000+len(f.calls))
			mhz, sibling := 5000.0, 0.1
			if f.quiet != nil {
				mhz, sibling = f.quiet(len(f.calls))
			}
			return runResult{Out: []byte(out), Wall: time.Second, CPU: time.Duration(share * float64(time.Second)), MHz: mhz, Sibling: sibling}, nil
		},
		list: func(s Suite, dir, bin string) ([]string, error) { return f.listed, nil },
		load: func() float64 {
			l := 0.5
			if len(f.loadavgs) > 0 {
				l, f.loadavgs = f.loadavgs[0], f.loadavgs[1:]
			}
			return l
		},
	}
}

func readResult(t *testing.T, path string) *File {
	t.Helper()
	f, err := parseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestRunInterleavesRounds(t *testing.T) {
	suites := []Suite{
		{Name: "A", Module: ".", Pkg: "pkg/x", Bench: "^BenchmarkA$", Time: "1s", Procs: 1},
		{Name: "B", Module: ".", Pkg: "pkg/x", Bench: "^BenchmarkB", Time: "2s", Procs: 1},
		{Name: "C", Module: "gui", Pkg: ".", Bench: "^BenchmarkC$", Optional: true, Procs: 1},
	}
	f := &fakeRun{
		fail:     map[string]error{"C": errors.New("cgo: gtk4 not found\nmore detail")},
		listed:   []string{"BenchmarkA", "BenchmarkB", "BenchmarkBig", "BenchmarkUnselected"},
		loadavgs: []float64{0.5, 0.7, 3.1, 0.6},
	}
	cfg := f.config(t, suites, 3)
	if err := Run(cfg); err != nil {
		t.Fatal(err)
	}
	if want := []string{"/repo/pkg/x", "/repo/gui"}; !slices.Equal(f.builds, want) {
		t.Errorf("builds = %q, want one per package: %q", f.builds, want)
	}
	if want := []string{"A", "B", "A", "B", "A", "B"}; !slices.Equal(f.calls, want) {
		t.Errorf("runs = %q, want rounds of every suite: %q", f.calls, want)
	}
	res := readResult(t, cfg.Out)
	for _, name := range []string{"BenchmarkA", "BenchmarkB"} {
		if b := res.Benches["bunk/pkg/x "+name]; b == nil || len(b.Values["ns/op"]) != 3 {
			t.Errorf("%s: %+v, want 3 samples", name, b)
		}
	}
	for key, want := range map[string]string{
		"bunk-bench":          "1",
		"pinned-cpu":          "0",
		"count":               "3",
		"suite-A":             "bench=^BenchmarkA$ benchtime=1s gomaxprocs=1",
		"suite-B":             "bench=^BenchmarkB benchtime=2s gomaxprocs=1",
		"loadavg-start":       "0.50",
		"loadavg-max":         "3.10",
		"cpu-mhz":             "5000",
		"sibling-busy-median": "0.10",
		"core-share-min":      "1.00",
		"retried-runs":        "0",
		"disturbed-runs":      "0 of 6",
		"skipped":             "C (build failed: cgo: gtk4 not found)",
		"not-run":             "pkg/x BenchmarkUnselected",
	} {
		if got := res.Config[key]; got != want {
			t.Errorf("header %s = %q, want %q", key, got, want)
		}
	}
	for _, key := range []string{"commit", "date", "go", "kernel", "cpu-model"} {
		if res.Config[key] == "" {
			t.Errorf("header lacks %s", key)
		}
	}
	if _, ok := res.Config["suite-C"]; ok {
		t.Error("header lists the skipped suite's settings")
	}

	// Running only suite A: B's benchmarks are not reported as unselected.
	f = &fakeRun{listed: []string{"BenchmarkA", "BenchmarkB", "BenchmarkUnselected"}}
	cfg = f.config(t, suites[:1], 1)
	cfg.All = suites
	if err := Run(cfg); err != nil {
		t.Fatal(err)
	}
	if got := readResult(t, cfg.Out).Config["not-run"]; got != "pkg/x BenchmarkUnselected" {
		t.Errorf("not-run with one suite = %q", got)
	}
	if data, _ := os.ReadFile(cfg.Out); strings.Contains(string(data), "PASS") {
		t.Error("output keeps go test's PASS lines")
	}
}

func TestRunRepeatsSharedRuns(t *testing.T) {
	suites := []Suite{{Name: "A", Module: ".", Pkg: "p", Bench: ".", Procs: 1}}
	for _, c := range []struct {
		name    string
		shares  []float64 // share of each run in turn
		samples []float64 // ns/op kept: 1000 + the number of the kept run
		retried string
		least   string
	}{
		{"alone", []float64{0.99, 0.98, 0.99}, []float64{1001, 1002, 1003}, "0", "0.98"},
		{"shared once", []float64{0.99, 0.5, 0.99, 0.99}, []float64{1001, 1003, 1004}, "1", "0.99"},
		// Shared on every attempt: the attempt with most of the core is kept.
		{"shared throughout", []float64{0.5, 0.7, 0.6, 0.6, 0.6, 0.6}, []float64{1002, 1004}, "4", "0.60"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeRun{share: func(_ string, call int) float64 { return c.shares[call-1] }}
			cfg := f.config(t, suites, len(c.samples))
			if err := Run(cfg); err != nil {
				t.Fatal(err)
			}
			if len(f.calls) != len(c.shares) {
				t.Errorf("%d runs, want %d", len(f.calls), len(c.shares))
			}
			res := readResult(t, cfg.Out)
			if got := res.Benches["bunk/p BenchmarkA"].Values["ns/op"]; !slices.Equal(got, c.samples) {
				t.Errorf("samples %v, want %v", got, c.samples)
			}
			if res.Config["retried-runs"] != c.retried || res.Config["core-share-min"] != c.least {
				t.Errorf("header retried-runs %q core-share-min %q, want %s and %s",
					res.Config["retried-runs"], res.Config["core-share-min"], c.retried, c.least)
			}
		})
	}
}

// A clock far below the core's top clock (power or thermal limits, heavy
// load on other cores) and a busy SMT sibling disturb a run as a shared
// core does.
func TestRunRepeatsLowClockAndBusySibling(t *testing.T) {
	suites := []Suite{{Name: "A", Module: ".", Pkg: "p", Bench: ".", Procs: 1}}
	type q struct{ mhz, sibling float64 }
	for _, c := range []struct {
		name      string
		runs      []q
		samples   []float64
		disturbed string
	}{
		{"clock a little low", []q{{4600, 0}, {5050, 0}}, []float64{1001, 1002}, "0 of 2"},
		{"clock throttled once", []q{{2010, 0}, {5050, 0}, {5040, 0.1}}, []float64{1002, 1003}, "0 of 2"},
		{"sibling busy once", []q{{5050, 0.9}, {5050, 0.02}, {5040, 0}}, []float64{1002, 1003}, "0 of 2"},
		// Every attempt disturbed: keep the one likely fastest, record it.
		{"loaded throughout", []q{{3800, 0.9}, {3700, 0.6}, {3900, 0.95}, {5050, 0}}, []float64{1002, 1004}, "1 of 2"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeRun{quiet: func(call int) (float64, float64) { return c.runs[call-1].mhz, c.runs[call-1].sibling }}
			cfg := f.config(t, suites, len(c.samples))
			if err := Run(cfg); err != nil {
				t.Fatal(err)
			}
			if len(f.calls) != len(c.runs) {
				t.Errorf("%d runs, want %d", len(f.calls), len(c.runs))
			}
			res := readResult(t, cfg.Out)
			if got := res.Benches["bunk/p BenchmarkA"].Values["ns/op"]; !slices.Equal(got, c.samples) {
				t.Errorf("samples %v, want %v", got, c.samples)
			}
			if got := res.Config["disturbed-runs"]; got != c.disturbed {
				t.Errorf("disturbed-runs = %q, want %q", got, c.disturbed)
			}
		})
	}
}

func TestRunFailures(t *testing.T) {
	suites := []Suite{
		{Name: "A", Module: ".", Pkg: "p", Bench: ".", Procs: 1},
		{Name: "G", Module: "gui", Pkg: ".", Bench: ".", Optional: true, Procs: 1},
	}
	// An optional suite that builds but does not run is skipped.
	f := &fakeRun{runFail: map[string]error{"G": errors.New("exit status 1\ncannot open display")}}
	cfg := f.config(t, suites, 2)
	if err := Run(cfg); err != nil {
		t.Fatal(err)
	}
	if want := []string{"A", "G", "A"}; !slices.Equal(f.calls, want) {
		t.Errorf("runs = %q, want %q", f.calls, want)
	}
	if got := readResult(t, cfg.Out).Config["skipped"]; got != "G (does not run: exit status 1)" {
		t.Errorf("skipped = %q", got)
	}

	// A required suite that fails stops the run and keeps the old file.
	f = &fakeRun{runFail: map[string]error{"A": errors.New("benchmark panicked")}}
	cfg = f.config(t, suites, 2)
	if err := os.MkdirAll(filepath.Dir(cfg.Out), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.Out, []byte("old baseline\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Run(cfg); err == nil || !strings.Contains(err.Error(), "benchmark panicked") {
		t.Errorf("Run = %v, want the benchmark's failure", err)
	}
	if data, _ := os.ReadFile(cfg.Out); string(data) != "old baseline\n" {
		t.Errorf("a failed run changed the output file: %q", data)
	}
	f = &fakeRun{fail: map[string]error{"A": errors.New("syntax error")}}
	if err := Run(f.config(t, suites, 1)); err == nil {
		t.Error("a required suite that does not build did not fail the run")
	}
}

func TestRunArgs(t *testing.T) {
	s := Suite{Name: "gui", Bench: "^BenchmarkFrame$", Time: "500ms", Args: []string{"-test.short"}}
	cfg := RunConfig{}
	want := []string{"-test.run", "^$", "-test.bench", "^BenchmarkFrame$", "-test.count", "1", "-test.benchmem", "-test.benchtime", "500ms", "-test.short"}
	if got := cfg.args(s); !slices.Equal(got, want) {
		t.Errorf("args = %q\nwant %q", got, want)
	}
	cfg.BenchTime = "20x"
	if got := cfg.args(s); got[8] != "20x" {
		t.Errorf("benchtime override ignored: %q", got)
	}
}

func TestTopLevelMatch(t *testing.T) {
	for _, c := range []struct {
		pattern, name string
		want          bool
	}{
		{".", "BenchmarkParse", true},
		{"^BenchmarkOutput$", "BenchmarkOutput", true},
		{"^BenchmarkOutput$", "BenchmarkOutputLarge", false},
		{"^BenchmarkReflow$/mixed", "BenchmarkReflow", true},
		{"[", "BenchmarkX", false},
	} {
		if got := topLevelMatch(c.pattern, c.name); got != c.want {
			t.Errorf("topLevelMatch(%q, %q) = %v", c.pattern, c.name, got)
		}
	}
}

func TestBenchEnv(t *testing.T) {
	t.Setenv("DISPLAY", ":0")
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	t.Setenv("GOGC", "off")
	t.Setenv("GOMAXPROCS", "24")
	env := benchEnv(1)
	for _, kv := range env {
		for _, k := range []string{"DISPLAY=", "WAYLAND_DISPLAY=", "GOGC=", "GOMAXPROCS=24"} {
			if strings.HasPrefix(kv, k) {
				t.Errorf("benchmark environment has %s", kv)
			}
		}
	}
	if !slices.Contains(env, "GOMAXPROCS=1") {
		t.Error("benchmark environment lacks GOMAXPROCS=1")
	}
}

func TestSelectSuites(t *testing.T) {
	got, err := selectSuites(DefaultSuites, "session-reflow, vt10x")
	if err != nil || len(got) != 2 || got[0].Name != "session-reflow" || got[1].Name != "vt10x" {
		t.Errorf("selectSuites = %v, %v", got, err)
	}
	if all, _ := selectSuites(DefaultSuites, ""); len(all) != len(DefaultSuites) {
		t.Error("an empty list does not select every suite")
	}
	if _, err := selectSuites(DefaultSuites, "nope"); err == nil || !strings.Contains(err.Error(), "vt10x") {
		t.Errorf("unknown suite: %v", err)
	}
}

// TestRunEndToEnd builds and runs a real benchmark twice, the second time
// three times slower, and checks that the comparison fails only then.
func TestRunEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs benchmarks")
	}
	// Timing on a pinned core is only meaningful on a quiet machine, not
	// while check-all runs other packages' race tests in parallel.
	if os.Getenv("BENCHCHECK_E2E") == "" {
		t.Skip("timing test; make bench-check runs it with BENCHCHECK_E2E=1")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go command")
	}
	cpu := "0"
	if _, err := exec.LookPath("taskset"); err != nil {
		cpu = ""
	}
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module spin\n\ngo 1.24\n")
	src := func(n int) string {
		return fmt.Sprintf(`package spin

import "testing"

var sink uint64

func BenchmarkSpin(b *testing.B) {
	for b.Loop() {
		x := uint64(1)
		for i := 0; i < %d; i++ {
			x = x*6364136223846793005 + 1442695040888963407
		}
		sink += x
	}
}
`, n)
	}
	suites := []Suite{{Name: "spin", Module: ".", Pkg: ".", Bench: ".", Time: "20ms", Procs: 1}}
	run := func(n int, out string) {
		t.Helper()
		writeFile(t, root, "spin_test.go", src(n))
		var log bytes.Buffer
		cfg := RunConfig{Root: root, Suites: suites, Count: 5, CPU: cpu, MinShare: 0.5, Retries: 1, MaxLoad: 1e9, Out: out, Log: &log}
		if err := Run(cfg); err != nil {
			t.Fatalf("%v\n%s", err, log.String())
		}
	}
	base, slow := filepath.Join(root, "base.txt"), filepath.Join(root, "slow.txt")
	run(20000, base)
	run(60000, slow)
	res := readResult(t, base)
	if b := res.Benches["spin BenchmarkSpin"]; b == nil || len(b.Values["ns/op"]) != 5 {
		t.Fatalf("baseline: %+v", res.Benches)
	}
	wantPin := cpu
	if cpu == "" {
		wantPin = "none"
	}
	if res.Config["pinned-cpu"] != wantPin {
		t.Errorf("pinned-cpu = %q, want %q", res.Config["pinned-cpu"], wantPin)
	}
	var stdout, stderr bytes.Buffer
	if code := cli([]string{"compare", base, slow}, &stdout, &stderr); code != exitRegression {
		t.Errorf("3x slower: exit %d, want %d\n%s%s", code, exitRegression, stdout.String(), stderr.String())
	}
	stdout.Reset()
	if code := cli([]string{"compare", base, base}, &stdout, &stderr); code != exitOK {
		t.Errorf("same file: exit %d\n%s%s", code, stdout.String(), stderr.String())
	}
}
