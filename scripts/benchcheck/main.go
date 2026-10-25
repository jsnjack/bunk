// Command benchcheck runs bunk's key benchmarks pinned to one core and
// compares them with a stored baseline. `make bench-check` and
// `make bench-baseline` drive it.
//
//	benchcheck run [flags]                 run the suites, write go test -bench output
//	benchcheck compare [flags] BASE CUR    print a table; exit 1 on a regression
//
// It uses only the standard library, so it runs without module downloads.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

func main() { os.Exit(cli(os.Args[1:], os.Stdout, os.Stderr)) }

// Exit codes.
const (
	exitOK         = 0
	exitRegression = 1
	exitError      = 2
)

func cli(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: benchcheck run|compare [flags]")
		return exitError
	}
	var err error
	code := exitOK
	switch args[0] {
	case "run":
		err = runCmd(args[1:], stderr)
	case "compare":
		code, err = compareCmd(args[1:], stdout, stderr)
	default:
		err = fmt.Errorf("unknown command %q: want run or compare", args[0])
	}
	if errors.Is(err, flag.ErrHelp) {
		return exitError
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "benchcheck:", err)
		return exitError
	}
	return code
}

func runCmd(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfg := RunConfig{Log: stderr}
	fs.StringVar(&cfg.Root, "root", ".", "repository root")
	fs.IntVar(&cfg.Count, "count", 10, "rounds; every suite runs once per round")
	fs.StringVar(&cfg.CPU, "cpu", "0", "taskset CPU list to pin to; empty runs unpinned")
	fs.StringVar(&cfg.BenchTime, "benchtime", "", "override every suite's -test.benchtime")
	fs.Float64Var(&cfg.MinShare, "min-share", DefaultOptions.MinShare, "repeat a run that got less of its core than this (CPU time / wall time)")
	fs.Float64Var(&cfg.MinClock, "min-clock", 0.8, "repeat a run whose core ran below this fraction of its top clock")
	fs.Float64Var(&cfg.MaxSibling, "max-sibling", 0.5, "repeat a run whose core's SMT sibling was busier than this")
	fs.IntVar(&cfg.Retries, "retries", 2, "repeats per run at most, for a disturbed run")
	fs.DurationVar(&cfg.Pause, "pause", time.Second, "wait before repeating a run")
	fs.Float64Var(&cfg.MaxLoad, "max-load", DefaultOptions.MaxLoad, "warn above this 1-minute load average")
	fs.StringVar(&cfg.Out, "o", "", "output file (required)")
	only := fs.String("suites", "", "comma-separated suite names; empty runs all")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if cfg.Out == "" || fs.NArg() > 0 {
		return errors.New("run: want -o FILE and no arguments")
	}
	if cfg.Count < 1 {
		return errors.New("run: -count must be at least 1")
	}
	suites, err := selectSuites(DefaultSuites, *only)
	if err != nil {
		return err
	}
	cfg.Suites, cfg.All = suites, DefaultSuites
	if cfg.CPU != "" {
		if _, err := exec.LookPath("taskset"); err != nil {
			_, _ = fmt.Fprintln(stderr, "warning: taskset not found: running unpinned, results are noisier")
			cfg.CPU = ""
		}
	}
	return Run(cfg)
}

// selectSuites returns the suites named in the comma-separated list, or
// all of them for an empty list.
func selectSuites(all []Suite, list string) ([]Suite, error) {
	if strings.TrimSpace(list) == "" {
		return all, nil
	}
	var out []Suite
	for _, name := range strings.Split(list, ",") {
		name = strings.TrimSpace(name)
		found := false
		for _, s := range all {
			if s.Name == name {
				out, found = append(out, s), true
			}
		}
		if !found {
			var names []string
			for _, s := range all {
				names = append(names, s.Name)
			}
			return nil, fmt.Errorf("unknown suite %q: have %s", name, strings.Join(names, ", "))
		}
	}
	return out, nil
}

func compareCmd(args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("compare", flag.ContinueOnError)
	fs.SetOutput(stderr)
	opt := DefaultOptions
	fs.Float64Var(&opt.Threshold, "threshold", opt.Threshold, "fail when a median time grows by more than this percentage")
	fs.Float64Var(&opt.Alpha, "alpha", opt.Alpha, "significance level of the Mann-Whitney U test")
	fs.Float64Var(&opt.MaxLoad, "max-load", opt.MaxLoad, "warn when a run's load average went above this")
	fs.Float64Var(&opt.MinShare, "min-share", opt.MinShare, "warn when a run got less of its core than this")
	fs.Float64Var(&opt.MaxClock, "max-clock", opt.MaxClock, "warn when the core clocks differ by more than this percentage")
	if err := fs.Parse(args); err != nil {
		return exitError, err
	}
	if fs.NArg() != 2 {
		return exitError, errors.New("compare: want BASELINE and CURRENT files")
	}
	if opt.Threshold < 0 || opt.Alpha <= 0 || opt.Alpha >= 1 {
		return exitError, errors.New("compare: want -threshold >= 0 and 0 < -alpha < 1")
	}
	base, err := parseFile(fs.Arg(0))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			err = fmt.Errorf("%w (record one with make bench-baseline)", err)
		}
		return exitError, err
	}
	cur, err := parseFile(fs.Arg(1))
	if err != nil {
		return exitError, err
	}
	if len(base.Order) == 0 || len(cur.Order) == 0 {
		return exitError, errors.New("compare: a file holds no benchmark results")
	}
	rep := Compare(base, cur, opt)
	if err := rep.Write(stdout, fs.Arg(0)); err != nil {
		return exitError, err
	}
	if rep.Regressions > 0 {
		return exitRegression, nil
	}
	return exitOK, nil
}

func parseFile(path string) (*File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck
	file, err := Parse(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return file, nil
}
