package main

// README pictures of single features: the collapsed rail as rings and as
// maps, a loop of agent states, a pane's badges, the hooks and restore
// banners, the Agent hooks preferences and bunk's TUI in a pane. Crops
// are cut from window shots with the geometry of the snapshot taken with
// them, so they follow the layout.

import (
	"encoding/json"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"bunk/internal/notify"
	"bunk/internal/restore"
)

// statesFrames are the frames of the states loop, which screenshots.sh
// turns into assets/gui-states.webp.
var statesFrames = []string{"frames/states-1.png", "frames/states-2.png", "frames/states-3.png", "frames/states-4.png"}

// railScreenshots: six tabs of one to four panes in the collapsed rail.
// Tab 1 holds claude on the left and codex above a shell on the right;
// the frames change their states, then the rail is shot as rings and as
// maps, and codex's pane, zoomed, shows its badges.
func railScreenshots(t *testing.T) {
	demo := t.TempDir()
	g := runGUI(t, scenario{
		cost:    9,
		prepare: func() { writeDemo(t, demo) },
		config:  "tabs = \"rail\"\nrail_open = false\nfont = \"Noto Sans Mono\"\nfont_size = 11\n",
		args: []string{"--size", "132x32",
			"--command", `cd "$DEMO/src/api" && exec claude`,
			"--tab", "", "--tab", "", "--tab", "", "--tab", "", "--tab", ""},
		env: demoEnv(t, demo),
		// Panes 0 to 5 are the tabs' first panes; splits number on from 6.
		script: []string{
			"wait pane.0.text~\"Running the integration tests\"",
			"split",
			"wait tab.1.panes.#=2 focused.line=❯",
			"type codex\\r",
			"wait focused.text~\"npm install\"",
			"split",
			"wait tab.1.panes.#=3 focused.line=❯",
			"type git log --oneline --graph --all\\r",
			"wait focused.text~\"Start the API server\" focused.line=❯",
			"select-tab 3", "wait selected_tab=3 focused.line=❯",
			"split", "wait tab.3.panes.#=2",
			"select-tab 4", "wait selected_tab=4 focused.line=❯",
			"split", "wait tab.4.panes.#=2 focused.line=❯",
			"split", "wait tab.4.panes.#=3 focused.line=❯",
			"focus left", "split", "wait tab.4.panes.#=4",
			"select-tab 6", "wait selected_tab=6 focused.line=❯",
			"split", "wait tab.6.panes.#=2",
			"select-tab 1", "wait selected_tab=1",
			"status pane=0 agent=claude state=working repo=api branch=feat/rate-limit cwd=~/src/api",
			"status pane=6 agent=codex state=working repo=api branch=feat/rate-limit cwd=~/src/api",
			"status pane=1 agent=copilot state=idle repo=infra branch=main cwd=~/ops/infra",
			"status pane=2 agent=gemini state=failed repo=ml branch=main cwd=~/work/ml notice=API error: quota exceeded",
			"status pane=3 agent=claude state=done repo=docs branch=main cwd=~/src/docs notice=Finished the docs refactor",
			"status pane=9 agent=codex state=working repo=docs branch=main cwd=~/src/docs",
			"status pane=5 agent=claude state=needs repo=web branch=fix-login cwd=~/src/web notice=Allow npm install?",
			"wait tab.6.state=needs pane.0.settled=true pane.6.settled=true pane.7.settled=true idle 300ms",
			"snap rings",
			"shot states-1.png",
			"status pane=6 state=needs notice=Allow npm install?",
			"wait pane.6.status.state=needs idle 300ms",
			"shot states-2.png",
			"status pane=6 state=working notice=",
			"status pane=0 state=done notice=Rate limit added",
			"wait pane.0.status.state=done pane.6.status.state=working idle 300ms",
			"shot states-3.png",
			"status pane=0 state=working notice=",
			"status pane=6 state=done notice=Retry-After header added",
			"wait pane.0.status.state=working pane.6.status.state=done idle 300ms",
			"shot states-4.png",
			// The map shows the states of the ring shot.
			"status pane=6 state=working notice=",
			"run printf 'rail_style = \"map\"\\n' >> config.toml",
			"wait rail_style=map pane.6.status.state=working idle 300ms",
			"snap map",
			"shot map-window.png",
			// Codex in a container asks, zoomed.
			"focus up",
			"wait focused_pane=6",
			"fake-container podman web-dev",
			"status pane=6 agent=codex state=needs container=podman:web-dev notice=Allow npm install?",
			"zoom",
			"wait pane.6.zoomed=true pane.6.settled=true idle 300ms",
			"snap badges",
			"shot badges-window.png",
			"quit",
		},
	})

	rings := g.snap("rings")
	if rings.Layout != "rail" || rings.RailOpen || rings.RailStyle != "ring" || len(rings.Tabs) != 6 {
		t.Fatalf("rings: layout %q, open %v, style %q, %d tabs", rings.Layout, rings.RailOpen, rings.RailStyle, len(rings.Tabs))
	}
	for i, want := range []int{3, 1, 2, 4, 1, 2} {
		if n := len(rings.tab(t, i+1).Panes); n != want {
			t.Errorf("tab %d: %d panes, want %d", i+1, n, want)
		}
	}
	cropShot(t, g, "states-1.png", "gui-rail-ring.png", railStrip(t, rings))
	if m := g.snap("map"); m.RailStyle != "map" {
		t.Errorf("map: style %q", m.RailStyle)
	} else {
		cropShot(t, g, "map-window.png", "gui-rail-map.png", railStrip(t, m))
	}

	b := g.snap("badges")
	p := b.pane(t, 6)
	if want := []string{"codex needs you · Allow npm install?", "web-dev", "ZOOM"}; !containsAll(p.BadgeRow, want) {
		t.Errorf("badges: %q", p.BadgeRow)
	}
	if r := badgeCrop(p); r.Empty() {
		t.Errorf("badges: no badge rects in %+v", p.Badges)
	} else {
		cropShot(t, g, "badges-window.png", "gui-badges.png", r)
	}
	saveShots(t, g, append([]string{"gui-rail-ring.png", "gui-rail-map.png", "gui-badges.png"}, statesFrames...)...)
}

// railStrip is the collapsed rail around its tabs in a window shot.
func railStrip(t *testing.T, s *uiSnapshot) image.Rectangle {
	t.Helper()
	first, last := s.tab(t, 1).Item, s.tab(t, len(s.Tabs)).Item
	if s.Chrome == nil || first == nil || last == nil {
		t.Fatalf("%s: no rail or tab geometry", s.Label)
	}
	pad := first.DH / 4
	return image.Rect(s.Chrome.DX, first.DY-pad, s.Chrome.DX+s.Chrome.DW, last.DY+last.DH+pad)
}

func containsAll(s string, subs []string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

// badgeCrop is pane p's row of badges, with half a cell on each side.
func badgeCrop(p paneReport) image.Rectangle {
	var r image.Rectangle
	for _, b := range p.Badges {
		if b.Rect != nil {
			r = r.Union(devRect(b.Rect))
		}
	}
	if r.Empty() {
		return r
	}
	pad := p.Cell[0] / 2
	x, w := p.Texture[0], p.Texture[2]
	return image.Rect(max(x, r.Min.X-pad), r.Min.Y, min(x+w, r.Max.X+pad), r.Max.Y)
}

func devRect(r *rectReport) image.Rectangle { return image.Rect(r.DX, r.DY, r.DX+r.DW, r.DY+r.DH) }

// cropShot writes the part r of the PNG src as dst, both in g's
// directory.
func cropShot(t *testing.T, g *guiRun, src, dst string, r image.Rectangle) {
	t.Helper()
	img := loadPNG(t, g.file(src))
	r = r.Intersect(img.Bounds())
	if r.Dx() < 16 || r.Dy() < 16 {
		t.Errorf("%s: crop %v of %v is too small", dst, r, img.Bounds())
		return
	}
	sub := img.(interface {
		SubImage(image.Rectangle) image.Image
	}).SubImage(r)
	f, err := os.Create(g.file(dst))
	must(t, err)
	if err := png.Encode(f, sub); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	must(t, f.Close())
}

// hooksScreenshots: a start that offers two closed windows back and
// outdated codex hooks in banners, then the Agent hooks section of the
// preferences with claude installed, codex outdated and the others
// missing.
func hooksScreenshots(t *testing.T) {
	state := t.TempDir()
	env, _ := restoreEnv(t, state)
	// Every agent is on PATH, so no row adds "not on PATH" for an agent
	// this machine lacks.
	stubs := t.TempDir()
	for _, name := range []string{"codex", "copilot", "gemini"} {
		must(t, os.WriteFile(filepath.Join(stubs, name), []byte("#!/bin/sh\n"), 0o755))
	}
	for i, kv := range env {
		if path, ok := strings.CutPrefix(kv, "PATH="); ok {
			env[i] = "PATH=" + stubs + ":" + path
		}
	}
	home := t.TempDir()
	for _, h := range notify.CheckHooks(notify.Dirs{Home: home}) {
		snippet, _ := notify.HookSnippet(h.Agent)
		switch h.Agent {
		case "claude":
		case "codex":
			snippet = partialHooks(t, snippet)
		default:
			continue
		}
		must(t, os.MkdirAll(filepath.Dir(h.Path), 0o755))
		must(t, os.WriteFile(h.Path, []byte(snippet), 0o644))
	}
	pane := func(cwd, agent string) *restore.Node {
		return &restore.Node{Pane: &restore.Pane{Cwd: cwd, Agent: agent}}
	}
	// The banner shows when the newest window was saved: fixed times
	// keep the shot the same on every run.
	for i, tabs := range [][]restore.Tab{
		{{Root: pane("/tmp", "claude")}, {Root: pane("/tmp", "")}},
		{{Root: &restore.Node{A: pane("/tmp", "claude"), B: pane("/tmp", "")}},
			{Name: "logs", Root: pane("/tmp", "")}, {Root: pane("/tmp", "codex")}},
	} {
		id := writeSnapshot(t, state, restore.Window{Tabs: tabs})
		stampSaved(t, state, id, time.Date(2026, 9, 14, 18, 30+i, 0, 0, time.Local))
	}
	g := runGUI(t, scenario{
		cost:   4,
		env:    env,
		config: "tabs = \"rail\"\nrail_open = false\nfont = \"Noto Sans Mono\"\nfont_size = 11\n",
		args:   []string{"--size", "100x28", "--agent-home", home},
		script: []string{
			"wait restore.banner=true hooks.banner=true focused.line=$ focused.settled=true idle 500ms",
			"snap banners",
			"shot banners-window.png",
			"kbd ctrl+comma",
			"wait prefs!=null prefs.hooks.#=4",
			"pref-scroll hooks",
			"wait idle 500ms",
			"snap prefs",
			"shot gui-hooks-prefs.png",
			"dialog-close",
			"wait prefs=null",
			"quit",
		},
	})
	b := g.snap("banners")
	if !strings.Contains(b.Restore.Offer, "2 windows") || !slices.Equal(b.Hooks.Agents, []string{"codex"}) {
		t.Errorf("banners: restore %q, hooks %+v", b.Restore.Offer, b.Hooks)
	}
	if len(b.Panes) == 0 || b.Panes[0].Rect == nil || b.Chrome == nil {
		t.Fatalf("banners: no pane or rail geometry")
	}
	top := b.Panes[0].Rect
	cropShot(t, g, "banners-window.png", "gui-banners.png",
		image.Rect(b.Chrome.DX+b.Chrome.DW, 0, top.DX+top.DW, top.DY))
	var states []string
	for _, h := range g.snap("prefs").Prefs.Hooks {
		states = append(states, h.Agent+" "+h.State)
	}
	if want := []string{"claude installed", "codex outdated", "copilot missing", "gemini missing"}; !slices.Equal(states, want) {
		t.Errorf("preferences list %q, want %q", states, want)
	}
	saveShots(t, g, "gui-banners.png", "gui-hooks-prefs.png")
}

// stampSaved sets the time a saved window records, which Slot.Save sets
// to the present.
func stampSaved(t *testing.T, state, id string, when time.Time) {
	t.Helper()
	path := filepath.Join(state, "bunk-gui", "windows", id+".json")
	var w restore.Window
	must(t, json.Unmarshal([]byte(readFile(t, path)), &w))
	w.Saved = when
	data, err := json.Marshal(w)
	must(t, err)
	must(t, os.WriteFile(path, data, 0o600))
}

// partialHooks keeps the first event of a JSON hook snippet, as an older
// bunk wrote fewer hooks: CheckHooks calls such a file outdated.
func partialHooks(t *testing.T, snippet string) string {
	t.Helper()
	var doc map[string]map[string]json.RawMessage
	must(t, json.Unmarshal([]byte(snippet), &doc))
	events := make([]string, 0, len(doc["hooks"]))
	for ev := range doc["hooks"] {
		events = append(events, ev)
	}
	slices.Sort(events)
	doc["hooks"] = map[string]json.RawMessage{events[0]: doc["hooks"][events[0]]}
	out, err := json.MarshalIndent(doc, "", "  ")
	must(t, err)
	return string(out) + "\n"
}

// tuiScreenshots: bunk's TUI in a bunk-gui pane, split into claude
// working, codex asking and a shell, as a terminal shows it. F1 reaches
// the TUI because the scenario turns bunk-gui's split key off.
func tuiScreenshots(t *testing.T) {
	demo := t.TempDir()
	g := runGUI(t, scenario{
		cost: 6,
		prepare: func() {
			writeDemo(t, demo)
			for name, text := range tuiFiles {
				must(t, os.WriteFile(filepath.Join(demo, name), []byte(text), 0o755))
			}
			build := exec.Command("go", "build", "-o", filepath.Join(demo, "bin", "bunk"), ".")
			build.Dir = filepath.Join("..", "..")
			if out, err := build.CombinedOutput(); err != nil {
				t.Fatalf("go build bunk: %v\n%s", err, out)
			}
		},
		config: "tabs = \"rail\"\nrail_open = false\nfont = \"Noto Sans Mono\"\nfont_size = 11\n\n[keys]\nsplit = \"none\"\n",
		// BUNK may come from a bunk the tests run in, and bunk refuses to
		// start under it. Without bunk-gui's notify token, the TUI does
		// not relay its state to the pane it runs in, whose own badge
		// would cover the TUI's.
		args: []string{"--size", "140x34",
			"--command", `cd "$DEMO/src/api" && unset BUNK BUNK_NOTIFY_SESSION BUNK_NOTIFY_SOCKET && exec bunk -c "$DEMO/bunk.toml"`},
		env: demoEnv(t, demo),
		script: []string{
			"wait focused.line~❯",
			"type claude\\r",
			"wait focused.text~\"Running the integration tests\" focused.line!~❯",
			"kbd F1",
			"wait focused.line~❯",
			"type codex\\r",
			"wait focused.text~\"codex needs you\" focused.line!~❯",
			"kbd F1",
			"wait focused.line~❯",
			"type git log --oneline --graph --all\\r",
			"wait focused.text~\"Start the API server\" focused.text~\"claude working\" focused.line~❯",
			"wait pane.0.settled=true idle 500ms",
			"snap tui",
			"shot tui-window.png",
			"quit",
		},
	})
	p := g.snap("tui").pane(t, 0)
	screen := p.text()
	for _, s := range []string{"claude working", "codex needs you", "Allow npm install?", "Start the API server", "Edit internal/limit/bucket.go"} {
		if !strings.Contains(screen, s) {
			t.Errorf("TUI lacks %q:\n%s", s, screen)
		}
	}
	x, y, w, h := p.Texture[0], p.Texture[1], p.Texture[2], p.Texture[3]
	cropShot(t, g, "tui-window.png", "tui.png", image.Rect(x, y, x+w, y+h))
	saveShots(t, g, "tui.png")
}

// tuiFiles replace writeDemo's agents for the TUI: they report their
// state through bunk's hook CLI, as agents with hooks do.
var tuiFiles = map[string]string{
	"bin/claude": "#!/bin/sh\nbunk notify working\n" +
		"draw() { printf '\\033[?1049h\\033[?25l\\033[H\\033[2J'; cat \"$DEMO/transcript.txt\"; }\n" +
		"trap draw WINCH\ndraw\nwhile :; do sleep 0.2 & wait $!; done\n",
	"bin/codex": "#!/bin/sh\ncat \"$DEMO/codex.txt\"\nbunk notify needs-you 'Allow npm install?'\n" +
		"while :; do sleep 0.2 & wait $!; done\n",
	"bunk.toml": "theme = \"default\"\n\n[notifications]\nenabled = false\n",
}

// demoCodex is what the fake codex prints: an edit, a test run and the
// command it wants to run next.
const demoCodex = "\x1b[2m›\x1b[0m Return Retry-After on 429 responses\n" +
	"\n" +
	"\x1b[1m•\x1b[0m Explored\n" +
	"  \x1b[2m└\x1b[0m Read internal/api/router.go\n" +
	"    Read internal/limit/bucket.go\n" +
	"    Search 429 in internal/\n" +
	"\n" +
	"\x1b[1m•\x1b[0m Edited internal/api/router.go \x1b[32m+4\x1b[0m \x1b[31m-1\x1b[0m\n" +
	"    \x1b[2m31\x1b[0m   if !b.Take(now) {\n" +
	"    \x1b[2m32\x1b[0m \x1b[31m-   w.WriteHeader(429)\x1b[0m\n" +
	"    \x1b[2m32\x1b[0m \x1b[32m+   wait := b.Next(now).Sub(now)\x1b[0m\n" +
	"    \x1b[2m33\x1b[0m \x1b[32m+   secs := retryAfter(wait)\x1b[0m\n" +
	"    \x1b[2m34\x1b[0m \x1b[32m+   w.Header().Set(\"Retry-After\", secs)\x1b[0m\n" +
	"    \x1b[2m35\x1b[0m \x1b[32m+   w.WriteHeader(429)\x1b[0m\n" +
	"\n" +
	"\x1b[1m•\x1b[0m Ran go test ./internal/api/\n" +
	"  \x1b[2m└\x1b[0m ok  example.com/api/internal/api  0.208s\n" +
	"\n" +
	"\x1b[1m•\x1b[0m The contract tests need a dev dependency:\n" +
	"  \x1b[1mnpm install --save-dev @example/contract\x1b[0m\n"
