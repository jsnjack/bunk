package main

// README screenshots. TestGUIScreenshots runs one demo scenario on the
// headless compositor and checks every shot, so `make gui-test` keeps the
// scenario working. With BUNK_SCREENSHOTS=DIR it also copies the PNGs into
// DIR; scripts/screenshots.sh sets it to assets/.
//
// The content is fixed: agent states come from the status test hook and one
// real `bunk-gui notify done` report; the shell pane lists a git repository
// built here with fixed names and dates. Shots wait out caret blinks and
// scroll indicators, so two runs on one machine give identical PNGs. Fonts
// and the GPU driver can still change the pixels.

import (
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// demoShots are the PNGs the scenario writes, in the order it takes them.
var demoShots = []string{"gui-hero.png", "gui-switcher.png", "gui-bar.png", "gui-image.png", "gui-find.png", "gui-shortcuts.png"}

func TestGUIScreenshots(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed: the demo repository needs it")
	}
	bin := filepath.Dir(guiBinary(t))
	demo := t.TempDir()
	g := runGUI(t, scenario{
		cost:    10,
		prepare: func() { writeDemo(t, demo) },
		config:  "tabs = \"rail\"\nrail_open = true\nfont = \"Noto Sans Mono\"\nfont_size = 11\n",
		args: []string{"--size", "120x32",
			"--command", `cd "$DEMO/src/api" && exec claude`,
			"--tab", "", "--tab", "", "--tab", `exec sh "$DEMO/done.sh"`, "--tab", ""},
		env: []string{
			"PATH=" + filepath.Join(demo, "bin") + ":" + bin + ":" + os.Getenv("PATH"),
			"DEMO=" + demo, "ENV=" + filepath.Join(demo, "shrc"),
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_PAGER=cat",
			"LC_COLLATE=C", "LS_COLORS=",
		},
		// Shots wait until the panes have drawn their programs' output at
		// their size (the fake agent redraws on SIGWINCH) and nothing moves.
		// An entry's focus ring fades in (a 200 ms CSS transition) and its
		// caret blinks: entry shots sleep out the fade, then follow a key
		// press into the entry (wait input), while GTK draws the caret
		// solid. The shortcuts shot waits for the scroll indicator to fade
		// out.
		script: []string{
			"split",
			"wait tab.1.panes.#=2 focused.line=❯",
			"type ls\\r",
			"wait focused.text~Makefile focused.line=❯",
			"type git log --oneline --graph --all\\r",
			"wait focused.text~\"Start the API server\" focused.line=❯",
			"status pane=0 agent=claude state=working repo=api branch=feat/rate-limit",
			"status tab=2 agent=codex state=needs repo=web branch=fix-login notice=Allow npm install?",
			"status tab=3 agent=copilot state=idle repo=infra branch=main container=toolbox:fedora-42",
			"status tab=5 agent=gemini state=failed repo=ml branch=main ssh=gpu01 notice=API error: quota exceeded",
			// Tab 4 keeps the state of its notify report: done.
			"wait tab.4.status.reported=true tab.4.state=done",
			"status tab=4 agent=claude repo=docs branch=main",
			"wait pane.0.text~\"Running the integration tests\" pane.0.settled=true pane.5.settled=true idle 300ms",
			"snap hero",
			"shot gui-hero.png",
			"switcher",
			"wait switcher.open=true keyboard_focus=switcher",
			"sleep 500ms",
			"kbd BackSpace",
			"wait input",
			"snap switcher",
			"shot gui-switcher.png",
			"switcher close",
			"wait switcher.open=false",
			"layout bar",
			"wait layout=bar",
			"wait pane.0.settled=true pane.5.settled=true idle 300ms",
			"snap bar",
			"shot gui-bar.png",
			"layout rail",
			"wait layout=rail",
			"type clear\\r",
			"wait focused.text!~\"git log\" focused.line=❯",
			"type kitten icat docs/load-test.png\\r",
			"wait focused.images.cells>0 focused.line=❯",
			"wait pane.0.settled=true pane.5.settled=true idle 300ms",
			"snap image",
			"shot gui-image.png",
			"rail closed",
			"wait rail_open=false",
			"new-tab exec sh \"$DEMO/gotest.sh\"",
			"wait tabs.#=6 focused.text~3.472s focused.line=❯",
			"kbd ctrl+shift+f",
			"wait find.focused=true",
			"sleep 500ms",
			"kbd-type limit",
			"wait find.query=limit find.busy=false find.total>=3",
			"kbd End",
			"wait input",
			"snap find",
			"shot gui-find.png",
			"kbd Escape",
			"wait find=null",
			"kbd alt+1",
			"wait selected_tab=1",
			"kbd ctrl+shift+question",
			"wait shortcuts!=null",
			"shortcut-scroll switcher",
			"wait shortcuts.indicator>0",
			"wait shortcuts.indicator=0 idle 200ms",
			"snap shortcuts",
			"shot gui-shortcuts.png",
			"quit",
		},
	})

	hero := g.snap("hero")
	want := []struct{ agent, state, repo string }{
		{"claude", "working", "api ⎇ feat/rate-limit"},
		{"codex", "needs", "web ⎇ fix-login"},
		{"copilot", "idle", "infra ⎇ main"},
		{"claude", "done", "docs ⎇ main"},
		{"gemini", "failed", "ml ⎇ main"},
	}
	if len(hero.Tabs) != len(want) || hero.SelectedTab != 1 || hero.Layout != "rail" || !hero.RailOpen {
		t.Fatalf("hero: %d tabs, selected %d, layout %q, rail open %v", len(hero.Tabs), hero.SelectedTab, hero.Layout, hero.RailOpen)
	}
	for i, w := range want {
		if tb := hero.tab(t, i+1); tb.Agent != w.agent || tb.State != w.state || tb.Repo != w.repo {
			t.Errorf("hero tab %d: %s %s %q; want %s %s %q", i+1, tb.Agent, tb.State, tb.Repo, w.agent, w.state, w.repo)
		}
	}
	if tb := hero.tab(t, 4); !tb.Status.Reported || tb.Notice != "Finished the docs refactor" {
		t.Errorf("hero tab 4: the notify report did not arrive: %+v", tb.Status)
	}
	if len(hero.tab(t, 1).Panes) != 2 {
		t.Errorf("hero tab 1: panes %v, want a split", hero.tab(t, 1).Panes)
	}
	agent, shell := hero.pane(t, 0).text(), hero.focused(t).text()
	for _, s := range []string{"Edit internal/limit/bucket.go", "Running the integration tests"} {
		if !strings.Contains(agent, s) {
			t.Errorf("agent pane lacks %q:\n%s", s, agent)
		}
	}
	for _, s := range []string{"internal", "(HEAD -> feat/rate-limit)", "(tag: v0.3.0)", "Limit /v1 routes per API key"} {
		if !strings.Contains(shell, s) {
			t.Errorf("shell pane lacks %q:\n%s", s, shell)
		}
	}
	if s := g.snap("switcher"); !s.Switcher.Open || len(s.Switcher.Rows) != len(want) || s.Switcher.Rows[0] != 2 {
		t.Errorf("switcher: %+v; want all tabs, needs-you first", s.Switcher)
	}
	if s := g.snap("bar"); s.Layout != "bar" {
		t.Errorf("bar: layout %q", s.Layout)
	}
	img := g.snap("image")
	if p := img.focused(t); p.Images.Stored != 1 || p.Images.Cells == 0 || p.Images.HalfBlocks != 0 || !img.RailOpen {
		t.Errorf("image: %+v in pane %d, rail open %v; want one bitmap drawn at full resolution", p.Images, p.ID, img.RailOpen)
	} else {
		// Drawn 1:1, the chart's background keeps its exact colour.
		shot := loadPNG(t, g.file("gui-image.png"))
		r := p.Rect
		if n := count(shot, r.DX, r.DY, r.DW, r.DH, hex(chartBG), 0); n < chartW*chartH/2 {
			t.Errorf("image: %d pixels of the chart background %v in the pane", n, hex(chartBG))
		}
	}
	if s := g.snap("find"); s.Find == nil || s.Find.Query != "limit" || s.Find.Total < 3 || !s.Find.Focused || s.Layout != "rail" || s.RailOpen {
		t.Errorf("find: %+v; layout %q, rail open %v", s.Find, s.Layout, s.RailOpen)
	}
	if s := g.snap("shortcuts"); s.Shortcuts == nil {
		t.Error("shortcuts: the window is not open")
	}

	// Decoded side by side: each takes a while under -race.
	sizes := make([]image.Rectangle, len(demoShots))
	errs := make([]error, len(demoShots))
	var wg sync.WaitGroup
	for i, name := range demoShots {
		wg.Go(func() {
			f, err := os.Open(g.file(name))
			if err == nil {
				var img image.Image
				if img, err = png.Decode(f); err == nil {
					sizes[i] = img.Bounds()
				}
				_ = f.Close()
			}
			errs[i] = err
		})
	}
	wg.Wait()
	for i, b := range sizes {
		if errs[i] != nil {
			t.Errorf("%s: %v", demoShots[i], errs[i])
		} else if b.Dx() < 800 || b.Dy() < 400 {
			t.Errorf("%s: %dx%d px, want a whole window", demoShots[i], b.Dx(), b.Dy())
		}
	}
	if out := os.Getenv("BUNK_SCREENSHOTS"); out != "" && !t.Failed() {
		for _, name := range demoShots {
			must(t, copyFile(g.file(name), filepath.Join(out, name)))
		}
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// writeDemo fills dir with the scenario's fixtures: a fake agent CLI on
// PATH, a shell prompt, scripts for two tabs and a git repository.
func writeDemo(t *testing.T, dir string) {
	t.Helper()
	files := map[string]string{
		// A fake agent: like a TUI, it draws on the alternate screen and
		// draws again after a resize.
		"bin/claude": "#!/bin/sh\ndraw() { printf '\\033[?1049h\\033[?25l\\033[H\\033[2J'; cat \"$DEMO/transcript.txt\"; }\n" +
			// wait returns at once on a trapped signal, so a resize redraws
			// without waiting for the sleep.
			"trap draw WINCH\ndraw\nwhile :; do sleep 0.2 & wait $!; done\n",
		// The prompt is fixed text: the shell pane stays in src/api.
		"shrc": "PS1='\x1b[1;34m~/src/api\x1b[0m \x1b[35mfeat/rate-limit\x1b[0m\n\x1b[32m❯\x1b[0m '\n" +
			"alias ls='ls --color=auto'\n",
		"done.sh": "bunk-gui notify done \"Finished the docs refactor\"\nsleep 3600\n",
		// kitten icat stand-in: prints the chart as a Kitty graphics image.
		"bin/kitten":      "#!/bin/sh\n[ \"$1\" = icat ] || exit 1\ncat \"$DEMO/load-test.kitty\"\n",
		"load-test.kitty": kittyChunks(demoChart()) + "\n",
		"gotest.sh": "printf '\\033[1;34m~/src/api\\033[0m \\033[35mfeat/rate-limit\\033[0m\\n\\033[32m❯\\033[0m go test -v ./internal/limit/\\n'\n" +
			"cat \"$DEMO/gotest.txt\"\nexec sh\n",
		"transcript.txt": demoTranscript,
		"gotest.txt":     demoTestLog,
	}
	for name, text := range files {
		path := filepath.Join(dir, name)
		must(t, os.MkdirAll(filepath.Dir(path), 0o755))
		must(t, os.WriteFile(path, []byte(text), 0o755))
	}
	demoRepo(t, filepath.Join(dir, "src", "api"))
}

// demoRepo creates a git repository with fixed authors and dates, so
// commit hashes are the same on every run: main and a feature branch that
// leave a tagged commit.
func demoRepo(t *testing.T, dir string) {
	t.Helper()
	for name, text := range map[string]string{
		"go.mod":                    "module example.com/api\n\ngo 1.26\n",
		"go.sum":                    "",
		"Makefile":                  "test:\n\tgo test ./...\n",
		"README.md":                 "# api\n",
		"cmd/api/main.go":           "package main\n\nfunc main() {}\n",
		"internal/limit/bucket.go":  "package limit\n",
		"internal/store/store.go":   "package store\n",
		"docs/rate-limits.md":       "# Rate limits\n",
		"scripts/deploy.sh":         "#!/bin/sh\n",
		"internal/api/router.go":    "package api\n",
		"internal/auth/token.go":    "package auth\n",
		"internal/store/migrate.go": "package store\n",
	} {
		path := filepath.Join(dir, name)
		must(t, os.MkdirAll(filepath.Dir(path), 0o755))
		mode := os.FileMode(0o644)
		if strings.HasSuffix(name, ".sh") {
			mode = 0o755
		}
		must(t, os.WriteFile(path, []byte(text), mode))
	}
	day := 0
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		date := fmt.Sprintf("2026-09-%02dT10:00:00+02:00", 1+day)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=Demo Dev", "GIT_AUTHOR_EMAIL=dev@example.com", "GIT_AUTHOR_DATE="+date,
			"GIT_COMMITTER_NAME=Demo Dev", "GIT_COMMITTER_EMAIL=dev@example.com", "GIT_COMMITTER_DATE="+date)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	commit := func(msg string) {
		t.Helper()
		day++
		git("commit", "-q", "--allow-empty", "-m", msg)
	}
	git("init", "-q", "-b", "main")
	git("add", ".")
	commit("Start the API server")
	commit("Add /health endpoint")
	commit("Log requests as JSON")
	commit("Store sessions in Postgres")
	git("tag", "v0.3.0")
	git("checkout", "-q", "-b", "feat/rate-limit")
	commit("Add a token bucket")
	commit("Limit /v1 routes per API key")
	commit("Refill on take")
	git("checkout", "-q", "main")
	commit("Document the deploy script")
	commit("Bump Go to 1.26")
	git("checkout", "-q", "feat/rate-limit")
}

// demoTranscript is what the fake agent prints: an edit with truecolour
// diff lines, a test run and a spinner.
const demoTranscript = "\x1b[2m›\x1b[0m Add a per-key rate limit to the public API\n" +
	"\n" +
	"\x1b[32m●\x1b[0m \x1b[1mRead\x1b[0m internal/limit/bucket.go\n" +
	"  \x1b[2m└ 84 lines\x1b[0m\n" +
	"\n" +
	"\x1b[32m●\x1b[0m \x1b[1mEdit\x1b[0m internal/limit/bucket.go  \x1b[32m+5\x1b[0m \x1b[31m-2\x1b[0m\n" +
	del + "41\x1b[0m" + delBg + "-    if b.tokens == 0 {\x1b[K\x1b[0m\n" +
	del + "42\x1b[0m" + delBg + "-        return false\x1b[K\x1b[0m\n" +
	add + "41\x1b[0m" + addBg + "+    b.refill(now)\x1b[K\x1b[0m\n" +
	add + "42\x1b[0m" + addBg + "+    if b.tokens < 1 {\x1b[K\x1b[0m\n" +
	add + "43\x1b[0m" + addBg + "+        return false\x1b[K\x1b[0m\n" +
	add + "44\x1b[0m" + addBg + "+    }\x1b[K\x1b[0m\n" +
	add + "45\x1b[0m" + addBg + "+    b.tokens--\x1b[K\x1b[0m\n" +
	"\n" +
	"\x1b[32m●\x1b[0m \x1b[1mRun\x1b[0m go test ./internal/limit/\n" +
	"  \x1b[2m└\x1b[0m ok   example.com/api/internal/limit  0.412s\n" +
	"\n" +
	"\x1b[32m●\x1b[0m \x1b[1mEdit\x1b[0m internal/api/router.go  \x1b[32m+1\x1b[0m\n" +
	add + "18\x1b[0m" + addBg + "+    r.Use(limit.PerKey(100, time.Minute))\x1b[K\x1b[0m\n" +
	"\n" +
	"\x1b[38;2;232;140;90m✻ Running the integration tests…\x1b[0m \x1b[2m(14s · esc to stop)\x1b[0m\n" +
	"\n" +
	"\x1b[2m────────────────────────────────────────────────────\x1b[0m\n" +
	"\x1b[1m›\x1b[0m \x1b[7m \x1b[0m\n" +
	"\x1b[2m────────────────────────────────────────────────────\x1b[0m\n"

// Diff line styles: a dim line number, then the line on a tinted background.
const (
	del   = "\x1b[38;2;150;110;110m   "
	add   = "\x1b[38;2;110;150;120m   "
	delBg = " \x1b[48;2;74;34;40m"
	addBg = " \x1b[48;2;28;64;42m"
)

// demoTestLog is a failing go test -v run for the find shot.
const demoTestLog = run + "TestBucketTake\n" + pass + "TestBucketTake (0.00s)\n" +
	run + "TestBucketRefill\n" + pass + "TestBucketRefill (0.01s)\n" +
	run + "TestBucketConcurrent\n" + pass + "TestBucketConcurrent (0.12s)\n" +
	run + "TestLimitBurst\n" +
	"    bucket_test.go:57: request 101: got 200, want 429\n" +
	"    bucket_test.go:58: limit state: tokens=1 refill=1m0s\n" +
	fail + "TestLimitBurst (2.00s)\n" +
	run + "TestLimitPerKey\n" +
	run + "TestLimitPerKey/key_a\n" +
	run + "TestLimitPerKey/key_b\n" +
	"    router_test.go:88: key b: got 429 after 1 request\n" +
	run + "TestLimitPerKey/key_c\n" +
	fail + "TestLimitPerKey (0.31s)\n" +
	"    " + pass + "TestLimitPerKey/key_a (0.10s)\n" +
	"    " + fail + "TestLimitPerKey/key_b (0.11s)\n" +
	"    " + pass + "TestLimitPerKey/key_c (0.10s)\n" +
	run + "TestLimitHeaders\n" + pass + "TestLimitHeaders (0.02s)\n" +
	run + "TestLimitReset\n" + pass + "TestLimitReset (1.01s)\n" +
	"\x1b[31mFAIL\x1b[0m\n" +
	"\x1b[31mFAIL\x1b[0m\texample.com/api/internal/limit\t3.472s\n"

const (
	run  = "\x1b[2m=== RUN\x1b[0m   "
	pass = "--- \x1b[32mPASS\x1b[0m: "
	fail = "--- \x1b[31mFAIL\x1b[0m: "
)

// The load-test chart: p99 and p50 latency over time, high and spiky until
// a dashed line where the rate limit went live, low after it.
const (
	chartW, chartH = 640, 300
	chartBG        = 0x1d2026
)

// demoChart draws the chart with integer maths only, so every platform
// gets the same pixels.
func demoChart() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, chartW, chartH))
	rgbOf := func(c uint32) color.NRGBA { return color.NRGBA{uint8(c >> 16), uint8(c >> 8), uint8(c), 255} }
	bg := rgbOf(chartBG)
	for y := 0; y < chartH; y++ {
		for x := 0; x < chartW; x++ {
			img.SetNRGBA(x, y, bg)
		}
	}
	for y := 30; y < chartH; y += 54 {
		for x := 0; x < chartW; x++ {
			img.SetNRGBA(x, y, rgbOf(0x2b2f37))
		}
	}
	for x := 40; x < chartW; x += 80 {
		for y := 0; y < chartH; y++ {
			img.SetNRGBA(x, y, rgbOf(0x2b2f37))
		}
	}
	// Samples every 8 px from a fixed linear congruential sequence.
	const deploy = 352
	seed := uint32(7)
	rnd := func(n int) int {
		seed = seed*1103515245 + 12345
		return int(seed>>16) % n
	}
	var p99, p50 []image.Point
	for x := 0; x <= chartW; x += 8 {
		a, b := 110+rnd(50), 200+rnd(20)
		if x < deploy && rnd(5) == 0 {
			a -= 40 + rnd(50)
		}
		if x >= deploy {
			a, b = 222+rnd(12), 250+rnd(8)
		}
		p99, p50 = append(p99, image.Pt(x, a)), append(p50, image.Pt(x, b))
	}
	// curveY is a series' height at x, between its samples.
	curveY := func(pts []image.Point, x int) int {
		i := min(x/8, len(pts)-2)
		p, q := pts[i], pts[i+1]
		return p.Y + (q.Y-p.Y)*(x-p.X)/(q.X-p.X)
	}
	blend := func(x, y int, c uint32, alpha int) {
		o := img.NRGBAAt(x, y)
		n := rgbOf(c)
		mix := func(a, b uint8) uint8 { return uint8((int(a)*(255-alpha) + int(b)*alpha) / 255) }
		img.SetNRGBA(x, y, color.NRGBA{mix(o.R, n.R), mix(o.G, n.G), mix(o.B, n.B), 255})
	}
	for x := 0; x < chartW; x++ {
		for y := curveY(p99, x); y < chartH; y++ {
			blend(x, y, 0xffb454, 36)
		}
	}
	// stroke draws a series as a line w px thick, filling the vertical
	// span between neighbouring columns so steep parts have no gaps.
	stroke := func(pts []image.Point, c uint32, w int) {
		prev := curveY(pts, 0)
		for x := 0; x < chartW; x++ {
			y := curveY(pts, x)
			for yy := min(prev, y); yy <= max(prev, y); yy++ {
				for dy := 0; dy < w; dy++ {
					for dx := 0; dx < w; dx++ {
						if px, py := x+dx-w/2, yy+dy-w/2; px >= 0 && px < chartW && py >= 0 && py < chartH {
							img.SetNRGBA(px, py, rgbOf(c))
						}
					}
				}
			}
			prev = y
		}
	}
	stroke(p50, 0x5aa9ff, 2)
	stroke(p99, 0xffb454, 3)
	for y := 0; y < chartH; y++ {
		if y/6%2 == 0 {
			img.SetNRGBA(deploy, y, rgbOf(0xc7b8ff))
			img.SetNRGBA(deploy+1, y, rgbOf(0xc7b8ff))
		}
	}
	return img
}

// kittyChunks encodes img as Kitty graphics, RGBA in chunks of 4096 base64
// bytes, as picture.kitty in gui_images_test.go and kitten icat do.
func kittyChunks(img *image.NRGBA) string {
	data := base64.StdEncoding.EncodeToString(img.Pix)
	var b strings.Builder
	for i := 0; i < len(data); i += 4096 {
		chunk := data[i:min(len(data), i+4096)]
		more := 0
		if i+4096 < len(data) {
			more = 1
		}
		if i == 0 {
			fmt.Fprintf(&b, "\x1b_Ga=T,f=32,s=%d,v=%d,m=%d;%s\x1b\\", img.Rect.Dx(), img.Rect.Dy(), more, chunk)
		} else {
			fmt.Fprintf(&b, "\x1b_Gm=%d;%s\x1b\\", more, chunk)
		}
	}
	return b.String()
}
