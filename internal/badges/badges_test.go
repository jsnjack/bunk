package badges_test

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/rivo/uniseg"

	"bunk/internal/badges"
	"bunk/internal/badges/badgetest"
	"bunk/internal/session"
)

// TestLayoutCases: every shared case lays out as both front ends expect.
func TestLayoutCases(t *testing.T) {
	for _, c := range badgetest.Cases {
		t.Run(c.Name, func(t *testing.T) {
			got := badges.Layout(c.In)
			if !slices.Equal(got.Badges, c.Want) || got.Inset != c.Inset {
				t.Errorf("Layout = %q inset %d, want %q inset %d", got.Badges, got.Inset, c.Want, c.Inset)
			}
		})
	}
}

// TestCasesCoverEveryRole: the shared cases show each role at least once,
// so both front ends' parity tests draw every badge kind.
func TestCasesCoverEveryRole(t *testing.T) {
	seen := map[badges.Role]bool{}
	for _, c := range badgetest.Cases {
		for _, b := range c.Want {
			seen[b.Role] = true
		}
	}
	for _, r := range badges.Roles {
		if !seen[r] {
			t.Errorf("no case shows role %s", r)
		}
		if _, ok := badgetest.DefaultLooks[r]; !ok {
			t.Errorf("no default look for role %s", r)
		}
	}
}

// kind is the badge a role belongs to, in right-to-left order.
func kind(r badges.Role) int {
	switch r {
	case badges.RolePass:
		return 0
	case badges.RoleScroll:
		return 1
	case badges.RoleWorking, badges.RoleIdle, badges.RoleNeedsYou, badges.RoleDone, badges.RoleFailed:
		return 2
	case badges.RoleSudo, badges.RoleSSH, badges.RoleContainer, badges.RoleMosh:
		return 3
	case badges.RoleZoom:
		return 4
	case badges.RoleFlash:
		return 5
	}
	return -1
}

// TestLayoutRules checks the fitting rules over many random states: the
// badges fit the width less the inset, keep their order, appear only for
// state that asks for them, hide every badge after one that does not fit,
// and the notice never takes the row past two thirds of it.
func TestLayoutRules(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	pick := func(xs ...string) string { return xs[rng.IntN(len(xs))] }
	states := []session.AgentState{session.AgentNone, session.AgentWorking, session.AgentIdle,
		session.AgentNeedsYou, session.AgentDone, session.AgentFailed}
	for i := range 20000 {
		in := badges.Input{
			Passthrough:   rng.IntN(2) == 0,
			ScrollOffset:  []int{0, 0, 3, 120, 99999}[rng.IntN(5)],
			Agent:         pick("claude", "codex", "agent", "とても長い名前"),
			State:         states[rng.IntN(len(states))],
			Notice:        pick("", "ok", "Approve edit to main.go?", "完了しました。テストはすべて合格です。", strings.Repeat("x", 200)),
			Process:       pick("", "sh", "ssh", "mosh", "sudo", "su", "vim"),
			ContainerType: pick("", "", "toolbox", "distrobox", "podman"),
			ContainerID:   pick("", "fedora-42", "名前"),
			SSHHost:       pick("", "build01"),
			Zoomed:        rng.IntN(2) == 0,
			Flash:         pick("", badges.Copied),
			Width:         rng.IntN(130),
		}
		name := fmt.Sprintf("%d %+v", i, in)
		row := badges.Layout(in)
		wantInset := 0
		if in.ScrollOffset > 0 {
			wantInset = 1
		}
		if row.Inset != wantInset {
			t.Fatalf("%s: inset %d", name, row.Inset)
		}
		if cells := row.Cells(); cells > max(0, in.Width-row.Inset) {
			t.Fatalf("%s: %d cells overflow: %q", name, cells, row.Badges)
		}
		want := map[int]bool{
			0: in.Passthrough, 1: in.ScrollOffset > 0, 2: in.State != session.AgentNone,
			3: in.ContainerType != "" || slices.Contains([]string{"ssh", "mosh", "sudo", "su"}, in.Process),
			4: in.Zoomed, 5: in.Flash != "",
		}
		last := -1
		for _, b := range row.Badges {
			k := kind(b.Role)
			if k <= last || !want[k] {
				t.Fatalf("%s: badge %q (%s) out of order or not asked for: %q", name, b.Text, b.Role, row.Badges)
			}
			for skipped := last + 1; skipped < k; skipped++ {
				if want[skipped] {
					t.Fatalf("%s: kind %d hidden but %q shown after it", name, skipped, b.Text)
				}
			}
			last = k
			if !strings.HasPrefix(b.Text, " ") || !strings.HasSuffix(b.Text, " ") {
				t.Fatalf("%s: badge %q lacks its padding", name, b.Text)
			}
			if dot, label, _, _ := badges.AgentLook(in.State); k == 2 && b.Text != " "+dot+" "+in.Agent+" "+label+" " &&
				b.Text != " "+dot+" "+label+" " && b.Text != " "+dot+" " {
				full := " " + dot + " " + in.Agent + " " + label + " "
				if in.Notice == "" || !strings.HasPrefix(b.Text, strings.TrimSuffix(full, " ")+" · ") {
					t.Fatalf("%s: agent badge %q is no form and no notice", name, b.Text)
				}
				without := row.Cells() - uniseg.StringWidth(b.Text) + uniseg.StringWidth(full)
				if room := in.Width - row.Inset; row.Cells() > max(without, room*2/3) {
					t.Fatalf("%s: the notice takes the row past two thirds: %q", name, row.Badges)
				}
			}
		}
	}
}

// TestNoticeRoom: the notice takes the room left over up to two thirds of
// the row, and only beside the agent badge's widest form.
func TestNoticeRoom(t *testing.T) {
	base := badges.Input{Agent: "claude", State: session.AgentNeedsYou, Notice: strings.Repeat("n", 100)}
	for _, tc := range []struct {
		name  string
		width int
		want  string
	}{
		// " ◐ claude needs you " is 20 cells; " · " 3; at least 8 cells of notice.
		{"two thirds", 90, " ◐ claude needs you · " + strings.Repeat("n", 36) + "… "},
		{"just room for 8", 47, " ◐ claude needs you · " + strings.Repeat("n", 7) + "… "},
		{"one cell short", 46, " ◐ claude needs you "},
		{"short form takes no notice", 19, " ◐ needs you "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			in.Width = tc.width
			row := badges.Layout(in)
			if len(row.Badges) != 1 || row.Badges[0].Text != tc.want {
				t.Fatalf("width %d: %q, want %q", tc.width, row.Badges, tc.want)
			}
			if c := row.Cells(); c > max(20, tc.width*2/3) {
				t.Errorf("width %d: %d cells, past two thirds", tc.width, c)
			}
		})
	}
}

func TestAgentLook(t *testing.T) {
	for _, tc := range []struct {
		state      session.AgentState
		dot, label string
		role       badges.Role
	}{
		{session.AgentWorking, "●", "working", badges.RoleWorking},
		{session.AgentIdle, "○", "idle", badges.RoleIdle},
		{session.AgentNeedsYou, "◐", "needs you", badges.RoleNeedsYou},
		{session.AgentDone, "✓", "done", badges.RoleDone},
		{session.AgentFailed, "✕", "failed", badges.RoleFailed},
	} {
		t.Run(tc.label, func(t *testing.T) {
			dot, label, role, ok := badges.AgentLook(tc.state)
			if !ok || dot != tc.dot || label != tc.label || role != tc.role {
				t.Fatalf("AgentLook = %q %q %s %v", dot, label, role, ok)
			}
			if st, ok := role.AgentState(); !ok || st != tc.state {
				t.Fatalf("%s.AgentState() = %v %v", role, st, ok)
			}
		})
	}
	if _, _, _, ok := badges.AgentLook(session.AgentNone); ok {
		t.Error("AgentNone has an agent badge")
	}
}

func TestRoleNames(t *testing.T) {
	seen := map[string]badges.Role{}
	for _, r := range badges.Roles {
		name := r.String()
		if prev, dup := seen[name]; dup || name == "" || strings.HasPrefix(name, "role-") {
			t.Errorf("role %d named %q (also %d)", r, name, prev)
		}
		seen[name] = r
		if _, ok := r.AgentState(); ok != (kind(r) == 2) {
			t.Errorf("%s.AgentState() ok=%v", r, ok)
		}
	}
}
