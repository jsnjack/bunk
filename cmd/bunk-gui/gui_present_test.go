package main

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bunk/internal/present"
)

// presentPlan is a Markdown document long enough to scroll, with a
// diagram, math, code, a table and a table wider than the panel.
func presentPlan() string {
	var b strings.Builder
	b.WriteString("# Plan\n\nIntro with $a^2 + b^2 = c^2$ inline.\n\n")
	b.WriteString("## Session storage\n\nSessions live in Redis, keyed by a random ID.\n\n")
	b.WriteString("```mermaid\ngraph LR\n  Client --> API --> Redis\n```\n\n$$\\sum_{i=1}^n i = \\frac{n(n+1)}{2}$$\n\n")
	b.WriteString("| Store | Revocation |\n|---|---|\n| Redis | instant |\n| JWT | at expiry |\n\n")
	for row := range 3 {
		b.WriteString("|")
		for col := range 14 {
			switch row {
			case 0:
				fmt.Fprintf(&b, " Column %d |", col+1)
			case 1:
				b.WriteString("---|")
			default:
				fmt.Fprintf(&b, " value %d cell |", col+1)
			}
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString("```go\nfunc main() { println(\"hi\") }\n```\n\n")
	for i := range 40 {
		b.WriteString("Filler paragraph " + strings.Repeat("words ", 30) + string(rune('a'+i%26)) + ".\n\n")
	}
	b.WriteString("## Rollout\n\nThe rollout section.\n\n")
	for range 40 {
		b.WriteString("More filler " + strings.Repeat("text ", 30) + "\n\n")
	}
	return b.String()
}

// docLayoutProbe reports, for presentPlan, whether the first paragraph is
// as wide as the measure (--bunk-measure), whether the wide table is wider
// than it, and whether that table scrolls. A scenario step is one line.
const docLayoutProbe = "(() => { " +
	"const doc = document.getElementById('bunk-doc'); " +
	"const measure = parseFloat(getComputedStyle(doc).getPropertyValue('--bunk-measure')); " +
	"const width = el => el.getBoundingClientRect().width; " +
	"const p = doc.querySelector(':scope > p'), table = doc.querySelectorAll(':scope > table')[1]; " +
	"return [Math.abs(width(p) - measure) < 1, width(table) > measure + 50, table.scrollWidth > table.clientWidth].join(','); " +
	"})()"

// TestGUIPresentMarkdown shows a Markdown file in the tab's document panel
// with "bunk-gui present" from the pane, as an agent runs it: the panel
// opens beside the pane, renders the diagram, math, table and code, and
// reloads in place when the file changes, keeping the reader at the same
// heading and marking the section that changed. A question about selected
// text is pasted into the pane that presented the document, unsent.
func TestGUIPresentMarkdown(t *testing.T) {
	t.Parallel()
	gui := filepath.Dir(guiBinary(t))
	g := runGUI(t, scenario{
		cost:  3,
		args:  []string{"--size", "100x30"},
		env:   []string{"PATH=" + gui + ":" + os.Getenv("PATH")},
		files: map[string]string{"plan.md": presentPlan()},
		script: []string{
			"maximize",
			"wait window.maximized=true window.w>1800 focused.line=$",
			"type bunk-gui present plan.md\\r",
			"wait tab.1.panel.open=true tab.1.panel.docs.0.loaded=true",
			"wait focused.text~\"Showing plan.md\"",
			"present-wait document.querySelector('.mermaid svg') && document.querySelectorAll('.katex').length >= 2",
			"present-eval [document.querySelectorAll('#bunk-doc h2').length, document.querySelectorAll('#bunk-doc table').length, document.querySelectorAll('pre code span[class]').length > 0].join(',')",
			"wait tab.1.panel.eval=\"2,2,true\"",
			// Text keeps the measure, and the wide table takes the
			// panel's width and scrolls in it.
			"present-eval " + docLayoutProbe,
			"wait tab.1.panel.eval=\"true,true,true\"",
			// In a page wider than the table, as in a browser window,
			// the table no longer scrolls and the text keeps the measure.
			"present-eval document.body.style.width = '2400px'; " + docLayoutProbe,
			"wait tab.1.panel.eval=\"true,true,false\"",
			"present-eval document.body.style.width = ''; 'reset'",
			"wait tab.1.panel.eval=reset",
			"snap shown",
			"shot shown.png",
			"present-eval document.getElementById('rollout').scrollIntoView(); String(Math.round(document.getElementById('rollout').getBoundingClientRect().top))",
			"wait tab.1.panel.eval=\"0\"",
			// The agent edits the section above the reader's place.
			"type sed -i 's/keyed by a random ID/keyed by a random 256-bit ID/' plan.md\\r",
			"wait tab.1.panel.docs.0.reloads=1 tab.1.panel.docs.0.changed=1",
			"present-eval [Math.round(document.getElementById('rollout').getBoundingClientRect().top), document.querySelectorAll('.bunk-changed').length, document.getElementById('session-storage').classList.contains('bunk-changed')].join(',')",
			"wait tab.1.panel.eval=\"0,1,true\"",
			"snap reloaded",
			"present-eval document.getElementById('session-storage').scrollIntoView(); 'ok'",
			"present-select keyed by a random 256-bit ID",
			"present-ask",
			"wait tab.1.panel.ask.section=\"Session storage\"",
			"snap ask",
			"kbd-type Why not Postgres?",
			"kbd Return",
			"wait tab.1.panel.ask=null focused.text~\"About plan.md › Session storage:\"",
			"snap sent",
			"quit",
		},
	})
	shown := g.snap("shown")
	pn := shown.Tabs[0].Panel
	if pn == nil || !pn.Open || pn.Rect == nil {
		t.Fatalf("panel after present: %+v", pn)
	}
	pane := shown.pane(t, 0)
	if pane.Rect == nil || pane.Rect.X+pane.Rect.W > pn.Rect.X+1 {
		t.Errorf("pane %+v overlaps the panel %+v", pane.Rect, pn.Rect)
	}
	if pn.Rect.W < panelMinWidth-1 || pn.Rect.W > float64(shown.Window.W)/2 {
		t.Errorf("panel width %v in a window %v wide", pn.Rect.W, shown.Window.W)
	}
	if d := pn.Docs[0]; d.Kind != "markdown" || d.Pane != 0 || d.Auto || filepath.Base(d.Path) != "plan.md" {
		t.Errorf("document: %+v", d)
	}
	if ask := g.snap("ask").Tabs[0].Panel.Ask; ask == nil || ask.Target != 0 || ask.Quote != "keyed by a random 256-bit ID" {
		t.Errorf("question popover: %+v", ask)
	}
	want := "About plan.md › Session storage:\n> keyed by a random 256-bit ID\nWhy not Postgres?"
	if got := g.snap("sent").Tabs[0].Panel.Sent; got != want {
		t.Errorf("pasted question:\n%s\nwant:\n%s", got, want)
	}
	if s := g.snap("sent"); s.FocusedPane != 0 || s.KeyboardFocus != "pane 0" {
		t.Errorf("after the question: focused pane %d, keyboard %q", s.FocusedPane, s.KeyboardFocus)
	}
	// Pasted, not sent: the shell did not run it.
	if text := strings.Join(g.snap("sent").pane(t, 0).Text, "\n"); strings.Contains(text, "not found") {
		t.Errorf("the question ran as a command:\n%s", text)
	}
}

// TestGUIPresentHTML shows an agent's HTML page reported by a hook, which
// opens the panel with [present] auto. The page runs its scripts and reads
// files of its own directory, but reaches neither the network, nor other
// files on disk, nor hidden files beside it. A page whose web process ends
// loads again once; ending it again keeps it down until its file changes.
// A page whose file went comes back with the file, and closing the tab ends
// its web view.
func TestGUIPresentHTML(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		cost:   2,
		args:   []string{"--size", "100x30"},
		config: "[present]\nauto = true\n",
		files: map[string]string{
			"data.txt": "local data",
			".secret":  "hidden",
			"page.html": `<!doctype html><html><head><title>Page</title></head><body>
<div id="result"></div><img id="remote" src="https://example.com/x.png">
<script>
function probe(name, url) {
  return fetch(url).then(r => r.text()).then(t => name + ":" + (t.length ? "ok" : "empty"), () => name + ":blocked");
}
Promise.all([
  probe("net", "https://example.com/"),
  probe("file", "file:///etc/hostname"),
  probe("same", "data.txt"),
  probe("hidden", ".secret"),
]).then(parts => {
  document.getElementById("result").textContent = parts.join(" ");
  document.body.dataset.done = "1";
});
</script></body></html>`,
		},
		script: []string{
			"wait focused.line=$",
			"present page.html auto",
			"wait tab.1.panel.open=true tab.1.panel.docs.0.loaded=true run.web_views=1",
			"present-wait document.body.dataset.done === '1'",
			"present-eval document.getElementById('result').textContent + ' img:' + document.getElementById('remote').naturalWidth",
			"wait tab.1.panel.eval=\"net:blocked file:blocked same:ok hidden:blocked img:0\"",
			"snap html",
			"present-kill",
			"wait tab.1.panel.docs.0.crashed=true",
			"present-wait document.body.dataset.done === '1'",
			"present-kill",
			"wait tab.1.panel.status~\"stopped twice\"",
			"type echo >> page.html\\r",
			"wait tab.1.panel.docs.0.reloads=1 tab.1.panel.docs.0.crashed=false tab.1.panel.status!~\"Could not\"",
			"present-wait document.body.dataset.done === '1'",
			"type mv page.html page.gone\\r",
			"wait tab.1.panel.status~\"Could not show\"",
			"type mv page.gone page.html\\r",
			"wait tab.1.panel.docs.0.reloads=3 tab.1.panel.status!~\"Could not\"",
			"present-wait document.body.dataset.done === '1'",
			"new-tab",
			"wait tabs.#=2 selected_tab=2",
			"select-tab 1",
			"wait selected_tab=1",
			"close-tab",
			"wait tabs.#=1 run.web_views=0",
			"quit",
		},
	})
	d := g.snap("html").Tabs[0].Panel.Docs[0]
	if d.Kind != "html" || !d.Auto {
		t.Errorf("document: %+v", d)
	}
}

// TestGUIPresentListed: without [present] auto, a document a hook reports
// is listed in the panel, which stays closed until the user opens it. A
// toast says so, and counts the documents listed while it is up; its Show
// button shows the last one.
func TestGUIPresentListed(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		args:  []string{"--size", "100x30"},
		files: map[string]string{"notes.md": "# Notes\n\nHello.\n", "todo.md": "# Todo\n"},
		script: []string{
			"wait focused.line=$",
			"present notes.md auto",
			"wait tab.1.panel.docs.#=1 tab.1.panel.unseen=1 toast_button=Show last_toast=\"notes.md listed in the panel\"",
			"present todo.md auto",
			"wait tab.1.panel.docs.#=2 tab.1.panel.unseen=2 last_toast=\"2 documents listed in the panel\"",
			"snap listed",
			"toast-action",
			"wait tab.1.panel.open=true tab.1.panel.current=1 tab.1.panel.docs.1.loaded=true tab.1.panel.unseen=0",
			"present-eval document.querySelector('#bunk-doc h1').textContent",
			"wait tab.1.panel.eval=\"Todo\"",
			"type rm todo.md\\r",
			"wait tab.1.panel.status~\"Could not show\"",
			"present-toggle",
			"wait tab.1.panel.open=false",
			"snap hidden",
			"quit",
		},
	})
	if pn := g.snap("listed").Tabs[0].Panel; pn == nil || pn.Open || len(pn.Docs) != 2 || !pn.Docs[0].Auto || !pn.Docs[1].Auto {
		t.Errorf("listed: %+v", pn)
	}
	if s := g.snap("hidden"); s.KeyboardFocus != "pane 0" {
		t.Errorf("hiding the panel left the keyboard with %q", s.KeyboardFocus)
	}
}

// TestGUIPresentPrefs: the Agents page of the preferences lists the
// bunk-present skill of Claude Code, Codex and Copilot CLI, missing in an
// empty home directory. Set up writes Codex's with its hooks, and the
// switch saves [present] auto in config.toml and holds without a restart.
func TestGUIPresentPrefs(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	g := runGUI(t, scenario{
		cost:   2,
		config: "# mine\n",
		args:   []string{"--size", "100x30", "--agent-home", home},
		files:  map[string]string{"notes.md": "# Notes\n"},
		script: []string{
			"maximize", "wait window.maximized=true window.w>1800",
			"kbd ctrl+comma",
			"wait prefs!=null prefs.skills.#=3",
			"snap before",
			"pref-scroll setup-codex",
			"ptr-at pref setup-codex", "ptr-click",
			"wait prefs.skills.1.state=installed prefs.hooks.1.state=installed prefs.setup_note~Installed",
			"snap installed",
			"pref-scroll present-auto",
			"ptr-at pref present-auto", "ptr-click",
			"wait prefs.present_auto=true run.reloads>=1",
			"dialog-close",
			"wait prefs=null",
			// The switch holds at once: a hook's document opens the panel.
			"present notes.md auto",
			"wait tab.1.panel.open=true",
			"quit",
		},
	})
	var agents []string
	for _, s := range g.snap("before").Prefs.Skills {
		agents = append(agents, s.Agent)
		if s.State != "missing" || !strings.HasPrefix(s.Path, home) {
			t.Errorf("before: %+v", s)
		}
	}
	if strings.Join(agents, ",") != "claude,codex,copilot" {
		t.Errorf("skills of %q", agents)
	}
	path := filepath.Join(home, ".codex", "skills", "bunk-present", "SKILL.md")
	if data, err := os.ReadFile(path); err != nil || !strings.Contains(string(data), "name: bunk-present") {
		t.Errorf("codex skill: %v\n%.200s", err, data)
	}
	if note := g.snap("installed").Prefs.SetupNote; !strings.Contains(note, "~/.codex/skills/bunk-present/SKILL.md") {
		t.Errorf("install note %q", note)
	}
	if got := readFile(t, g.file("config.toml")); !strings.Contains(got, "[present]\nauto = true") || !strings.Contains(got, "# mine") {
		t.Errorf("config.toml:\n%s", got)
	}
}

// tinyPDF is a PDF with one blank page.
const tinyPDF = "%PDF-1.4\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj\n" +
	"3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 200 100]>>endobj\ntrailer<</Root 1 0 R>>\n%%EOF\n"

// designPage is an agent's HTML page that loads bunk's page runtime for a
// diagram and math, and takes its colours from the theme.
const designPage = `<!doctype html><html><head><title>Design</title>
<script src="bunk-present://assets/bunk-doc.js" defer></script>
<style>body{background:var(--bunk-bg);color:var(--bunk-fg)}</style></head>
<body><h1>Design</h1><pre class="mermaid">graph LR
  A --> B</pre><p>Cost <span class="math">a^2+b^2</span>.</p></body></html>`

// TestGUIPresentPreview previews files as the file dialog of open_in_panel
// does: code highlighted by its name and reloaded when it changes, an
// image, a PDF in WebKit's viewer, text without an extension, and an
// agent's HTML page that draws a diagram and math with bunk's runtime in
// the theme's colours. The browser opens the image as it is, and a copy
// of the HTML page that loads bunk's runtime from disk in the theme's
// colours. A binary file is refused with a toast, and the action opens the
// file dialog.
func TestGUIPresentPreview(t *testing.T) {
	t.Parallel()
	cache := t.TempDir()
	var shot bytes.Buffer
	if err := png.Encode(&shot, image.NewRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	g := runGUI(t, scenario{
		cost: 2,
		args: []string{"--size", "100x30"},
		env:  []string{"XDG_CACHE_HOME=" + cache},
		files: map[string]string{
			"main.go": "package main\n\nfunc main() {}\n", "shot.png": shot.String(), "doc.pdf": tinyPDF,
			"design.html": designPage, "notes": "plain notes\n", "data.bin": "\x00\x01\x02",
		},
		script: []string{
			"wait focused.line=$",
			"preview main.go",
			"wait tab.1.panel.open=true tab.1.panel.docs.0.loaded=true",
			// A preview fills the page, however wide, unlike a document's text.
			"present-eval [document.querySelectorAll('pre.chroma .kn').length > 0, getComputedStyle(document.documentElement).getPropertyValue('--bunk-dark').trim(), (document.body.style.width = '2400px', Math.round(document.getElementById('bunk-doc').getBoundingClientRect().width))].join(',')",
			"wait tab.1.panel.eval=\"true,1,2400\"",
			"type echo '// more' >> main.go\\r",
			"wait tab.1.panel.docs.0.reloads=1",
			"preview shot.png",
			"wait tab.1.panel.current=1 tab.1.panel.docs.1.loaded=true",
			// The page can finish loading before the image is decoded.
			"present-wait document.querySelector('img').naturalWidth === 3",
			"present-browser",
			"wait run.opened.#=1",
			"preview doc.pdf",
			"wait tab.1.panel.current=2 tab.1.panel.docs.2.loaded=true",
			"present-eval document.contentType",
			"wait tab.1.panel.eval=application/pdf",
			"preview design.html",
			"wait tab.1.panel.current=3 tab.1.panel.docs.3.loaded=true",
			"present-wait document.querySelector('.mermaid svg') && document.querySelector('.katex')",
			// KaTeX's stylesheet hides the MathML copy of the formula.
			"present-wait getComputedStyle(document.querySelector('.katex-mathml')).position === 'absolute'",
			"present-eval getComputedStyle(document.documentElement).getPropertyValue('--bunk-bg').trim() + ' ' + getComputedStyle(document.body).backgroundColor",
			"wait tab.1.panel.eval~rgb",
			"snap design",
			"present-browser",
			"wait run.opened.#=2",
			"preview notes",
			"wait tab.1.panel.current=4 tab.1.panel.docs.4.loaded=true",
			"preview data.bin",
			"wait last_toast~\"Cannot preview data.bin\"",
			"snap previews",
			"menu-item Open in panel…",
			"wait open_dialog=true",
			"quit",
		},
	})
	var kinds []string
	for _, d := range g.snap("previews").Tabs[0].Panel.Docs {
		kinds = append(kinds, d.Kind)
	}
	if strings.Join(kinds, ",") != "text,image,pdf,html,text" {
		t.Errorf("documents of kinds %q", kinds)
	}
	var hex string
	var r, gr, b int
	if _, err := fmt.Sscanf(g.snap("design").Tabs[0].Panel.Eval, "%s rgb(%d, %d, %d)", &hex, &r, &gr, &b); err != nil || hex != fmt.Sprintf("#%02x%02x%02x", r, gr, b) {
		t.Errorf("the page's background %q is not the theme's (%v)", g.snap("design").Tabs[0].Panel.Eval, err)
	}
	if len(g.Opened) != 2 || g.Opened[0] != present.FileURL(filepath.Join(g.dir, "shot.png")) {
		t.Fatalf("opened %q, want the image, then the HTML page's copy", g.Opened)
	}
	page := strings.TrimPrefix(g.Opened[1], "file://")
	if !strings.HasPrefix(page, filepath.Join(cache, present.CacheDir)+"/") {
		t.Fatalf("the HTML page opened as %s, not as a copy in the cache", g.Opened[1])
	}
	src, err := os.ReadFile(page)
	if err != nil {
		t.Fatal(err)
	}
	if html := string(src); strings.Contains(html, present.AssetsURL) || !strings.Contains(html, "--bunk-bg:") || !strings.Contains(html, "bunk-doc.js") {
		t.Errorf("the page's copy does not load bunk's runtime from disk in the theme's colours:\n%s", html)
	}
}

// filler is n short paragraphs, so a document scrolls.
func filler(n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "Paragraph %d of filler text that makes the page scroll.\n\n", i+1)
	}
	return b.String()
}

// TestGUIPresentLinks follows links between documents in the panel. A
// click on a link to another file beside the document shows that file as
// a document of its own, at the heading the link names: questions about it
// name it, and it reloads when it changes. Back returns to the first
// document where the reader left it. A link to a hidden file is refused
// with a toast.
func TestGUIPresentLinks(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		cost: 2,
		args: []string{"--size", "100x30"},
		files: map[string]string{
			"plan.md":    "# Plan\n\n" + filler(40) + "See [the spec](spec.md#second) and [a secret](.secret.md).\n",
			"spec.md":    "# Spec\n\n" + filler(40) + "## Second\n\nThe second part.\n\n" + filler(80),
			".secret.md": "# Secret\n",
		},
		script: []string{
			"maximize",
			"wait window.maximized=true focused.line=$",
			"present plan.md",
			"wait tab.1.panel.open=true tab.1.panel.docs.0.loaded=true tab.1.panel.back=false",
			"present-eval document.querySelector('a[href^=\"spec.md\"]').scrollIntoView(); 'ok'",
			"wait tab.1.panel.eval=ok",
			"ptr-at doc a[href^=\"spec.md\"]",
			"ptr-click",
			"wait tab.1.panel.current=1 tab.1.panel.docs.1.loaded=true tab.1.panel.back=true",
			"present-wait Math.abs(document.getElementById('second').getBoundingClientRect().top) < 2 && window.scrollY > 0",
			"present-select The second part",
			"present-ask",
			"wait tab.1.panel.ask.section=Second",
			"snap ask",
			"kbd Escape",
			"wait tab.1.panel.ask=null",
			"type echo 'Spec, edited.' >> spec.md\\r",
			"wait tab.1.panel.docs.1.reloads=1",
			"present-wait document.body.textContent.indexOf('Spec, edited.') >= 0",
			"present-back",
			"wait tab.1.panel.current=0 tab.1.panel.docs.0.loaded=true tab.1.panel.back=false",
			// Back at the link the reader clicked.
			"present-wait window.scrollY > 0 && document.querySelector('h1').textContent === 'Plan'",
			"present-eval document.querySelector('a[href=\".secret.md\"]').scrollIntoView(); 'secret'",
			"wait tab.1.panel.eval=secret",
			"ptr-at doc a[href=\".secret.md\"]",
			"ptr-click",
			"wait last_toast~\"Cannot open .secret.md\"",
			"snap end",
			"quit",
		},
	})
	ask := g.snap("ask").Tabs[0].Panel
	if ask.Ask == nil || !strings.Contains(ask.Ask.Title, "spec.md › Second") {
		t.Errorf("the question about the linked document: %+v", ask.Ask)
	}
	if d := ask.Docs[1]; filepath.Base(d.Path) != "spec.md" || d.Kind != "markdown" || d.Pane != 0 {
		t.Errorf("the linked document: %+v", d)
	}
	if end := g.snap("end").Tabs[0].Panel; len(end.Docs) != 2 || end.Current != 0 {
		t.Errorf("documents after the hidden link: %+v", end.Docs)
	}
}

// TestGUIPresentFindZoom finds and zooms in the panel with the keys of
// the search and text size actions, which do so while the panel has the
// keyboard. The find bar counts the matches and steps through them; Esc
// closes it. Zoom scales the pages, not the terminal's text, and shows in
// the status line. In the pane the same keys act on the terminal.
func TestGUIPresentFindZoom(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		cost: 2,
		args: []string{"--size", "100x30"},
		files: map[string]string{
			"plan.md": "# Plan\n\nA needle here.\n\n" + filler(30) + "Another needle.\n\n" + filler(30) + "The last needle.\n",
		},
		script: []string{
			"maximize",
			"wait window.maximized=true focused.line=$",
			"present plan.md",
			"wait tab.1.panel.open=true tab.1.panel.docs.0.loaded=true",
			"ptr-at doc h1",
			"ptr-click",
			"wait keyboard_focus=panel",
			"kbd ctrl+f",
			"wait keyboard_focus=\"panel find\"",
			"kbd-type needle",
			"wait tab.1.panel.find.count=1/3",
			"kbd Return",
			"wait tab.1.panel.find.count=2/3",
			"kbd ctrl+p",
			"wait tab.1.panel.find.count=1/3",
			"kbd ctrl+p",
			"wait tab.1.panel.find.count=3/3",
			"present-wait window.scrollY > 0",
			"snap found",
			"kbd Escape",
			"wait tab.1.panel.find=null keyboard_focus=panel",
			"kbd ctrl+equal",
			"wait tab.1.panel.zoom=110 text_percent=100",
			"kbd ctrl+minus",
			"kbd ctrl+minus",
			"wait tab.1.panel.zoom=90",
			"kbd ctrl+0",
			"wait tab.1.panel.zoom=100",
			"kbd ctrl+equal",
			"wait tab.1.panel.zoom=110 tab.1.panel.status~110%",
			"ptr-at pane 0",
			"ptr-click",
			"wait keyboard_focus=\"pane 0\"",
			"kbd ctrl+equal",
			"wait text_percent>100 tab.1.panel.zoom=110",
			"quit",
		},
	})
	if f := g.snap("found").Tabs[0].Panel.Find; f == nil || f.Query != "needle" {
		t.Errorf("find bar: %+v", f)
	}
}

// statePage is an agent's HTML page with state a reload would lose: two
// sections, a checkbox, a slider whose value a script shows, and tabs.
const statePage = `<!doctype html><html><head><title>State</title></head><body>
<h1>State</h1>
<details id="more"><summary>More</summary><p>Hidden text</p></details>
<details><summary>Second</summary><p>Also hidden</p></details>
<label><input type="checkbox" name="done" value="a"> Done</label>
<input type="range" id="level" min="0" max="10" value="2"><output id="out">2</output>
<div role="tablist"><button role="tab" id="t1" aria-selected="true">One</button><button role="tab" id="t2" aria-selected="false">Two</button></div>
<div id="p1">Panel one</div><div id="p2" hidden>Panel two</div>
<script>
document.getElementById('level').addEventListener('input', function (e) { document.getElementById('out').textContent = e.target.value; });
document.querySelectorAll('[role=tab]').forEach(function (t) {
  t.addEventListener('click', function () {
    document.querySelectorAll('[role=tab]').forEach(function (o) { o.setAttribute('aria-selected', o === t ? 'true' : 'false'); });
    document.getElementById('p1').hidden = t.id !== 't1';
    document.getElementById('p2').hidden = t.id !== 't2';
  });
});
</script>
<!-- v1 -->
</body></html>`

// TestGUIPresentKeepsState reloads an HTML page whose file changed and
// gives it back what the reader did: the section they opened, the box
// they ticked, the slider they moved (its script sees the input) and the
// tab they chose.
func TestGUIPresentKeepsState(t *testing.T) {
	t.Parallel()
	runGUI(t, scenario{
		cost:  2,
		args:  []string{"--size", "100x30"},
		files: map[string]string{"state.html": statePage},
		script: []string{
			"wait focused.line=$",
			"present state.html",
			"wait tab.1.panel.open=true tab.1.panel.docs.0.loaded=true",
			"present-eval document.querySelectorAll('details')[1].open = true; document.querySelector('[name=done]').checked = true; " +
				"var l = document.getElementById('level'); l.value = '7'; l.dispatchEvent(new Event('input')); document.getElementById('t2').click(); 'set'",
			"wait tab.1.panel.eval=set",
			"type sed -i 's/v1/v2/' state.html\\r",
			"wait tab.1.panel.docs.0.reloads=1",
			"present-wait document.body.innerHTML.indexOf('v2') >= 0 && document.querySelectorAll('details')[1].open && !document.getElementById('more').open && " +
				"document.querySelector('[name=done]').checked && document.getElementById('out').textContent === '7' && !document.getElementById('p2').hidden",
			"quit",
		},
	})
}

// TestGUIPresentProblems checks documents as "bunk present" does: the
// check gets the page's problems once it has drawn, and the status line
// counts them. A Markdown document reports a diagram and a formula that
// do not parse and an image that is not there; an HTML page reports a
// script error, a remote image its policy blocked and a missing file. A
// fixed file loads again before the next check, which finds nothing.
func TestGUIPresentProblems(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		cost: 2,
		args: []string{"--size", "100x30"},
		files: map[string]string{
			"broken.md": "# Broken\n\n```mermaid\ngraph LR\n  A -->\n```\n\n$\\frac{1}{$\n\n![shot](missing.png)\n",
			"page.html": `<!doctype html><html><head><title>P</title><script src="bunk-present://assets/bunk-doc.js" defer></script></head>` +
				`<body><h1>P</h1><script>undefinedFunction();</script><img src="https://example.com/x.png"><img src="gone.png">` +
				`<pre class="mermaid">graph LR; A-->B</pre></body></html>`,
		},
		script: []string{
			"wait focused.line=$",
			"present broken.md check",
			"wait tab.1.panel.checked.#=3",
			"snap broken",
			"present page.html check",
			"wait tab.1.panel.checked.#=3",
			"snap page",
			"type printf '# Fixed\\\\n' > broken.md && echo fixed-$((40+2))\\r",
			"wait focused.text~fixed-42",
			"present broken.md check",
			"wait tab.1.panel.checked!=null tab.1.panel.current=0",
			"snap fixed",
			"present-eval document.querySelector('h1').textContent",
			"wait tab.1.panel.eval=Fixed",
			"quit",
		},
	})
	has := func(label string, want ...string) {
		t.Helper()
		pn := g.snap(label).Tabs[0].Panel
		if pn.Checked == nil {
			t.Fatalf("%s: no check answered", label)
		}
		all := strings.Join(*pn.Checked, "\n")
		for _, w := range want {
			if !strings.Contains(all, w) {
				t.Errorf("%s: problems lack %q:\n%s", label, w, all)
			}
		}
		if !strings.HasPrefix(pn.Status, "3 problems · ") {
			t.Errorf("%s: status %q", label, pn.Status)
		}
	}
	has("broken", "Mermaid diagram 1: Parse error", "math 1: KaTeX parse error", "cannot load missing.png: not found")
	if pn := g.snap("fixed").Tabs[0].Panel; pn.Checked == nil || len(*pn.Checked) != 0 || strings.Contains(pn.Status, "problem") {
		t.Errorf("the fixed document: problems %v, status %q", pn.Checked, pn.Status)
	}
	has("page", "script error: ", "undefinedFunction", "blocked https://example.com/x.png", "cannot load gone.png: not found")
}

// kitPage uses every component of the page runtime's kit.
const kitPage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Kit</title>
<link rel="stylesheet" href="bunk-present://assets/bunk-doc.css">
<script src="bunk-present://assets/bunk-doc.js" defer></script>
</head><body><article id="bunk-doc">
<h1 id="kit">Kit demo</h1>
<nav class="bunk-toc"></nav>
<h2 id="status">Status</h2>
<p>Build <span class="bunk-badge good">passing</span> <span class="bunk-badge warn">2 flaky</span> <span class="bunk-badge bad">1 failing</span> <span class="bunk-badge info">v1.19</span></p>
<div class="bunk-callout warning"><p class="bunk-callout-title">Warning</p><p>Revocation takes up to five minutes.</p></div>
<h2 id="options">Options</h2>
<div class="bunk-tabs">
<section data-title="Redis"><p>Sessions in Redis, revoked at once.</p></section>
<section data-title="JWT"><p>Signed tokens, revoked at expiry.</p></section>
</div>
<h2 id="data">Data</h2>
<table class="bunk-sort bunk-filter"><thead><tr><th>Store</th><th>Latency</th><th>Cost</th></tr></thead>
<tbody><tr><td>Redis</td><td>1.2 ms</td><td>$40</td></tr><tr><td>Postgres</td><td>4 ms</td><td>$25</td></tr><tr><td>JWT</td><td>0 ms</td><td>$0</td></tr></tbody></table>
<h2 id="flow">Flow</h2>
<div class="bunk-steps">
<section data-title="Client sends the token"><p>The browser sends its cookie.</p></section>
<section data-title="API checks Redis"><p>The API looks the session up.</p></section>
<section data-title="API answers"><p>200 or 401.</p></section>
</div>
<h3 id="change">Change</h3>
<pre class="bunk-diff">--- a/session.go
+++ b/session.go
@@ -1,3 +1,3 @@
 func check(id string) bool {
-	return cache[id]
+	return redis.Exists(id)
 }</pre>
<pre><code>make check-all</code></pre>
</article></body></html>
`

// TestGUIPresentKit runs the kit in the panel: an outline, tabs the user
// clicks (the choice holds when the page reloads), steps, a table that
// sorts and filters, a coloured diff and a copy button that puts the code
// on the clipboard. A Markdown table sorts without markup of its own.
func TestGUIPresentKit(t *testing.T) {
	t.Parallel()
	runGUI(t, scenario{
		cost: 2,
		args: []string{"--size", "100x30"},
		files: map[string]string{
			"kit.html": kitPage,
			"table.md": "# T\n\n| Name | Size |\n|---|---|\n| b | 10 |\n| a | 9 |\n| c | 100 |\n",
		},
		script: []string{
			"maximize",
			"wait window.maximized=true focused.line=$",
			"present kit.html",
			"wait tab.1.panel.open=true tab.1.panel.docs.0.loaded=true",
			"present-wait document.querySelectorAll('[role=tab]').length === 5 && document.querySelectorAll('nav.bunk-toc li').length === 5",
			"ptr-at doc .bunk-tabs [role=tab]:nth-child(2)",
			"ptr-click",
			"present-wait !document.querySelectorAll('.bunk-tabs section')[1].hidden",
			"present-eval document.querySelector('.bunk-code:last-of-type').scrollIntoView(); 'ok'",
			"wait tab.1.panel.eval=ok",
			"ptr-at doc .bunk-code:last-of-type .bunk-copy",
			"ptr-click",
			"wait clipboard \"make check-all\"",
			"type sed -i 's/Kit demo/Kit demo, edited/' kit.html\\r",
			"wait tab.1.panel.docs.0.reloads=1",
			"present-wait document.querySelector('h1').textContent === 'Kit demo, edited' && !document.querySelectorAll('.bunk-tabs section')[1].hidden",
			"present-eval [document.querySelectorAll('pre.bunk-diff .d-add').length, document.querySelectorAll('pre.bunk-diff .d-del').length].join(',')",
			"wait tab.1.panel.eval=1,1",
			"present table.md",
			"wait tab.1.panel.current=1 tab.1.panel.docs.1.loaded=true",
			"present-eval document.querySelectorAll('th')[1].click(); Array.from(document.querySelectorAll('tbody tr')).map(function (r) { return r.cells[0].textContent; }).join('')",
			"wait tab.1.panel.eval=abc",
			"quit",
		},
	})
}

// libsPage draws with every library a page can use: a chart, a graph,
// highlighted code and D3.
const libsPage = `<!doctype html><html><head><title>Libs</title>
<link rel="stylesheet" href="bunk-present://assets/bunk-doc.css">
<script src="bunk-present://assets/bunk-doc.js" defer></script></head>
<body><article id="bunk-doc"><h1>Libs</h1>
<pre class="bunk-chart" data-height="240">{"xAxis": {"type": "category", "data": ["a", "b"]}, "yAxis": {}, "series": [{"type": "bar", "data": [1, 2]}]}</pre>
<pre class="bunk-graphviz">digraph { a -> b }</pre>
<pre><code class="language-go">func main() { fmt.Println("hi") }</code></pre>
<div id="d3"></div>
<script>
document.addEventListener('DOMContentLoaded', function () {
  bunkDoc.use('d3').then(function (d3) { d3.select('#d3').append('p').attr('id', 'd3-made').text('made by d3'); });
});
</script>
</article></body></html>`

// TestGUIPresentLibraries draws a chart with ECharts, a graph with
// Graphviz (WebAssembly, in an HTML page and in Markdown, whose policy
// allows it), code with highlight.js and an element with D3, and reports a
// chart that is not JSON and a graph that does not parse as problems.
func TestGUIPresentLibraries(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		cost: 2,
		args: []string{"--size", "100x30"},
		files: map[string]string{
			"libs.html": libsPage,
			"libs.md": "# Libs\n\n```chart\n{\"series\": [{\"type\": \"pie\", \"data\": [1, 2]}]}\n```\n\n```dot\ndigraph { x -> y }\n```\n\n" +
				"```chart\n{not json\n```\n\n```dot\ndigraph { -> }\n```\n",
		},
		script: []string{
			"wait focused.line=$",
			"present libs.html check",
			"wait tab.1.panel.checked!=null",
			"present-eval [document.querySelectorAll('.bunk-chart-box svg').length, document.querySelectorAll('pre.bunk-graphviz svg').length, " +
				"document.querySelectorAll('code.hljs span[class^=hljs-]').length > 0, !!document.getElementById('d3-made')].join(',')",
			"wait tab.1.panel.eval=1,1,true,true",
			"snap html",
			"present libs.md check",
			"wait tab.1.panel.checked!=null tab.1.panel.current=1",
			"present-eval [document.querySelectorAll('.bunk-chart-box svg').length, document.querySelectorAll('pre.bunk-graphviz svg').length].join(',')",
			"wait tab.1.panel.eval=1,1",
			"snap md",
			"quit",
		},
	})
	if c := g.snap("html").Tabs[0].Panel.Checked; c == nil || len(*c) != 0 {
		t.Errorf("the HTML page had problems: %v", c)
	}
	c := g.snap("md").Tabs[0].Panel.Checked
	if c == nil {
		t.Fatal("no check of the Markdown page")
	}
	all := strings.Join(*c, "\n")
	for _, want := range []string{"chart 2: the option is not JSON", "Graphviz graph 2: "} {
		if !strings.Contains(all, want) {
			t.Errorf("problems lack %q:\n%s", want, all)
		}
	}
	if len(*c) != 2 {
		t.Errorf("problems: %q, want two", *c)
	}
}

// TestGUIPresentAnchor: "bunk-gui present FILE#ID" opens the document at
// that heading, and again at it when the document is shown already.
func TestGUIPresentAnchor(t *testing.T) {
	t.Parallel()
	gui := filepath.Dir(guiBinary(t))
	runGUI(t, scenario{
		cost:  2,
		args:  []string{"--size", "100x30"},
		env:   []string{"PATH=" + gui + ":" + os.Getenv("PATH")},
		files: map[string]string{"spec.md": "# Spec\n\n" + filler(40) + "## Rollout\n\nRoll out slowly.\n\n" + filler(80)},
		script: []string{
			"maximize",
			"wait window.maximized=true focused.line=$",
			"type bunk-gui present spec.md#rollout\\r",
			"wait tab.1.panel.open=true tab.1.panel.docs.0.loaded=true focused.text~\"Showing spec.md#rollout\"",
			"present-wait Math.abs(document.getElementById('rollout').getBoundingClientRect().top) < 2",
			"present-eval window.scrollTo(0, 0); 'top'",
			"wait tab.1.panel.eval=top",
			"present spec.md#rollout",
			"present-wait Math.abs(document.getElementById('rollout').getBoundingClientRect().top) < 2",
			"quit",
		},
	})
}

// TestGUIPresentReview collects a review: two comments, their passages
// highlighted in the page, again after the document reloads; then pastes
// them as one message into the agent's input and clears the highlights. A
// question takes several lines with Shift+Enter.
func TestGUIPresentReview(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		cost:  2,
		args:  []string{"--size", "100x30"},
		files: map[string]string{"plan.md": presentPlan()},
		script: []string{
			"wait focused.line=$",
			"present plan.md",
			"wait tab.1.panel.open=true tab.1.panel.docs.0.loaded=true",
			"present-select keyed by a random ID",
			"present-ask",
			"wait tab.1.panel.ask.section=\"Session storage\"",
			"present-review-add Why Redis?",
			"wait tab.1.panel.review.#=1 tab.1.panel.ask=null",
			"present-select instant",
			"present-ask",
			"wait tab.1.panel.ask!=null",
			"present-review-add Really instant?",
			"wait tab.1.panel.review.#=2",
			"present-wait CSS.highlights.get('bunk-review') && CSS.highlights.get('bunk-review').size === 2",
			"type sed -i 's/Intro with/Introduction with/' plan.md\\r",
			"wait tab.1.panel.docs.0.reloads=1",
			"present-wait CSS.highlights.get('bunk-review') && CSS.highlights.get('bunk-review').size === 2",
			"present-review-send",
			"wait tab.1.panel.review.#=0 tab.1.panel.sent~\"Review of plan.md:\"",
			"snap sent",
			"present-wait !CSS.highlights.get('bunk-review')",
			"present-select keyed by a random ID",
			"present-ask",
			"wait tab.1.panel.ask!=null",
			"kbd-type line one",
			"kbd shift+Return",
			"kbd-type line two",
			"kbd Return",
			"wait tab.1.panel.ask=null tab.1.panel.sent~\"line two\"",
			"snap multi",
			"quit",
		},
	})
	want := "Review of plan.md:\n\n1. Session storage\n> keyed by a random ID\nWhy Redis?\n\n2. Session storage\n> instant\nReally instant?"
	if got := g.snap("sent").Tabs[0].Panel.Sent; got != want {
		t.Errorf("review:\n%s\nwant:\n%s", got, want)
	}
	if got := g.snap("multi").Tabs[0].Panel.Sent; !strings.HasSuffix(got, "\nline one\nline two") {
		t.Errorf("multi-line question:\n%s", got)
	}
}

// TestGUIPresentReply: a page's reply button opens the question popover
// with its text and section, and Enter pastes it into the agent's input;
// a reply the page's script sends without the user's click is dropped.
func TestGUIPresentReply(t *testing.T) {
	t.Parallel()
	page := `<!doctype html><html><head><title>Choice</title><script src="bunk-present://assets/bunk-doc.js" defer></script></head>` +
		`<body><h1>Choice</h1><h2 id="options">Options</h2><p>Pick one.</p>` +
		`<button id="b" data-bunk-reply="Go with option B">Choose B</button></body></html>`
	g := runGUI(t, scenario{
		cost:  2,
		args:  []string{"--size", "100x30"},
		files: map[string]string{"choice.html": page},
		script: []string{
			"maximize",
			"wait window.maximized=true focused.line=$",
			"present choice.html",
			"wait tab.1.panel.open=true tab.1.panel.docs.0.loaded=true",
			"present-eval bunkDoc.reply('sneaky') ? 'posted' : 'not'",
			"wait tab.1.panel.eval=posted",
			"sleep 300ms",
			"wait tab.1.panel.ask=null",
			"ptr-at doc #b",
			"ptr-click",
			"wait tab.1.panel.ask.section=Options tab.1.panel.ask.title~\"Reply to\" tab.1.panel.ask.quote=\"\"",
			"kbd Return",
			"wait tab.1.panel.ask=null tab.1.panel.sent~\"Go with option B\"",
			"snap sent",
			"quit",
		},
	})
	if got, want := g.snap("sent").Tabs[0].Panel.Sent, "About choice.html › Options:\nGo with option B"; got != want {
		t.Errorf("reply:\n%s\nwant:\n%s", got, want)
	}
}

// TestGUIPresentLiveCopy: once the user opened a document in the browser,
// its copy is written again with a new version when the file changes, so
// the open copy reloads.
func TestGUIPresentLiveCopy(t *testing.T) {
	t.Parallel()
	cache := t.TempDir()
	g := runGUI(t, scenario{
		cost:  2,
		args:  []string{"--size", "100x30"},
		env:   []string{"XDG_CACHE_HOME=" + cache},
		files: map[string]string{"plan.md": "# Plan\n\nFirst version.\n"},
		script: []string{
			"wait focused.line=$",
			"present plan.md",
			"wait tab.1.panel.open=true tab.1.panel.docs.0.loaded=true",
			"present-browser",
			"wait run.opened.#=1",
			"snap opened",
			"type printf '# Plan\\\\n\\\\nSecond version.\\\\n' > plan.md\\r",
			"wait tab.1.panel.docs.0.reloads=1",
			"sleep 500ms",
			"quit",
		},
	})
	page := strings.TrimPrefix(g.Opened[0], "file://")
	if !strings.HasPrefix(page, filepath.Join(cache, present.CacheDir)+"/") {
		t.Fatalf("opened %s, not a copy in the cache", g.Opened[0])
	}
	if s := readFile(t, page); !strings.Contains(s, "Second version.") || !strings.Contains(s, "bunk-live.js") {
		t.Errorf("the copy was not written again:\n%s", s)
	}
	if v := readFile(t, filepath.Join(filepath.Dir(page), "version.js")); !strings.HasPrefix(v, "bunkVersion(") {
		t.Errorf("version.js: %q", v)
	}
}

// TestGUIPresentChanges: an edit of an HTML page marks the heading of the
// section it changed, counts it, and lists what changed; the drawn chart
// and the kit's controls do not count. A Markdown edit lists its changed
// section too.
func TestGUIPresentChanges(t *testing.T) {
	t.Parallel()
	page := `<!doctype html><html><head><title>C</title><link rel="stylesheet" href="bunk-present://assets/bunk-doc.css">` +
		`<script src="bunk-present://assets/bunk-doc.js" defer></script></head><body><article id="bunk-doc">` +
		`<h1 id="c">C</h1><h2 id="one">One</h2><p>Sessions live in Redis.</p>` +
		`<pre class="bunk-chart">{"xAxis": {"data": ["a"]}, "yAxis": {}, "series": [{"type": "bar", "data": [1]}]}</pre>` +
		`<h2 id="two">Two</h2><p>Tokens expire in an hour.</p><pre><code>make</code></pre></article></body></html>`
	g := runGUI(t, scenario{
		cost:  2,
		args:  []string{"--size", "100x30"},
		files: map[string]string{"c.html": page, "m.md": "# M\n\n## Alpha\n\nFirst.\n\n## Beta\n\nSecond.\n"},
		script: []string{
			"wait focused.line=$",
			"present c.html",
			"wait tab.1.panel.open=true tab.1.panel.docs.0.loaded=true",
			"present-wait document.querySelector('.bunk-chart-box svg')",
			"type sed -i 's/an hour/a day/' c.html\\r",
			"wait tab.1.panel.docs.0.reloads=1 tab.1.panel.docs.0.changes.#=1",
			"present-wait document.querySelectorAll('[data-bunk-changed]').length === 1 && document.getElementById('two').hasAttribute('data-bunk-changed')",
			"snap html",
			"present m.md",
			"wait tab.1.panel.current=1 tab.1.panel.docs.1.loaded=true",
			"type sed -i 's/Second./Second, edited./' m.md\\r",
			"wait tab.1.panel.docs.1.reloads=1 tab.1.panel.docs.1.changes.#=1",
			"snap md",
			"quit",
		},
	})
	html := g.snap("html").Tabs[0].Panel
	if d := html.Docs[0]; len(d.Changes) != 1 || d.Changes[0] != "changed Two" || d.Changed != 1 || !strings.Contains(html.Status, "1 section changed") {
		t.Errorf("HTML: changes %q, changed %d, status %q", d.Changes, d.Changed, html.Status)
	}
	if d := g.snap("md").Tabs[0].Panel.Docs[1]; len(d.Changes) != 1 || d.Changes[0] != "changed Beta" {
		t.Errorf("Markdown: changes %q", d.Changes)
	}
}

// TestGUIPresentFocus: the zoom key gives the panel the whole tab while
// the panel has the keyboard, and gives the panes back at the panel's old
// width; the panes keep their size meanwhile. A question pasted into a
// pane ends it, so the pane shows.
func TestGUIPresentFocus(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		cost:  2,
		args:  []string{"--size", "100x30"},
		files: map[string]string{"plan.md": "# Plan\n\nKeep sessions in Redis.\n"},
		script: []string{
			"maximize",
			"wait window.maximized=true focused.line=$ focused.settled=true",
			"present plan.md",
			"wait tab.1.panel.open=true tab.1.panel.docs.0.loaded=true",
			"wait idle 300ms",
			"snap before",
			"ptr-at doc h1",
			"ptr-click",
			"wait keyboard_focus=panel",
			"kbd F12",
			"wait tab.1.panel.focus_mode=true pane.0.visible=false",
			"wait idle 300ms",
			"snap focused",
			"kbd F12",
			"wait tab.1.panel.focus_mode=false pane.0.visible=true",
			"wait idle 300ms",
			"snap after",
			"present-focus",
			"wait tab.1.panel.focus_mode=true",
			"present-select Keep sessions in Redis",
			"present-ask",
			"wait tab.1.panel.ask!=null",
			"kbd Return",
			"wait tab.1.panel.focus_mode=false pane.0.visible=true keyboard_focus=\"pane 0\"",
			"quit",
		},
	})
	before, focused, after := g.snap("before"), g.snap("focused"), g.snap("after")
	pb, pf, pa := before.Tabs[0].Panel.Rect, focused.Tabs[0].Panel.Rect, after.Tabs[0].Panel.Rect
	if pb == nil || pf == nil || pa == nil {
		t.Fatal("the panel has no rectangle")
	}
	if tab := float64(focused.Window.W) - focused.Chrome.W; pf.W < tab-2 {
		t.Errorf("focused: the panel is %v wide in a tab %v wide", pf.W, tab)
	}
	if math.Abs(pa.W-pb.W) > 2 {
		t.Errorf("the panel came back %v wide, was %v", pa.W, pb.W)
	}
	if c0, c1 := before.pane(t, 0).Cols, focused.pane(t, 0).Cols; c0 != c1 {
		t.Errorf("the hidden pane went from %d to %d columns", c0, c1)
	}
}
