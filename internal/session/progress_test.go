package session

import "testing"

func TestParseProgress(t *testing.T) {
	prev := Progress{State: ProgressNormal, Percent: 40}
	for _, tc := range []struct {
		in   string
		want Progress
		ok   bool
	}{
		{"4;0", Progress{}, true},
		{"4;0;77", Progress{}, true},
		{"4;1;42", Progress{ProgressNormal, 42}, true},
		{"4;1", Progress{ProgressNormal, 0}, true},
		{"4;1;250", Progress{ProgressNormal, 100}, true},
		{"4;1;-3", Progress{ProgressNormal, 0}, true},
		{"4;2", Progress{ProgressError, 40}, true}, // keeps the percentage
		{"4;2;90", Progress{ProgressError, 90}, true},
		{"4;3", Progress{ProgressIndeterminate, 0}, true},
		{"4;3;60", Progress{ProgressIndeterminate, 0}, true},
		{"4;4", Progress{ProgressPaused, 40}, true},
		{"4;5;10", Progress{}, false},
		{"4;x", Progress{}, false},
		{"4;1;x", Progress{}, false},
		{"4", Progress{}, false},
	} {
		got, ok := parseProgress(tc.in, prev)
		if ok != tc.ok || got != tc.want {
			t.Errorf("parseProgress(%q) = %+v, %v; want %+v, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
	if _, ok := parseStatusOSC(9, []byte("4;1;10")); !ok {
		t.Error("OSC 9;4 is not read as a progress report")
	}
}

// TestProgressStatus: a progress report shows in Status, is announced
// once, and ends when the program clears it or leaves the foreground.
// TestProgressBeforeFirstPoll: the shell reports progress before the
// first poll has seen it; the poll finding the shell keeps it.
func TestProgressBeforeFirstPoll(t *testing.T) {
	s := newTestSession(20, 5)
	const shell = 100
	s.onStatusOSC(9, []byte("4;1;60"))
	s.refreshStatus(shell, shell, "bash", true)
	if got := s.Status().Progress; got != (Progress{ProgressNormal, 60}) {
		t.Fatalf("the first poll dropped the shell's progress: %+v", got)
	}
	s.onStatusOSC(9, []byte("4;2"))
	if got := s.Status().Progress; got != (Progress{ProgressError, 60}) {
		t.Errorf("after 9;4;2: %+v, want failed at 60%%", got)
	}
}

func TestProgressStatus(t *testing.T) {
	s := newTestSession(20, 5)
	const shell, prog = 100, 200
	s.refreshStatus(shell, prog, "make", true) // the poll sees the program
	s.agentChangedNow()

	s.onStatusOSC(9, []byte("4;1;30"))
	if got := s.Status().Progress; got != (Progress{ProgressNormal, 30}) {
		t.Fatalf("after 9;4;1;30: %+v", got)
	}
	if !s.agentChangedNow() || s.agentChangedNow() {
		t.Error("a progress report is not announced exactly once")
	}
	s.onStatusOSC(9, []byte("4;0"))
	if got := s.Status().Progress; got != (Progress{}) {
		t.Errorf("after 9;4;0: %+v", got)
	}

	s.onStatusOSC(9, []byte("4;3"))
	s.refreshStatus(shell, prog, "make", false)
	if got := s.Status().Progress.State; got != ProgressIndeterminate {
		t.Errorf("the same foreground group dropped the progress: %v", got)
	}
	if !s.refreshStatus(shell, shell, "bash", true) {
		t.Error("the program leaving did not report a change")
	}
	if got := s.Status().Progress; got != (Progress{}) {
		t.Errorf("progress outlived its program: %+v", got)
	}

	// Reported while the poll still saw the shell: the next group adopts it.
	s.onStatusOSC(9, []byte("4;1;5"))
	s.refreshStatus(shell, 300, "curl", true)
	if got := s.Status().Progress; got != (Progress{ProgressNormal, 5}) {
		t.Errorf("the program's first group dropped its progress: %+v", got)
	}
	s.refreshStatus(shell, shell, "bash", true)
	if got := s.Status().Progress; got != (Progress{}) {
		t.Errorf("progress outlived its program: %+v", got)
	}
}
