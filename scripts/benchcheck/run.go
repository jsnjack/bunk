package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Suite is one test binary invocation that a run repeats in every round.
type Suite struct {
	Name     string   // label in progress lines, notes and the header
	Module   string   // module directory, relative to the repository root
	Pkg      string   // package directory inside Module
	Bench    string   // -test.bench pattern
	Time     string   // -test.benchtime
	Args     []string // extra test binary flags
	Procs    int      // GOMAXPROCS for the benchmark process
	Optional bool     // skip with a note when it cannot build or run
}

// DefaultSuites are the benchmarks `make bench-check` runs. Every
// benchmarked path is single-threaded, so each runs with GOMAXPROCS=1 on
// the one pinned core: garbage collection then shares the measured core
// instead of hiding on idle ones. vt10x runs every benchmark of the
// package, so new parser benchmarks join without an edit here.
var DefaultSuites = []Suite{
	{Name: "vt10x", Module: ".", Pkg: "internal/vt10x", Bench: ".", Time: "500ms", Procs: 1},
	{Name: "session-output", Module: ".", Pkg: "internal/session", Bench: "^BenchmarkOutput$", Time: "500ms", Procs: 1},
	// Each reflow iteration restores 10 000 rows outside the timer, so a
	// short benchtime keeps a round near 10 s.
	{Name: "session-reflow", Module: ".", Pkg: "internal/session", Bench: "^BenchmarkReflow$", Time: "200ms", Procs: 1},
	// The frame benchmark draws into a fake rasterizer: no display needed.
	// -test.short stops TestMain from starting the headless compositor.
	{Name: "gui-frame", Module: "cmd/bunk-gui", Pkg: ".", Bench: "^BenchmarkFullScreenFrame$", Time: "500ms",
		Args: []string{"-test.short"}, Procs: 1, Optional: true},
}

// RunConfig configures Run.
type RunConfig struct {
	Root   string  // repository root
	Suites []Suite // in the order each round runs them
	// All lists every suite, including those not run this time, so a
	// benchmark that another suite selects is not reported as unselected.
	All       []Suite
	Count     int     // rounds; every suite runs once per round
	CPU       string  // taskset CPU list; "" runs unpinned
	BenchTime string  // overrides every suite's Time when set
	MinShare  float64 // a run with less CPU time / wall time is repeated
	// MinClock repeats a run whose core ran below this fraction of its top
	// clock: a power or thermal limit, or heavy load on other cores.
	MinClock float64
	// MaxSibling repeats a run whose core's SMT sibling was busier than
	// this fraction of the time.
	MaxSibling float64
	Retries    int           // repeats per run at most, for a disturbed run
	Pause      time.Duration // wait before a repeat, for a burst of load to pass
	MaxLoad    float64       // load average above which Run warns
	Out        string        // output file, replaced only when the run succeeds
	Log        io.Writer

	maxMHz float64 // top clock of the pinned core; 0 if unknown

	// Replaced by tests.
	build func(s Suite, dir, bin string) error
	exec  func(s Suite, dir, bin string, args []string) (runResult, error)
	list  func(s Suite, dir, bin string) ([]string, error)
	load  func() float64
}

// runResult is one benchmark process.
type runResult struct {
	Out       []byte
	Wall, CPU time.Duration
	MHz       float64 // median clock of the pinned core while it ran; 0 if unknown
	// Sibling is the busy fraction of the pinned core's SMT sibling while
	// it ran, or -1 if unknown. The sibling shares the core's caches and
	// execution units, so its load slows a benchmark without taking CPU
	// time from it.
	Sibling float64
}

// share is the fraction of its wall time the process spent on a CPU. A
// single-threaded process alone on its pinned core gets close to 1.
func (r runResult) share() float64 {
	if r.Wall <= 0 {
		return 0
	}
	return min(1, float64(r.CPU)/float64(r.Wall))
}

// disturbances lists why a run's times are unreliable: another process
// shared the core, its clock fell far below its top clock, or its SMT
// sibling was busy. It is empty when the run had a quiet core.
func (cfg *RunConfig) disturbances(r runResult) []string {
	var d []string
	if r.share() < cfg.MinShare {
		d = append(d, fmt.Sprintf("share %.0f%%", r.share()*100))
	}
	if cfg.maxMHz > 0 && r.MHz > 0 && r.MHz < cfg.MinClock*cfg.maxMHz {
		d = append(d, fmt.Sprintf("clock %.0f MHz", r.MHz))
	}
	if cfg.MaxSibling > 0 && r.Sibling > cfg.MaxSibling {
		d = append(d, fmt.Sprintf("sibling %.0f%% busy", r.Sibling*100))
	}
	return d
}

// quality ranks the attempts of one run by a guess at the fraction of a
// quiet core's speed each got. Compute-bound times follow the clock, and
// share scales speed directly; a busy SMT sibling is counted as costing up
// to half.
func (cfg *RunConfig) quality(r runResult) float64 {
	q := r.share()
	if cfg.maxMHz > 0 && r.MHz > 0 {
		q *= min(1, r.MHz/cfg.maxMHz)
	}
	if r.Sibling > 0 {
		q *= 1 - r.Sibling/2
	}
	return q
}

// Run builds each suite's test binary once, then runs `Count` rounds; a
// round runs every suite once, so a burst of load touches all benchmarks
// alike instead of every sample of one. A disturbed run is repeated up to
// Retries times, and the attempt with the best quality is kept. The output
// is `go test -bench` text behind a header of "key: value" lines that
// record the machine and the run's load.
func Run(cfg RunConfig) error {
	cfg.defaults()
	hdr := &header{}
	startLoad := cfg.load()
	hostHeader(hdr, cfg)

	tmp, err := os.MkdirTemp("", "benchcheck-")
	if err != nil {
		return fmt.Errorf("create build directory: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(tmp); err != nil {
			cfg.logf("warning: remove %s: %v\n", tmp, err)
		}
	}()

	type binary struct{ path, dir string }
	bins := map[string]binary{}
	var suites []Suite
	var skipped []string
	cfg.logf("benchcheck: building test binaries\n")
	for _, s := range cfg.Suites {
		dir := filepath.Join(cfg.Root, s.Module, s.Pkg)
		if _, ok := bins[dir]; !ok {
			bin := filepath.Join(tmp, fmt.Sprintf("%d-%s.test", len(bins), filepath.Base(filepath.Clean(dir))))
			if err := cfg.build(s, filepath.Join(cfg.Root, s.Module), bin); err != nil {
				if !s.Optional {
					return fmt.Errorf("%s: %w", s.Name, err)
				}
				skipped = append(skipped, fmt.Sprintf("%s (build failed: %s)", s.Name, firstLine(err)))
				cfg.logf("benchcheck: skipping %s: build failed:\n%v\n", s.Name, err)
				continue
			}
			bins[dir] = binary{bin, dir}
		}
		suites = append(suites, s)
	}

	// Benchmarks in the built packages that no suite selects.
	var notRun []string
	seen := map[string]bool{}
	for _, s := range suites {
		b := bins[filepath.Join(cfg.Root, s.Module, s.Pkg)]
		if seen[b.dir] {
			continue
		}
		seen[b.dir] = true
		names, err := cfg.list(s, b.dir, b.path)
		if err != nil {
			continue // the run below reports a binary that does not start
		}
		for _, name := range names {
			covered := false
			for _, o := range slices.Concat(cfg.All, suites) {
				if filepath.Join(cfg.Root, o.Module, o.Pkg) == b.dir && topLevelMatch(o.Bench, name) {
					covered = true
				}
			}
			if !covered {
				notRun = append(notRun, filepath.ToSlash(filepath.Join(s.Module, s.Pkg))+" "+name)
			}
		}
	}

	var body bytes.Buffer
	maxLoad, minShare, retried, runs, disturbed := startLoad, 1.0, 0, 0, 0
	var clocks, siblings []float64
	failed := map[string]bool{}
	for round := 1; round <= cfg.Count; round++ {
		cfg.logf("round %d/%d:", round, cfg.Count)
		for _, s := range suites {
			if failed[s.Name] {
				continue
			}
			b := bins[filepath.Join(cfg.Root, s.Module, s.Pkg)]
			var res runResult
			var err error
			for attempt := 0; ; attempt++ {
				var try runResult
				try, err = cfg.exec(s, b.dir, b.path, cfg.args(s))
				if err != nil {
					break
				}
				if attempt == 0 || cfg.quality(try) > cfg.quality(res) {
					res = try
				}
				d := strings.Join(cfg.disturbances(try), ", ")
				if d == "" || attempt >= cfg.Retries {
					break
				}
				retried++
				fmt.Fprintf(&body, "# repeating %s, round %d: %s\n", s.Name, round, d)
				cfg.logf(" [%s disturbed: %s; repeating]", s.Name, d)
				time.Sleep(cfg.Pause)
			}
			maxLoad = max(maxLoad, cfg.load())
			if err != nil {
				if s.Optional && round == 1 {
					failed[s.Name] = true
					skipped = append(skipped, fmt.Sprintf("%s (does not run: %s)", s.Name, firstLine(err)))
					cfg.logf(" %s skipped", s.Name)
					continue
				}
				cfg.logf("\n")
				return fmt.Errorf("%s, round %d: %w", s.Name, round, err)
			}
			runs++
			minShare = min(minShare, res.share())
			if res.MHz > 0 {
				clocks = append(clocks, res.MHz)
			}
			if res.Sibling >= 0 {
				siblings = append(siblings, res.Sibling)
			}
			fmt.Fprintf(&body, "# round %d/%d %s: wall %.2fs cpu %.2fs share %.2f", round, cfg.Count, s.Name,
				res.Wall.Seconds(), res.CPU.Seconds(), res.share())
			if res.MHz > 0 {
				fmt.Fprintf(&body, " clock %.0fMHz", res.MHz)
			}
			if res.Sibling >= 0 {
				fmt.Fprintf(&body, " sibling-busy %.2f", res.Sibling)
			}
			body.WriteByte('\n')
			body.Write(benchLines(res.Out))
			cfg.logf(" %s %.1fs", s.Name, res.Wall.Seconds())
			if d := cfg.disturbances(res); len(d) > 0 {
				disturbed++
				cfg.logf(" (kept: %s)", strings.Join(d, ", "))
			}
		}
		cfg.logf("\n")
	}
	if runs == 0 {
		return errors.New("no benchmark ran")
	}

	endLoad := cfg.load()
	maxLoad = max(maxLoad, endLoad)
	hdr.add("count", strconv.Itoa(cfg.Count))
	for _, s := range suites {
		if !failed[s.Name] {
			hdr.add("suite-"+s.Name, fmt.Sprintf("bench=%s benchtime=%s gomaxprocs=%d", s.Bench, cfg.benchTime(s), s.Procs))
		}
	}
	hdr.add("loadavg-start", fmt.Sprintf("%.2f", startLoad))
	hdr.add("loadavg-max", fmt.Sprintf("%.2f", maxLoad))
	hdr.add("loadavg-end", fmt.Sprintf("%.2f", endLoad))
	if len(clocks) > 0 {
		hdr.add("cpu-mhz", fmt.Sprintf("%.0f", median(clocks)))
	}
	if len(siblings) > 0 {
		hdr.add("sibling-busy-median", fmt.Sprintf("%.2f", median(siblings)))
		hdr.add("sibling-busy-max", fmt.Sprintf("%.2f", slices.Max(siblings)))
	}
	hdr.add("core-share-min", fmt.Sprintf("%.2f", minShare))
	hdr.add("retried-runs", strconv.Itoa(retried))
	hdr.add("disturbed-runs", fmt.Sprintf("%d of %d", disturbed, runs))
	hdr.add("skipped", strings.Join(skipped, "; "))
	hdr.add("not-run", strings.Join(notRun, ", "))

	var out bytes.Buffer
	hdr.write(&out)
	out.WriteByte('\n')
	out.Write(body.Bytes())
	if err := writeAtomic(cfg.Out, out.Bytes()); err != nil {
		return fmt.Errorf("write results: %w", err)
	}
	cfg.logf("benchcheck: wrote %s: %d runs, %d repeated, load average %.2f to %.2f, least core share %.2f\n",
		cfg.Out, runs, retried, startLoad, maxLoad, minShare)
	if maxLoad > cfg.MaxLoad {
		cfg.logf("warning: load average reached %.2f (limit %g, the benchmark itself adds 1): results are noisy\n",
			maxLoad, cfg.MaxLoad)
	}
	for _, s := range skipped {
		cfg.logf("note: skipped %s\n", s)
	}
	return nil
}

func (cfg *RunConfig) defaults() {
	if cfg.Log == nil {
		cfg.Log = io.Discard
	}
	if cfg.build == nil {
		cfg.build = buildTest
	}
	if cfg.exec == nil {
		cfg.exec = cfg.execBench
	}
	if cfg.list == nil {
		cfg.list = listBenchmarks
	}
	if cfg.load == nil {
		cfg.load = loadAvg
	}
	if _, err := strconv.Atoi(cfg.CPU); err == nil && cfg.maxMHz == 0 {
		cfg.maxMHz = readNum("/sys/devices/system/cpu/cpu"+cfg.CPU+"/cpufreq/cpuinfo_max_freq") / 1000
	}
}

// logf prints progress; a failed write to the log does not stop a run.
func (cfg *RunConfig) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(cfg.Log, format, args...)
}

func (cfg *RunConfig) benchTime(s Suite) string {
	if cfg.BenchTime != "" {
		return cfg.BenchTime
	}
	return s.Time
}

// args are the test binary flags of one run of s.
func (cfg *RunConfig) args(s Suite) []string {
	a := []string{"-test.run", "^$", "-test.bench", s.Bench, "-test.count", "1", "-test.benchmem"}
	if t := cfg.benchTime(s); t != "" {
		a = append(a, "-test.benchtime", t)
	}
	return append(a, s.Args...)
}

// topLevelMatch reports whether -test.bench pattern selects the top-level
// benchmark name. The go test runner matches each "/" level separately;
// the first level decides whether a top-level benchmark runs.
func topLevelMatch(pattern, name string) bool {
	first := pattern
	if i := strings.Index(pattern, "/"); i >= 0 {
		first = pattern[:i]
	}
	re, err := regexp.Compile(first)
	return err == nil && re.MatchString(name)
}

// benchLines keeps the configuration and result lines of go test output.
func benchLines(out []byte) []byte {
	var b bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "Benchmark") || configLine.MatchString(line) {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.Bytes()
}

func buildTest(s Suite, moduleDir, bin string) error {
	pkg := "./" + filepath.ToSlash(filepath.Clean(s.Pkg))
	if filepath.Clean(s.Pkg) == "." {
		pkg = "."
	}
	cmd := exec.Command("go", "test", "-c", "-o", bin, pkg)
	cmd.Dir = moduleDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}

func listBenchmarks(s Suite, dir, bin string) ([]string, error) {
	cmd := exec.Command(bin, append([]string{"-test.list", "^Benchmark"}, s.Args...)...)
	cmd.Dir = dir
	cmd.Env = benchEnv(s.Procs)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list benchmarks of %s: %w", s.Name, err)
	}
	var names []string
	for _, f := range strings.Fields(string(out)) {
		if strings.HasPrefix(f, "Benchmark") {
			names = append(names, f)
		}
	}
	return names, nil
}

// benchEnv is the environment of a benchmark process: GOMAXPROCS as the
// suite wants, Go runtime tuning from the caller's shell removed, and no
// display, so nothing can open a window on the desktop.
func benchEnv(procs int) []string {
	drop := []string{"GOMAXPROCS", "GOGC", "GOMEMLIMIT", "GODEBUG", "DISPLAY", "WAYLAND_DISPLAY", "WAYLAND_SOCKET", "BUNK_HEADLESS"}
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(drop, k) {
			env = append(env, kv)
		}
	}
	if procs > 0 {
		env = append(env, "GOMAXPROCS="+strconv.Itoa(procs))
	}
	return env
}

// execBench runs one benchmark process, pinned with taskset when CPU is
// set, and measures its wall time, CPU time and the pinned core's clock.
func (cfg *RunConfig) execBench(s Suite, dir, bin string, args []string) (runResult, error) {
	name, argv := bin, args
	if cfg.CPU != "" {
		name, argv = "taskset", append([]string{"-c", cfg.CPU, bin}, args...)
	}
	cmd := exec.Command(name, argv...)
	cmd.Dir = dir
	cmd.Env = benchEnv(s.Procs)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	sibling := smtSibling(cfg.CPU)
	busy0, total0 := cpuJiffies(sibling)
	stop := sampleClock(cfg.CPU)
	start := time.Now()
	err := cmd.Run()
	res := runResult{Out: stdout.Bytes(), Wall: time.Since(start), MHz: stop(), Sibling: -1}
	if busy1, total1 := cpuJiffies(sibling); total1 > total0 {
		res.Sibling = float64(busy1-busy0) / float64(total1-total0)
	}
	if err != nil {
		return res, fmt.Errorf("%w\n%s%s", err, tail(stdout.Bytes()), tail(stderr.Bytes()))
	}
	if ru, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
		res.CPU = time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
	}
	return res, nil
}

// sampleClock samples the clock of a single pinned CPU every 100 ms until
// the returned function is called, which returns the median in MHz, or 0
// when cpu is not one CPU or the kernel does not report its clock.
func sampleClock(cpu string) func() float64 {
	if _, err := strconv.Atoi(cpu); err != nil {
		return func() float64 { return 0 }
	}
	path := "/sys/devices/system/cpu/cpu" + cpu + "/cpufreq/scaling_cur_freq"
	done := make(chan struct{})
	result := make(chan float64)
	go func() {
		var mhz []float64
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if khz := readNum(path); khz > 0 {
					mhz = append(mhz, khz/1000)
				}
			case <-done:
				if len(mhz) == 0 {
					result <- 0
				} else {
					result <- median(mhz)
				}
				return
			}
		}
	}()
	return func() float64 {
		close(done)
		return <-result
	}
}

// smtSibling returns the other hardware thread of a single pinned CPU's
// core, or "" when cpu is not one CPU or has no sibling.
func smtSibling(cpu string) string {
	if _, err := strconv.Atoi(cpu); err != nil {
		return ""
	}
	list := readTrim("/sys/devices/system/cpu/cpu" + cpu + "/topology/thread_siblings_list")
	for _, part := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == '-' }) {
		if part != cpu {
			if _, err := strconv.Atoi(part); err == nil {
				return part
			}
		}
	}
	return ""
}

// cpuJiffies returns the busy and total time of one CPU from /proc/stat,
// in clock ticks; zeros when cpu is "" or unknown.
func cpuJiffies(cpu string) (busy, total uint64) {
	if cpu == "" {
		return 0, 0
	}
	data, _ := os.ReadFile("/proc/stat")
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "cpu"+cpu {
			continue
		}
		for i, f := range fields[1:] {
			n, _ := strconv.ParseUint(f, 10, 64)
			total += n
			if i != 3 && i != 4 { // idle and iowait
				busy += n
			}
		}
	}
	return busy, total
}

// header is the ordered "key: value" block at the top of a run's output.
type header struct{ keys, values []string }

func (h *header) add(key, value string) {
	value = strings.Join(strings.Fields(value), " ")
	if value != "" {
		h.keys, h.values = append(h.keys, key), append(h.values, value)
	}
}

func (h *header) write(w *bytes.Buffer) {
	for i, k := range h.keys {
		fmt.Fprintf(w, "%s: %s\n", k, h.values[i])
	}
}

// hostHeader records what changes every result: source, toolchain,
// kernel, CPU and its power settings.
func hostHeader(h *header, cfg RunConfig) {
	h.add("bunk-bench", "1")
	h.add("commit", gitCommit(cfg.Root))
	h.add("date", time.Now().UTC().Format(time.RFC3339))
	h.add("go", goVersion(cfg.Root))
	h.add("kernel", readTrim("/proc/sys/kernel/osrelease"))
	h.add("cpu-model", cpuModel())
	if cfg.CPU == "" {
		h.add("pinned-cpu", "none")
		return
	}
	h.add("pinned-cpu", cfg.CPU)
	h.add("smt-sibling", smtSibling(cfg.CPU))
	if _, err := strconv.Atoi(cfg.CPU); err == nil {
		dir := "/sys/devices/system/cpu/cpu" + cfg.CPU + "/cpufreq/"
		if khz := readNum(dir + "cpuinfo_max_freq"); khz > 0 {
			h.add("cpu-max-mhz", fmt.Sprintf("%.0f", khz/1000))
		}
		h.add("cpu-governor", readTrim(dir+"scaling_governor"))
		h.add("cpu-epp", readTrim(dir+"energy_performance_preference"))
	}
}

func gitCommit(root string) string {
	out, err := exec.Command("git", "-C", root, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	commit := strings.TrimSpace(string(out))
	if st, err := exec.Command("git", "-C", root, "status", "--porcelain", "--untracked-files=no").Output(); err == nil && len(bytes.TrimSpace(st)) > 0 {
		commit += "-dirty"
	}
	return commit
}

func goVersion(root string) string {
	cmd := exec.Command("go", "env", "GOVERSION")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return runtime.Version()
	}
	return strings.TrimSpace(string(out))
}

func cpuModel() string {
	data, _ := os.ReadFile("/proc/cpuinfo")
	for _, line := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "model name" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// loadAvg returns the 1-minute load average, or 0 when unknown.
func loadAvg() float64 { return readNum("/proc/loadavg") }

func readTrim(path string) string {
	data, _ := os.ReadFile(path)
	return strings.TrimSpace(string(data))
}

// readNum returns the first number in a file, or 0.
func readNum(path string) float64 { return num(readTrim(path)) }

func firstLine(err error) string {
	s, _, _ := strings.Cut(err.Error(), "\n")
	return s
}

// tail returns the last 20 lines of b.
func tail(b []byte) string {
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > 20 {
		lines = lines[len(lines)-20:]
	}
	s := strings.Join(lines, "\n")
	if s != "" {
		s += "\n"
	}
	return s
}

// writeAtomic replaces path with data, so a failed or interrupted run
// leaves the previous file whole.
func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		return errors.Join(fmt.Errorf("write %s: %w", f.Name(), err), f.Close(), os.Remove(f.Name()))
	}
	if err := f.Close(); err != nil {
		return errors.Join(fmt.Errorf("close %s: %w", f.Name(), err), os.Remove(f.Name()))
	}
	if err := os.Chmod(f.Name(), 0o644); err != nil {
		return errors.Join(fmt.Errorf("chmod %s: %w", f.Name(), err), os.Remove(f.Name()))
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return errors.Join(fmt.Errorf("rename to %s: %w", path, err), os.Remove(f.Name()))
	}
	return nil
}
