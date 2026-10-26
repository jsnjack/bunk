package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"maps"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// File holds one benchmark file: `go test -bench` output, optionally
// preceded by the header that `benchcheck run` writes.
type File struct {
	Config  map[string]string // first value of each "key: value" line
	Benches map[string]*Bench // by Bench.Key
	Order   []string          // keys in the order they first appear
}

// Bench holds every sample of one benchmark, by unit ("ns/op", "B/op", ...).
type Bench struct {
	Pkg, Name string
	Values    map[string][]float64
}

// Key identifies a benchmark across files: package and full name.
func (b *Bench) Key() string { return b.Pkg + " " + b.Name }

// Label is the short name the table shows, such as "vt10x Write/ascii".
func (b *Bench) Label() string {
	pkg := b.Pkg
	if i := strings.LastIndex(pkg, "/"); i >= 0 {
		pkg = pkg[i+1:]
	}
	name := strings.TrimPrefix(b.Name, "Benchmark")
	if pkg == "" {
		return name
	}
	return pkg + " " + name
}

var configLine = regexp.MustCompile(`^([a-z][^\s:]*): (.*)$`)

// Parse reads a benchmark file. Lines that are neither configuration nor
// benchmark results (PASS, ok, comments, a parent benchmark's bare name)
// are ignored. A benchmark's name keeps any "-N" GOMAXPROCS suffix, so
// runs with different GOMAXPROCS do not match.
func Parse(r io.Reader) (*File, error) {
	f := &File{Config: map[string]string{}, Benches: map[string]*Bench{}}
	pkg := ""
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t\r")
		if m := configLine.FindStringSubmatch(line); m != nil {
			if m[1] == "pkg" {
				pkg = m[2]
			}
			if _, ok := f.Config[m[1]]; !ok {
				f.Config[m[1]] = strings.TrimSpace(m[2])
			}
			continue
		}
		if !strings.HasPrefix(line, "Benchmark") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 || len(fields)%2 != 0 {
			continue
		}
		if _, err := strconv.ParseUint(fields[1], 10, 64); err != nil {
			continue
		}
		b := &Bench{Pkg: pkg, Name: fields[0]}
		if old, ok := f.Benches[b.Key()]; ok {
			b = old
		} else {
			b.Values = map[string][]float64{}
			f.Benches[b.Key()] = b
			f.Order = append(f.Order, b.Key())
		}
		for i := 2; i+1 < len(fields); i += 2 {
			v, err := strconv.ParseFloat(fields[i], 64)
			if err != nil {
				return nil, fmt.Errorf("%s: bad value %q", fields[0], fields[i])
			}
			b.Values[fields[i+1]] = append(b.Values[fields[i+1]], v)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read results: %w", err)
	}
	return f, nil
}

// median returns the median of xs, or NaN for no samples.
func median(xs []float64) float64 { return quantile(xs, 0.5) }

// quantile returns the q-quantile of xs by linear interpolation between
// order statistics, or NaN for no samples.
func quantile(xs []float64, q float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	pos := q * float64(len(s)-1)
	lo := int(math.Floor(pos))
	hi := min(lo+1, len(s)-1)
	return s[lo] + (s[hi]-s[lo])*(pos-float64(lo))
}

// exactLimit is the pooled sample size up to which mannWhitney counts the
// exact distribution; above it the normal approximation is close enough.
const exactLimit = 60

// mannWhitney returns the two-sided p-value of the Mann-Whitney U test:
// the chance that samples a and b, drawn from one distribution, differ in
// rank as much as they do. Ties get mid-ranks. Up to exactLimit samples in
// total, the p-value is exact, ties included.
func mannWhitney(a, b []float64) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 1
	}
	r := rankSamples(a, b)
	if len(r.ranks) > exactLimit {
		return r.normalP()
	}
	return r.exactP()
}

// ranked holds the pooled samples of a U test as doubled mid-ranks, which
// keep tied ranks whole numbers: positions i..j (0-based) of the sorted
// pool share rank (i+1+j+1)/2, doubled i+j+2.
type ranked struct {
	ranks   []int   // doubled rank of each pooled sample
	n1, n2  int     // sizes of a and b
	w       int     // doubled rank sum of a
	tieTerm float64 // sum of t³-t over groups of t tied values
}

func rankSamples(a, b []float64) ranked {
	type obs struct {
		v     float64
		fromA bool
	}
	pool := make([]obs, 0, len(a)+len(b))
	for _, v := range a {
		pool = append(pool, obs{v, true})
	}
	for _, v := range b {
		pool = append(pool, obs{v, false})
	}
	sort.SliceStable(pool, func(i, j int) bool { return pool[i].v < pool[j].v })
	r := ranked{ranks: make([]int, len(pool)), n1: len(a), n2: len(b)}
	for i := 0; i < len(pool); {
		j := i
		for j+1 < len(pool) && pool[j+1].v == pool[i].v {
			j++
		}
		for k := i; k <= j; k++ {
			r.ranks[k] = i + j + 2
			if pool[k].fromA {
				r.w += i + j + 2
			}
		}
		t := float64(j - i + 1)
		r.tieTerm += t*t*t - t
		i = j + 1
	}
	return r
}

// dist is how far a's doubled rank sum lies from its expected value.
func (r ranked) dist() int { return abs(r.w - r.n1*(len(r.ranks)+1)) }

// exactP counts every way of splitting the pooled ranks into groups of n1
// and n2, and returns the fraction whose rank sum lies at least as far from
// the expected value as the observed one.
func (r ranked) exactP() float64 {
	n, mean, dist := len(r.ranks), r.n1*(len(r.ranks)+1), r.dist()
	// ways[k][s]: subsets of k pooled samples whose doubled ranks sum to s.
	maxSum := n * (n + 1)
	ways := make([][]float64, r.n1+1)
	for k := range ways {
		ways[k] = make([]float64, maxSum+1)
	}
	ways[0][0] = 1
	for i, rank := range r.ranks {
		for k := min(i+1, r.n1); k >= 1; k-- {
			prev, cur := ways[k-1], ways[k]
			// k doubled ranks of at most 2n each sum to at most 2nk.
			for s := min(maxSum, 2*n*k); s >= rank; s-- {
				cur[s] += prev[s-rank]
			}
		}
	}
	var total, extreme float64
	for s, c := range ways[r.n1] {
		total += c
		if abs(s-mean) >= dist {
			extreme += c
		}
	}
	return min(1, extreme/total)
}

// normalP approximates the p-value with the normal distribution of U,
// corrected for ties and continuity.
func (r ranked) normalP() float64 {
	n, n1, n2 := float64(len(r.ranks)), float64(r.n1), float64(r.n2)
	variance := n1 * n2 / 12 * ((n + 1) - r.tieTerm/(n*(n-1)))
	if variance <= 0 {
		return 1
	}
	z := (float64(r.dist())/2 - 0.5) / math.Sqrt(variance)
	if z <= 0 {
		return 1
	}
	return min(1, math.Erfc(z/math.Sqrt2))
}

// minPValue is the smallest p-value mannWhitney can return for samples of
// n1 and n2 values: both groups fully separated, in either direction.
func minPValue(n1, n2 int) float64 {
	if n1 == 0 || n2 == 0 {
		return 1
	}
	c := 1.0 // C(n1+n2, n1)
	for i := 1; i <= n1; i++ {
		c = c * float64(n2+i) / float64(i)
	}
	return min(1, 2/c)
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// Options set when a slowdown fails the comparison.
type Options struct {
	Threshold float64 // percent the median may grow before it fails
	Alpha     float64 // significance level of the U test
	MaxLoad   float64 // 1-minute load average above which a run is "loaded"
	MinShare  float64 // CPU time / wall time below which a run shared its core
	MaxClock  float64 // percent the pinned core's clock may differ
	// MaxSibling is the median busy fraction of the pinned core's SMT
	// sibling above which a run is reported as disturbed.
	MaxSibling float64
}

// DefaultOptions are the comparison settings `make bench-check` starts from.
var DefaultOptions = Options{Threshold: 10, Alpha: 0.05, MaxLoad: 4, MinShare: 0.9, MaxClock: 5, MaxSibling: 0.25}

// Verdicts of one comparison row.
const (
	verdictSame       = "~"
	verdictSlower     = "slower"
	verdictFaster     = "faster"
	verdictRegression = "REGRESSION"
	verdictNew        = "new"
	verdictMissing    = "missing"
	verdictFew        = "too few samples"
	verdictNoMetric   = "no time metric"
)

// Row compares one benchmark.
type Row struct {
	Label, Metric string
	Base, Cur     []float64
	BaseMed       float64
	CurMed        float64
	Delta         float64 // percent change of the median, positive = slower
	P             float64
	Verdict       string
}

// Report is the outcome of Compare.
type Report struct {
	Rows        []Row
	Notes       []string // what the run did not cover
	Warnings    []string // why the numbers may be unreliable
	Regressions int
	Opt         Options
}

// timeMetric picks the unit to compare: CPU time when both files report
// it (it does not grow while another process holds the core), else wall
// time. It returns "" when the files share neither.
func timeMetric(base, cur *Bench) string {
	for _, unit := range []string{"cpu-ns/op", "ns/op"} {
		if len(base.Values[unit]) > 0 && len(cur.Values[unit]) > 0 {
			return unit
		}
	}
	return ""
}

// Compare compares every benchmark of cur with base. A benchmark is a
// regression when the U test finds the samples differ (p < Alpha) and the
// median time grew by more than Threshold percent. Benchmarks present in
// only one file are reported as new or missing, never as failures.
func Compare(base, cur *File, opt Options) Report {
	rep := Report{Opt: opt}
	keys := slices.Clone(cur.Order)
	for _, k := range base.Order {
		if _, ok := cur.Benches[k]; !ok {
			keys = append(keys, k)
		}
	}
	var allocs []string
	for _, k := range keys {
		b, c := base.Benches[k], cur.Benches[k]
		switch {
		case b == nil:
			rep.Rows = append(rep.Rows, Row{Label: c.Label(), Verdict: verdictNew})
			continue
		case c == nil:
			rep.Rows = append(rep.Rows, Row{Label: b.Label(), Verdict: verdictMissing})
			continue
		}
		row := Row{Label: c.Label(), Metric: timeMetric(b, c)}
		if row.Metric == "" {
			row.Verdict = verdictNoMetric
			rep.Rows = append(rep.Rows, row)
			continue
		}
		row.Base, row.Cur = b.Values[row.Metric], c.Values[row.Metric]
		row.BaseMed, row.CurMed = median(row.Base), median(row.Cur)
		if row.BaseMed > 0 {
			row.Delta = (row.CurMed - row.BaseMed) / row.BaseMed * 100
		}
		row.P = mannWhitney(row.Base, row.Cur)
		switch {
		case minPValue(len(row.Base), len(row.Cur)) >= opt.Alpha:
			row.Verdict = verdictFew
		case row.P >= opt.Alpha:
			row.Verdict = verdictSame
		case row.Delta > opt.Threshold:
			row.Verdict = verdictRegression
			rep.Regressions++
		case row.Delta > 0:
			row.Verdict = verdictSlower
		default:
			row.Verdict = verdictFaster
		}
		rep.Rows = append(rep.Rows, row)
		// Allocations made once and amortised over b.N grow with ns/op, so
		// only a path that stopped being allocation-free is reported.
		if ba, ca := median(b.Values["allocs/op"]), median(c.Values["allocs/op"]); ba == 0 && ca > 0 {
			allocs = append(allocs, fmt.Sprintf("%s %s → %s", row.Label, fmtNum(ba), fmtNum(ca)))
		}
	}
	if len(allocs) > 0 {
		rep.Warnings = append(rep.Warnings, "allocation-free benchmarks now allocate: "+strings.Join(allocs, ", "))
	}
	for _, r := range rep.Rows {
		if r.Verdict == verdictFew {
			n := 2
			for minPValue(n, n) >= opt.Alpha {
				n++
			}
			rep.Warnings = append(rep.Warnings, fmt.Sprintf(
				"too few samples to reach p < %g: record and compare at least %d runs (BENCH_COUNT)", opt.Alpha, n))
			break
		}
	}
	rep.Notes, rep.Warnings = headerNotes(base.Config, cur.Config, opt, rep.Notes, rep.Warnings)
	return rep
}

// headerNotes compares the two files' headers: settings that change every
// result, and signs that a run shared its core.
func headerNotes(base, cur map[string]string, opt Options, notes, warns []string) ([]string, []string) {
	same := func(key, what string) {
		b, c := base[key], cur[key]
		if b != "" && c != "" && b != c {
			warns = append(warns, fmt.Sprintf("%s differs: baseline %q, current %q", what, b, c))
		}
	}
	if base["cpu-model"] == "" || cur["cpu-model"] == "" {
		same("cpu", "CPU")
	}
	same("cpu-model", "CPU")
	same("go", "Go version")
	same("pinned-cpu", "pinned CPU")
	for _, k := range slices.Sorted(maps.Keys(cur)) {
		if name, ok := strings.CutPrefix(k, "suite-"); ok {
			same(k, "settings of suite "+name)
		}
	}
	same("cpu-governor", "CPU governor")
	same("cpu-epp", "energy-performance preference (power profile)")
	same("kernel", "kernel")
	if b, c := num(base["cpu-mhz"]), num(cur["cpu-mhz"]); b > 0 && c > 0 && math.Abs(c-b)/b*100 > opt.MaxClock {
		// Within one run, compute-bound times follow the clock. Between
		// runs, other load (shared L3, memory bandwidth) shifts them by a
		// further 5-7%, so times are not scaled by the clock.
		warns = append(warns, fmt.Sprintf(
			"the pinned core ran at %.0f MHz, the baseline at %.0f MHz: load on other cores lowers the clock, and compute-bound times grow by as much", c, b))
	}
	for _, f := range []struct {
		name string
		cfg  map[string]string
	}{{"baseline", base}, {"current run", cur}} {
		if l := num(f.cfg["loadavg-max"]); l > opt.MaxLoad {
			warns = append(warns, fmt.Sprintf(
				"machine was loaded during the %s: load average up to %.2f (limit %g); rerun on a quiet machine before trusting a change",
				f.name, l, opt.MaxLoad))
		}
		if s := num(f.cfg["core-share-min"]); s > 0 && s < opt.MinShare {
			warns = append(warns, fmt.Sprintf(
				"another process shared the pinned core during the %s: a run got only %.0f%% of it", f.name, s*100))
		}
		if n := num(f.cfg["disturbed-runs"]); n > 0 {
			warns = append(warns, fmt.Sprintf(
				"%s runs of the %s were kept although disturbed (shared core, low clock or busy SMT sibling)",
				f.cfg["disturbed-runs"], f.name))
		}
		if s := num(f.cfg["sibling-busy-median"]); s > opt.MaxSibling {
			warns = append(warns, fmt.Sprintf(
				"the pinned core's SMT sibling (CPU %s) was %.0f%% busy during the %s: it shares the core's caches and execution units",
				f.cfg["smt-sibling"], s*100, f.name))
		}
	}
	if s := cur["skipped"]; s != "" {
		notes = append(notes, "skipped: "+s)
	}
	if s := cur["not-run"]; s != "" {
		notes = append(notes, "benchmarks no suite selects: "+s)
	}
	return notes, warns
}

func num(s string) float64 {
	f, err := strconv.ParseFloat(strings.Fields(s + " x")[0], 64)
	if err != nil {
		return 0
	}
	return f
}

// fmtNum prints a count or other plain number with up to 4 significant digits.
func fmtNum(v float64) string { return strconv.FormatFloat(v, 'g', 4, 64) }

// fmtValue prints a value of the given unit; times get a unit prefix.
func fmtValue(v float64, unit string) string {
	if !strings.HasSuffix(unit, "ns/op") {
		return fmtNum(v)
	}
	switch {
	case v >= 1e9:
		return fmtNum(v/1e9) + "s"
	case v >= 1e6:
		return fmtNum(v/1e6) + "ms"
	case v >= 1e3:
		return fmtNum(v/1e3) + "µs"
	}
	return fmtNum(v) + "ns"
}

// spread is half the interquartile range as a percentage of the median.
func spread(xs []float64) string {
	m := median(xs)
	if m == 0 || len(xs) < 2 {
		return ""
	}
	return fmt.Sprintf(" ±%.1f%%", (quantile(xs, 0.75)-quantile(xs, 0.25))/2/m*100)
}

// Write prints the table, notes, warnings and the final verdict line.
func (r Report) Write(w io.Writer, baseName string) error {
	table := [][]string{{"benchmark", "metric", "baseline", "current", "delta", "p", "verdict"}}
	counts := map[string]int{}
	for _, row := range r.Rows {
		counts[row.Verdict]++
		if row.Metric == "" {
			table = append(table, []string{row.Label, "", "", "", "", "", row.Verdict})
			continue
		}
		table = append(table, []string{row.Label, row.Metric,
			fmtValue(row.BaseMed, row.Metric) + spread(row.Base),
			fmtValue(row.CurMed, row.Metric) + spread(row.Cur),
			fmt.Sprintf("%+.1f%%", row.Delta), fmt.Sprintf("%.3f", row.P), row.Verdict})
	}
	var b bytes.Buffer
	writeTable(&b, table)
	b.WriteByte('\n')
	for _, n := range r.Notes {
		fmt.Fprintln(&b, "note:", n)
	}
	for _, s := range r.Warnings {
		fmt.Fprintln(&b, "warning:", s)
	}
	fmt.Fprintf(&b, "compared with %s: %d regression(s), %d slower within %g%%, %d faster, %d unchanged, %d new, %d missing\n",
		baseName, counts[verdictRegression], counts[verdictSlower], r.Opt.Threshold, counts[verdictFaster],
		counts[verdictSame]+counts[verdictFew], counts[verdictNew], counts[verdictMissing])
	if r.Regressions > 0 {
		fmt.Fprintf(&b, "FAIL: %d benchmark(s) slower by more than %g%% (p < %g)\n", r.Regressions, r.Opt.Threshold, r.Opt.Alpha)
	} else {
		fmt.Fprintf(&b, "ok: no benchmark slower by more than %g%%\n", r.Opt.Threshold)
	}
	if _, err := w.Write(b.Bytes()); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	return nil
}

// writeTable writes rows as left-aligned columns two spaces apart.
func writeTable(b *bytes.Buffer, rows [][]string) {
	var widths []int
	for _, row := range rows {
		for i, cell := range row {
			if i == len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], utf8.RuneCountInString(cell))
		}
	}
	for _, row := range rows {
		line := ""
		for i, cell := range row {
			if i < len(row)-1 {
				cell += strings.Repeat(" ", widths[i]-utf8.RuneCountInString(cell)+2)
			}
			line += cell
		}
		b.WriteString(strings.TrimRight(line, " "))
		b.WriteByte('\n')
	}
}
