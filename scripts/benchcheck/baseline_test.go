package main

import (
	"fmt"
	"path"
	"strconv"
	"strings"
	"testing"
)

const baselinePath = "../../testdata/bench-baseline.txt"

// TestCommittedBaseline checks the baseline `make bench-check` compares
// with: a full header, enough samples for the U test, every suite covered,
// and suite settings equal to DefaultSuites. Change a suite's settings and
// this fails until `make bench-baseline` records a new baseline.
func TestCommittedBaseline(t *testing.T) {
	f, err := parseFile(baselinePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"bunk-bench", "commit", "date", "go", "kernel", "cpu-model", "pinned-cpu", "count", "loadavg-max", "core-share-min"} {
		if f.Config[key] == "" {
			t.Errorf("header lacks %s", key)
		}
	}
	count, err := strconv.Atoi(f.Config["count"])
	if err != nil || minPValue(count, count) >= DefaultOptions.Alpha {
		t.Fatalf("count %q: too few samples for p < %g", f.Config["count"], DefaultOptions.Alpha)
	}
	cfg := RunConfig{}
	for _, s := range DefaultSuites {
		got, ok := f.Config["suite-"+s.Name]
		if !ok {
			if s.Optional && strings.Contains(f.Config["skipped"], s.Name) {
				continue
			}
			t.Errorf("suite %s: not in the baseline", s.Name)
			continue
		}
		if want := fmt.Sprintf("bench=%s benchtime=%s gomaxprocs=%d", s.Bench, cfg.benchTime(s), s.Procs); got != want {
			t.Errorf("suite %s: baseline settings %q, DefaultSuites %q: run make bench-baseline", s.Name, got, want)
		}
		pkg := path.Join("bunk", s.Module, s.Pkg)
		n := 0
		for _, b := range f.Benches {
			top, _, _ := strings.Cut(b.Name, "/")
			if b.Pkg != pkg || !topLevelMatch(s.Bench, top) {
				continue
			}
			n++
			if got := len(b.Values["ns/op"]); got != count {
				t.Errorf("%s: %d samples, want %d", b.Label(), got, count)
			}
		}
		if n == 0 {
			t.Errorf("suite %s: no benchmark of %s in the baseline", s.Name, pkg)
		}
	}
}
