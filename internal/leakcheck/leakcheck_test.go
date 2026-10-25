package leakcheck

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// recorder is a TB that keeps the errors instead of failing the test.
type recorder struct{ errs []string }

func (r *recorder) Helper() {}
func (r *recorder) Errorf(format string, args ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}

func (r *recorder) has(substr string) bool {
	for _, e := range r.errs {
		if strings.Contains(e, substr) {
			return true
		}
	}
	return false
}

var quick = Limits{Settle: 50 * time.Millisecond, Heap: 4 << 20}

// TestCheckCatchesLeaks injects each kind of leak on purpose and checks
// that Check reports it, then removes it and checks that Check is quiet:
// the leak tests rely on these detectors.
func TestCheckCatchesLeaks(t *testing.T) {
	if !Supported() {
		t.Skip("no /proc")
	}
	tests := []struct {
		name   string
		want   string
		inject func(t *testing.T) (release func())
	}{
		{"unclosed fd", "file descriptors open", func(t *testing.T) func() {
			f, err := os.Open("/dev/null")
			if err != nil {
				t.Fatal(err)
			}
			return func() {
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{"stuck goroutine", "goroutines", func(t *testing.T) func() {
			stop := make(chan struct{})
			started := make(chan struct{})
			go func() {
				close(started)
				<-stop
			}()
			<-started
			return func() { close(stop) }
		}},
		{"zombie child", "zombie children", func(t *testing.T) func() {
			cmd := exec.Command("/bin/true")
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			// Without Wait the exited child stays a zombie.
			for deadline := time.Now().Add(5 * time.Second); Children()[cmd.Process.Pid].State != 'Z'; {
				if time.Now().After(deadline) {
					t.Fatal("child never became a zombie")
				}
				time.Sleep(5 * time.Millisecond)
			}
			return func() {
				if err := cmd.Wait(); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{"running child", "still running", func(t *testing.T) func() {
			cmd := exec.Command("/bin/sleep", "60")
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			return func() {
				if err := cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				if err := cmd.Wait(); err == nil {
					t.Fatal("killed sleep exited cleanly")
				}
			}
		}},
		{"heap growth", "live heap", func(t *testing.T) func() {
			held = make([]byte, 16<<20)
			for i := range held {
				held[i] = byte(i)
			}
			return func() { held = nil }
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := Take()
			release := tt.inject(t)
			var r recorder
			Check(&r, tt.name, base, quick)
			if !r.has(tt.want) {
				t.Errorf("Check missed the leak; reported %q", r.errs)
			}
			release()
			r = recorder{}
			Check(&r, tt.name, base, Limits{Settle: 5 * time.Second, Heap: quick.Heap})
			if len(r.errs) > 0 {
				t.Errorf("Check still reports after release: %q", r.errs)
			}
		})
	}
}

// held keeps the heap-growth allocation reachable.
var held []byte

func TestParseStat(t *testing.T) {
	tests := []struct {
		name, stat string
		ppid       int
		want       Proc
		ok         bool
	}{
		{"plain", "123 (sh) S 45 123 123 0", 45, Proc{'S', "sh"}, true},
		{"zombie", "9 (yes) Z 1 9 9 0", 1, Proc{'Z', "yes"}, true},
		{"name with parens and spaces", "7 (a (b) c) R 3 7 7", 3, Proc{'R', "a (b) c"}, true},
		{"truncated", "7 (sh", 0, Proc{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ppid, p, ok := parseStat([]byte(tt.stat))
			if ok != tt.ok || ppid != tt.ppid || p != tt.want {
				t.Fatalf("parseStat(%q) = %d, %+v, %v", tt.stat, ppid, p, ok)
			}
		})
	}
}
