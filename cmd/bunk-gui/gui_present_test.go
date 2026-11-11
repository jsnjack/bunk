package main

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
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
// is listed in the panel, which stays closed until the user opens it.
func TestGUIPresentListed(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		args:  []string{"--size", "100x30"},
		files: map[string]string{"notes.md": "# Notes\n\nHello.\n"},
		script: []string{
			"wait focused.line=$",
			"present notes.md auto",
			"wait tab.1.panel.docs.#=1",
			"snap listed",
			"present-toggle",
			"wait tab.1.panel.open=true tab.1.panel.docs.0.loaded=true",
			"present-eval document.querySelector('#bunk-doc h1').textContent",
			"wait tab.1.panel.eval=\"Notes\"",
			"type rm notes.md\\r",
			"wait tab.1.panel.status~\"Could not show\"",
			"present-toggle",
			"wait tab.1.panel.open=false",
			"snap hidden",
			"quit",
		},
	})
	if pn := g.snap("listed").Tabs[0].Panel; pn == nil || pn.Open || len(pn.Docs) != 1 || !pn.Docs[0].Auto {
		t.Errorf("listed: %+v", pn)
	}
	if s := g.snap("hidden"); s.KeyboardFocus != "pane 0" {
		t.Errorf("hiding the panel left the keyboard with %q", s.KeyboardFocus)
	}
}

// TestGUIPresentPrefs: the Documents section of the preferences lists the
// bunk-present skill of Claude Code, Codex and Copilot CLI, missing in an
// empty home directory. Install writes Codex's, and the switch saves
// [present] auto in config.toml and holds without a restart.
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
			"pref-scroll skill-codex-install",
			"ptr-at pref skill-codex-install", "ptr-click",
			"wait prefs.skills.1.state=installed prefs.skills_note~Installed",
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
	if note := g.snap("installed").Prefs.SkillsNote; !strings.Contains(note, "~/.codex/skills/bunk-present/SKILL.md") {
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
