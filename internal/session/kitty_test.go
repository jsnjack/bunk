package session

import "testing"

func TestAuditKittySetOperations(t *testing.T) {
	p := newTestSession(10, 2)
	p.handleKittyKeyboard([]byte("\x1b[=1u\x1b[=4;2u"))
	if len(p.KittyStack) != 1 || p.KittyStack[0] != 5 {
		t.Fatalf("set bits: %v", p.KittyStack)
	}
	p.handleKittyKeyboard([]byte("\x1b[=1;3u"))
	if p.KittyStack[0] != 4 {
		t.Fatalf("clear bits: %v", p.KittyStack)
	}
	p.handleKittyKeyboard([]byte("\x1b]2;title\x1b[=9u\a"))
	if p.KittyStack[0] != 4 {
		t.Fatal("title changed keyboard negotiation")
	}
}

func TestRegressionKittyOwnership(t *testing.T) {
	for _, tc := range []struct {
		name              string
		owner, foreground int
		wantEnabled       bool
	}{
		{"current app", 12, 12, true},
		{"exited app", 12, 11, false},
		{"new app negotiated before poll", 13, 13, true},
		{"foreground unavailable", 12, 0, true},
		{"owner unavailable", 0, 12, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &Session{KittyStack: []int{1}, KittyOwnerPGID: tc.owner}
			p.clearStaleKittyMode(tc.foreground)
			if got := len(p.KittyStack) > 0; got != tc.wantEnabled {
				t.Fatalf("enabled = %v, want %v", got, tc.wantEnabled)
			}
		})
	}
}
