package main

import (
	"bytes"
	"fmt"
	"maps"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const sampleOutput = `bunk-bench: 1
go: go1.26.10
cpu-model: Test CPU
# round 1/2 vt10x: wall 1.00s cpu 0.99s share 0.99 clock 5113MHz sibling-busy 0.02
goos: linux
goarch: amd64
pkg: bunk/internal/vt10x
cpu: Test CPU
BenchmarkWrite
BenchmarkWrite/ascii         	     649	   1753102 ns/op	 598.13 MB/s	       0 B/op	       0 allocs/op
BenchmarkWrite/sgr           	     194	   6173722 ns/op	 169.86 MB/s	       0 B/op	       0 allocs/op
PASS
ok  	bunk/internal/vt10x	4.6s
pkg: bunk/internal/session
BenchmarkReflow/shell/200-120-200 	     733	   1615222 ns/op	   1608926 cpu-ns/op	 2391552 B/op	     630 allocs/op
BenchmarkWrite/ascii-24 	     700	   1700000 ns/op
--- FAIL: BenchmarkBroken
# round 2/2 vt10x: wall 1.00s cpu 0.99s share 0.99
pkg: bunk/internal/vt10x
BenchmarkWrite/ascii         	     650	   1753000 ns/op	 598.10 MB/s	       0 B/op	       0 allocs/op
`

func TestParse(t *testing.T) {
	f, err := Parse(strings.NewReader(sampleOutput))
	if err != nil {
		t.Fatal(err)
	}
	if f.Config["go"] != "go1.26.10" || f.Config["cpu-model"] != "Test CPU" || f.Config["pkg"] != "bunk/internal/vt10x" {
		t.Errorf("config = %v", f.Config)
	}
	want := []string{
		"bunk/internal/vt10x BenchmarkWrite/ascii",
		"bunk/internal/vt10x BenchmarkWrite/sgr",
		"bunk/internal/session BenchmarkReflow/shell/200-120-200",
		"bunk/internal/session BenchmarkWrite/ascii-24",
	}
	if !slices.Equal(f.Order, want) {
		t.Fatalf("order = %q\nwant %q", f.Order, want)
	}
	ascii := f.Benches[want[0]]
	if got := ascii.Values["ns/op"]; !slices.Equal(got, []float64{1753102, 1753000}) {
		t.Errorf("ascii ns/op = %v", got)
	}
	if got := ascii.Values["MB/s"]; len(got) != 2 || got[0] != 598.13 {
		t.Errorf("ascii MB/s = %v", got)
	}
	reflow := f.Benches[want[2]]
	if reflow.Label() != "session Reflow/shell/200-120-200" {
		t.Errorf("label = %q: a width in the name must not be taken for a GOMAXPROCS suffix", reflow.Label())
	}
	if got := reflow.Values["cpu-ns/op"]; !slices.Equal(got, []float64{1608926}) {
		t.Errorf("reflow cpu-ns/op = %v", got)
	}
}

func TestParseRejectsBadNumbers(t *testing.T) {
	if _, err := Parse(strings.NewReader("BenchmarkX 10 abc ns/op\n")); err == nil {
		t.Error("a malformed value parsed without error")
	}
}

func TestQuantiles(t *testing.T) {
	for _, c := range []struct {
		xs   []float64
		q    float64
		want float64
	}{
		{[]float64{3, 1, 2}, 0.5, 2},
		{[]float64{4, 1, 3, 2}, 0.5, 2.5},
		{[]float64{1, 2, 3, 4, 5}, 0.25, 2},
		{[]float64{1, 2, 3, 4, 5}, 0.75, 4},
		{[]float64{7}, 0.5, 7},
	} {
		if got := quantile(c.xs, c.q); got != c.want {
			t.Errorf("quantile(%v, %g) = %g, want %g", c.xs, c.q, got, c.want)
		}
	}
	if !math.IsNaN(median(nil)) {
		t.Error("median of no samples is not NaN")
	}
	xs := []float64{3, 1, 2}
	median(xs)
	if !slices.Equal(xs, []float64{3, 1, 2}) {
		t.Error("median reordered its argument")
	}
}

func TestMannWhitneyKnownValues(t *testing.T) {
	seq := func(from, to float64) (s []float64) {
		for v := from; v <= to; v++ {
			s = append(s, v)
		}
		return s
	}
	for _, c := range []struct {
		name string
		a, b []float64
		want float64
	}{
		{"separated 3+3", seq(1, 3), seq(4, 6), 2.0 / 20},
		{"separated 5+5", seq(1, 5), seq(6, 10), 2.0 / 252},
		{"separated 10+10", seq(1, 10), seq(11, 20), 2.0 / 184756},
		{"reversed", seq(6, 10), seq(1, 5), 2.0 / 252},
		{"identical", []float64{5, 5, 5}, []float64{5, 5, 5}, 1},
		{"interleaved", []float64{1, 4, 5, 8}, []float64{2, 3, 6, 7}, 1},
		{"empty", nil, seq(1, 3), 1},
	} {
		if got := mannWhitney(c.a, c.b); math.Abs(got-c.want) > 1e-12 {
			t.Errorf("%s: p = %g, want %g", c.name, got, c.want)
		}
	}
	for n := 2; n <= 12; n++ {
		if got, want := mannWhitney(seq(1, float64(n)), seq(101, float64(100+n))), minPValue(n, n); math.Abs(got-want) > 1e-12 {
			t.Errorf("separated %d+%d: p = %g, minPValue = %g", n, n, got, want)
		}
	}
}

// bruteP is the U test's exact p-value by enumerating every split of the
// pooled samples.
func bruteP(a, b []float64) float64 {
	r := rankSamples(a, b)
	n, mean, dist := len(r.ranks), r.n1*(len(r.ranks)+1), r.dist()
	var total, extreme float64
	for mask := 0; mask < 1<<n; mask++ {
		if popcount(mask) != r.n1 {
			continue
		}
		s := 0
		for i := range n {
			if mask&(1<<i) != 0 {
				s += r.ranks[i]
			}
		}
		total++
		if abs(s-mean) >= dist {
			extreme++
		}
	}
	return extreme / total
}

func popcount(x int) (n int) {
	for ; x != 0; x &= x - 1 {
		n++
	}
	return n
}

func TestMannWhitneyExactMatchesEnumeration(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for trial := range 300 {
		n1, n2 := 1+rng.IntN(7), 1+rng.IntN(7)
		a, b := make([]float64, n1), make([]float64, n2)
		levels := 2 + rng.IntN(12) // few levels: many ties
		for i := range a {
			a[i] = float64(rng.IntN(levels))
		}
		for i := range b {
			b[i] = float64(rng.IntN(levels) + rng.IntN(3))
		}
		got, want := mannWhitney(a, b), bruteP(a, b)
		if math.Abs(got-want) > 1e-12 {
			t.Fatalf("trial %d: a=%v b=%v: p = %g, enumeration %g", trial, a, b, got, want)
		}
		if rev := mannWhitney(b, a); math.Abs(rev-got) > 1e-12 {
			t.Fatalf("trial %d: p(a,b) = %g but p(b,a) = %g", trial, got, rev)
		}
	}
}

func TestMannWhitneyNormalApproximation(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	for trial := range 20 {
		a, b := make([]float64, 25), make([]float64, 25)
		shift := rng.Float64() * 1.5
		for i := range a {
			a[i] = math.Round(rng.NormFloat64()*10) / 10
			b[i] = math.Round((rng.NormFloat64()+shift)*10) / 10
		}
		r := rankSamples(a, b)
		exact, approx := r.exactP(), r.normalP()
		if math.Abs(exact-approx) > 0.01 {
			t.Errorf("trial %d: exact p %g, normal approximation %g", trial, exact, approx)
		}
	}
	// Above exactLimit samples mannWhitney uses the approximation.
	a, b := make([]float64, 40), make([]float64, 40)
	for i := range a {
		a[i], b[i] = float64(i), float64(i)+100
	}
	if p := mannWhitney(a, b); p > 1e-10 {
		t.Errorf("80 separated samples: p = %g", p)
	}
}

func TestMinPValue(t *testing.T) {
	for _, c := range []struct {
		n1, n2 int
		want   float64
	}{{3, 3, 0.1}, {4, 4, 2.0 / 70}, {5, 5, 2.0 / 252}, {10, 10, 2.0 / 184756}, {0, 5, 1}, {1, 1, 1}} {
		if got := minPValue(c.n1, c.n2); math.Abs(got-c.want) > 1e-15 {
			t.Errorf("minPValue(%d, %d) = %g, want %g", c.n1, c.n2, got, c.want)
		}
	}
}

// file builds a File from benchmark samples by unit.
func file(cfg map[string]string, benches ...*Bench) *File {
	f := &File{Config: map[string]string{}, Benches: map[string]*Bench{}}
	for k, v := range cfg {
		f.Config[k] = v
	}
	for _, b := range benches {
		f.Benches[b.Key()] = b
		f.Order = append(f.Order, b.Key())
	}
	return f
}

func bench(name string, units ...any) *Bench {
	b := &Bench{Pkg: "bunk/internal/vt10x", Name: name, Values: map[string][]float64{}}
	for i := 0; i < len(units); i += 2 {
		b.Values[units[i].(string)] = units[i+1].([]float64)
	}
	return b
}

func scale(xs []float64, f float64) []float64 {
	out := make([]float64, len(xs))
	for i, x := range xs {
		out[i] = x * f
	}
	return out
}

// Ten wall times of a benchmark on a shared machine: about 1 ms, two runs
// slowed by another process.
var noisy = []float64{1.000e6, 1.031e6, 0.982e6, 1.402e6, 1.012e6, 0.991e6, 0.973e6, 1.022e6, 1.505e6, 1.004e6}

// The same benchmark measured again: other values, other outliers.
var noisyAgain = []float64{1.040e6, 0.990e6, 1.013e6, 0.981e6, 1.452e6, 1.031e6, 1.002e6, 0.971e6, 1.021e6, 0.993e6}

func verdicts(rep Report) map[string]string {
	m := map[string]string{}
	for _, r := range rep.Rows {
		m[r.Label] = r.Verdict
	}
	return m
}

func TestCompareVerdicts(t *testing.T) {
	jitter := []float64{1.00, 1.01, 0.99, 1.02, 0.98, 1.00, 1.01, 0.99, 1.00, 1.02}
	base := scale(jitter, 1e6)
	for _, c := range []struct {
		name      string
		cur       []float64
		threshold float64
		want      string
	}{
		{"15% slower", scale(reversed(jitter), 1.15e6), 10, verdictRegression},
		{"8% slower, threshold 10", scale(reversed(jitter), 1.08e6), 10, verdictSlower},
		{"8% slower, threshold 5", scale(reversed(jitter), 1.08e6), 5, verdictRegression},
		{"20% faster", scale(jitter, 0.8e6), 10, verdictFaster},
		{"same", scale(reversed(jitter), 1e6), 10, verdictSame},
		{"noisy but equal", noisyAgain, 10, verdictSame},
		{"noisy and 15% slower", scale(noisyAgain, 1.15), 10, verdictRegression},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := base
			if strings.HasPrefix(c.name, "noisy") {
				b = noisy
			}
			opt := DefaultOptions
			opt.Threshold = c.threshold
			rep := Compare(file(nil, bench("BenchmarkWrite/ascii", "ns/op", b)),
				file(nil, bench("BenchmarkWrite/ascii", "ns/op", c.cur)), opt)
			if got := rep.Rows[0].Verdict; got != c.want {
				t.Errorf("verdict %q (delta %+.1f%%, p %.4f), want %q", got, rep.Rows[0].Delta, rep.Rows[0].P, c.want)
			}
			if wantFail := c.want == verdictRegression; (rep.Regressions > 0) != wantFail {
				t.Errorf("regressions = %d", rep.Regressions)
			}
		})
	}
}

// sampleRun draws n wall times around mean: ±5% jitter, and in one run of
// ten another process holds the core for 10-50% of the time.
func sampleRun(rng *rand.Rand, n int, mean float64) []float64 {
	xs := make([]float64, n)
	for i := range xs {
		xs[i] = mean * (0.95 + 0.1*rng.Float64())
		if rng.IntN(10) == 0 {
			xs[i] *= 1.1 + 0.4*rng.Float64()
		}
	}
	return xs
}

// TestCompareErrorRates measures the decision rule on simulated runs of
// ten samples: equal code must almost never fail, and nine in ten 15%
// slowdowns must fail. A slowdown only just above the threshold is missed
// when outliers raise the baseline's median.
func TestCompareErrorRates(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	const trials = 1000
	falseFails, caught := 0, 0
	for range trials {
		base := sampleRun(rng, 10, 1e6)
		same := Compare(file(nil, bench("BenchmarkX", "ns/op", base)), file(nil, bench("BenchmarkX", "ns/op", sampleRun(rng, 10, 1e6))), DefaultOptions)
		falseFails += same.Regressions
		slow := Compare(file(nil, bench("BenchmarkX", "ns/op", base)), file(nil, bench("BenchmarkX", "ns/op", sampleRun(rng, 10, 1.15e6))), DefaultOptions)
		caught += slow.Regressions
	}
	if rate := float64(falseFails) / trials; rate > 0.002 {
		t.Errorf("equal code failed in %.2f%% of trials", rate*100)
	}
	if rate := float64(caught) / trials; rate < 0.9 {
		t.Errorf("a 15%% slowdown was caught in only %.1f%% of trials", rate*100)
	}
	t.Logf("false failures %d/%d, 15%% slowdowns caught %d/%d", falseFails, trials, caught, trials)
}

// A run that shared its core shows in wall time, not in CPU time: the
// comparison uses CPU time when both files have it.
func TestComparePrefersCPUTime(t *testing.T) {
	base := bench("BenchmarkReflow/x", "ns/op", noisy, "cpu-ns/op", noisy)
	cur := bench("BenchmarkReflow/x", "ns/op", scale(noisyAgain, 1.5), "cpu-ns/op", noisyAgain)
	rep := Compare(file(nil, base), file(nil, cur), DefaultOptions)
	if r := rep.Rows[0]; r.Metric != "cpu-ns/op" || r.Verdict != verdictSame {
		t.Errorf("metric %q verdict %q, want cpu-ns/op and %q", r.Metric, r.Verdict, verdictSame)
	}
	// Without CPU time in the baseline, wall time decides.
	rep = Compare(file(nil, bench("BenchmarkReflow/x", "ns/op", noisy)), file(nil, cur), DefaultOptions)
	if r := rep.Rows[0]; r.Metric != "ns/op" || r.Verdict != verdictRegression {
		t.Errorf("metric %q verdict %q, want ns/op and %q", r.Metric, r.Verdict, verdictRegression)
	}
	rep = Compare(file(nil, bench("BenchmarkY", "MB/s", noisy)), file(nil, bench("BenchmarkY", "MB/s", noisy)), DefaultOptions)
	if r := rep.Rows[0]; r.Verdict != verdictNoMetric || rep.Regressions != 0 {
		t.Errorf("no common time unit: verdict %q", r.Verdict)
	}
}

func TestCompareAddedAndRemoved(t *testing.T) {
	base := file(nil, bench("BenchmarkGone", "ns/op", noisy), bench("BenchmarkKept", "ns/op", noisy))
	cur := file(nil, bench("BenchmarkKept", "ns/op", noisyAgain), bench("BenchmarkAdded", "ns/op", scale(noisy, 3)))
	rep := Compare(base, cur, DefaultOptions)
	want := map[string]string{"vt10x Kept": verdictSame, "vt10x Added": verdictNew, "vt10x Gone": verdictMissing}
	if got := verdicts(rep); !maps.Equal(got, want) {
		t.Errorf("verdicts = %v, want %v", got, want)
	}
	if rep.Regressions != 0 {
		t.Error("an added or removed benchmark failed the comparison")
	}
	var out bytes.Buffer
	if err := rep.Write(&out, "base.txt"); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"1 new, 1 missing", "ok: no benchmark slower"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("output lacks %q:\n%s", s, out.String())
		}
	}
}

// reversed returns xs in reverse order: the same values, measured in
// another order.
func reversed(xs []float64) []float64 {
	out := slices.Clone(xs)
	slices.Reverse(out)
	return out
}

func TestCompareTooFewSamples(t *testing.T) {
	rep := Compare(file(nil, bench("BenchmarkX", "ns/op", []float64{1, 1.01, 0.99})),
		file(nil, bench("BenchmarkX", "ns/op", []float64{2, 2.01, 1.99})), DefaultOptions)
	if v := rep.Rows[0].Verdict; v != verdictFew || rep.Regressions != 0 {
		t.Errorf("3+3 samples, twice as slow: verdict %q, regressions %d", v, rep.Regressions)
	}
	if !slices.ContainsFunc(rep.Warnings, func(s string) bool { return strings.Contains(s, "at least 4 runs") }) {
		t.Errorf("warnings = %q", rep.Warnings)
	}
}

func TestCompareWarnings(t *testing.T) {
	baseCfg := map[string]string{
		"go": "go1.26.10", "cpu-model": "Fast CPU", "kernel": "7.2", "pinned-cpu": "0",
		"cpu-epp": "performance", "cpu-mhz": "5100", "loadavg-max": "1.2", "core-share-min": "0.99",
		"suite-vt10x": "bench=. benchtime=500ms gomaxprocs=1",
	}
	curCfg := map[string]string{
		"go": "go1.27.0", "cpu-model": "Slow CPU", "kernel": "7.2", "pinned-cpu": "0",
		"cpu-epp": "balance_power", "cpu-mhz": "4500", "loadavg-max": "9.5", "core-share-min": "0.62",
		"suite-vt10x": "bench=. benchtime=100ms gomaxprocs=1",
		"smt-sibling": "12", "sibling-busy-median": "0.60", "disturbed-runs": "3 of 40",
		"skipped": "gui-frame (build failed: no gtk4)",
		"not-run": "internal/vt10x BenchmarkNew",
	}
	base := bench("BenchmarkX", "ns/op", noisy, "allocs/op", []float64{0, 0, 0})
	cur := bench("BenchmarkX", "ns/op", noisyAgain, "allocs/op", []float64{28, 28, 28})
	rep := Compare(file(baseCfg, base), file(curCfg, cur), DefaultOptions)
	var out bytes.Buffer
	if err := rep.Write(&out, "base.txt"); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		`Go version differs: baseline "go1.26.10", current "go1.27.0"`,
		`CPU differs: baseline "Fast CPU", current "Slow CPU"`,
		`energy-performance preference (power profile) differs`,
		`settings of suite vt10x differs`,
		`pinned core ran at 4500 MHz, the baseline at 5100 MHz: load on other cores lowers the clock, and compute-bound times grow by as much`,
		`loaded during the current run: load average up to 9.50`,
		`shared the pinned core during the current run: a run got only 62%`,
		`allocation-free benchmarks now allocate: vt10x X 0 → 28`,
		`3 of 40 runs of the current run were kept although disturbed`,
		`SMT sibling (CPU 12) was 60% busy during the current run`,
		`note: skipped: gui-frame (build failed: no gtk4)`,
		`note: benchmarks no suite selects: internal/vt10x BenchmarkNew`,
	} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("output lacks %q", s)
		}
	}
	for _, s := range []string{"kernel differs", "during the baseline"} {
		if strings.Contains(out.String(), s) {
			t.Errorf("output has %q", s)
		}
	}
	if t.Failed() {
		t.Log(out.String())
	}
	// Warnings never fail the comparison by themselves.
	if rep.Regressions != 0 {
		t.Error("warnings alone failed the comparison")
	}

	// Allocations amortised over b.N grow with ns/op: 60 → 122 is no warning.
	base.Values["allocs/op"], cur.Values["allocs/op"] = []float64{60, 60}, []float64{122, 122}
	if rep := Compare(file(nil, base), file(nil, cur), DefaultOptions); len(rep.Warnings) != 0 {
		t.Errorf("warnings = %q", rep.Warnings)
	}
}

func TestFormat(t *testing.T) {
	for _, c := range []struct {
		v    float64
		unit string
		want string
	}{
		{88176, "ns/op", "88.18µs"},
		{1753102, "ns/op", "1.753ms"},
		{49188023, "cpu-ns/op", "49.19ms"},
		{2.5e9, "ns/op", "2.5s"},
		{512, "ns/op", "512ns"},
		{28, "allocs/op", "28"},
	} {
		if got := fmtValue(c.v, c.unit); got != c.want {
			t.Errorf("fmtValue(%g, %q) = %q, want %q", c.v, c.unit, got, c.want)
		}
	}
}

func writeFile(t *testing.T, dir, name, data string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// benchText renders samples as go test output.
func benchText(name string, ns []float64) string {
	var b strings.Builder
	b.WriteString("pkg: bunk/internal/vt10x\n")
	for _, v := range ns {
		fmt.Fprintf(&b, "%s \t 100\t %.0f ns/op\n", name, v)
	}
	return b.String()
}

func TestCompareCommand(t *testing.T) {
	dir := t.TempDir()
	base := writeFile(t, dir, "base.txt", benchText("BenchmarkWrite/ascii", noisy))
	same := writeFile(t, dir, "same.txt", benchText("BenchmarkWrite/ascii", noisyAgain))
	slow := writeFile(t, dir, "slow.txt", benchText("BenchmarkWrite/ascii", scale(noisyAgain, 1.15)))
	empty := writeFile(t, dir, "empty.txt", "PASS\n")
	for _, c := range []struct {
		name string
		args []string
		code int
		want string
	}{
		{"equal", []string{"compare", base, same}, exitOK, "ok: no benchmark slower by more than 10%"},
		{"15% slower", []string{"compare", base, slow}, exitRegression, "FAIL: 1 benchmark(s) slower by more than 10%"},
		{"15% slower, threshold 20", []string{"compare", "-threshold", "20", base, slow}, exitOK, "ok: no benchmark slower by more than 20%"},
		{"no baseline", []string{"compare", filepath.Join(dir, "missing.txt"), same}, exitError, "make bench-baseline"},
		{"no results", []string{"compare", base, empty}, exitError, "no benchmark results"},
		{"one file", []string{"compare", base}, exitError, "want BASELINE and CURRENT"},
		{"bad alpha", []string{"compare", "-alpha", "0", base, same}, exitError, "alpha"},
		{"unknown command", []string{"frobnicate"}, exitError, "unknown command"},
		{"run without output", []string{"run", "-count", "3"}, exitError, "want -o FILE"},
		{"run no rounds", []string{"run", "-count", "0", "-o", filepath.Join(dir, "x.txt")}, exitError, "at least 1"},
		{"run unknown suite", []string{"run", "-suites", "nope", "-o", filepath.Join(dir, "x.txt")}, exitError, "have vt10x, session-output"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := cli(c.args, &stdout, &stderr); code != c.code {
				t.Errorf("exit %d, want %d\n%s%s", code, c.code, stdout.String(), stderr.String())
			}
			if !strings.Contains(stdout.String()+stderr.String(), c.want) {
				t.Errorf("output lacks %q:\n%s%s", c.want, stdout.String(), stderr.String())
			}
		})
	}
}
