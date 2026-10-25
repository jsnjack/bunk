package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// makeDryRun prints the commands make would run for the repository's
// Makefile, without running them.
func makeDryRun(t *testing.T, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("no make")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("make", append([]string{"--no-print-directory", "-n", "MONOVA="}, args...)...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func TestMakeBenchTargets(t *testing.T) {
	out := makeDryRun(t, "bench-check", "BENCH_THRESHOLD=7", "BENCH_COUNT=4", "BENCH_SUITES=vt10x")
	for _, want := range []string{
		"go run ./scripts/benchcheck run -count 4 -cpu '0' -max-load 4 -benchtime '' -suites 'vt10x' -o bin/bench-current.txt",
		"go run ./scripts/benchcheck compare -threshold 7 -max-load 4",
		"testdata/bench-baseline.txt bin/bench-current.txt",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("make bench-check lacks %q:\n%s", want, out)
		}
	}
	out = makeDryRun(t, "bench-baseline")
	if want := "run -count 10 -cpu '0' -max-load 4 -benchtime '' -suites '' -o testdata/bench-baseline.txt"; !strings.Contains(out, want) {
		t.Errorf("make bench-baseline lacks %q:\n%s", want, out)
	}
	if strings.Contains(out, "compare") {
		t.Errorf("make bench-baseline compares:\n%s", out)
	}
}

// Timings depend on machine load, so the gates never run benchmarks. The
// other long targets keep working next to the bench targets.
func TestGatesSkipBenchmarks(t *testing.T) {
	for _, target := range []string{"check", "check-all", "fuzz", "soak"} {
		for _, line := range strings.Split(makeDryRun(t, target), "\n") {
			if strings.Contains(line, "benchcheck run") || strings.Contains(line, " -bench ") || strings.Contains(line, " -bench=") {
				t.Errorf("make %s runs benchmarks: %s", target, line)
			}
		}
	}
}
