package present

import (
	"bytes"
	"fmt"
	"html/template"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// DocumentsURL is the address of the Documents page in bunk-gui's panel:
// the documents the panels showed, to show one again. A link to
// DocumentsOpen with the path in the query shows that document.
const (
	DocumentsURL  = "bunk-present://documents/"
	DocumentsOpen = "open"
)

// assetDocuments is the Documents page's script: its box filters the list.
const assetDocuments = "bunk-documents.js"

// DocEntry is a document the Documents page lists.
type DocEntry struct {
	Path  string
	Where string // the repository or folder it belongs to; "" when unknown
	Agent string // the agent that presented it; "" when a person opened it
	Shown time.Time
}

// docGroup is the documents of one repository or folder.
type docGroup struct {
	Name, Where string
	Here        bool
	Docs        []docItem
}

type docItem struct {
	Name, Dir, Agent string
	Link             template.URL // bunk-present://, which html/template would refuse
	When, Stamp      string
}

var documentsPage = template.Must(template.New("documents").Parse(`<!doctype html>
<html lang="en">
<head>
<meta http-equiv="Content-Security-Policy" content="{{.CSP}}">
<meta charset="utf-8">
<title>Documents</title>
<link rel="stylesheet" href="{{.Style}}">
<style>
{{.ThemeCSS}}
.bunk-docs-filter { width: 100%; box-sizing: border-box; margin: 0 0 8px; }
.bunk-docs-group h2 { font-size: 1.05em; margin: 1.2em 0 0.3em; }
.bunk-docs-group h2 .bunk-badge { margin-left: 6px; vertical-align: 1px; }
.bunk-docs-group ul { list-style: none; margin: 0; padding: 0; }
.bunk-docs-group li { display: flex; align-items: baseline; gap: 10px; padding: 3px 0; border-bottom: 1px solid var(--bunk-border); }
.bunk-docs-group li a { flex: none; max-width: 60%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.bunk-docs-dir { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 0.85em; color: var(--bunk-muted); }
.bunk-docs-group time, .bunk-docs-agent { flex: none; font-size: 0.85em; color: var(--bunk-muted); }
</style>
</head>
<body>
<article id="bunk-doc" class="bunk-documents">
<h1>Documents</h1>
{{if .Groups}}<p>The documents bunk-gui showed, by repository, newest first. Click one to show it in this tab.</p>
<input type="search" id="bunk-docs-filter" class="bunk-filter-box bunk-docs-filter" placeholder="Filter documents" aria-label="Filter the documents">
<p id="bunk-docs-none" hidden>No documents match.</p>
{{range .Groups}}<section class="bunk-docs-group">
<h2 title="{{.Where}}">{{.Name}}{{if .Here}} <span class="bunk-badge info">this tab</span>{{end}}</h2>
<ul>
{{range .Docs}}<li><a href="{{.Link}}" title="{{.Dir}}">{{.Name}}</a><span class="bunk-docs-dir">{{.Dir}}</span>{{if .Agent}}<span class="bunk-docs-agent">{{.Agent}}</span>{{end}}<time datetime="{{.Stamp}}">{{.When}}</time></li>
{{end}}</ul>
</section>
{{end}}{{else}}<p>No documents yet. The documents agents present, and the files you open in the panel, are listed here, so you can show them again after you close their tab or bunk-gui.</p>
{{end}}</article>
<script src="{{.Script}}" defer></script>
</body>
</html>
`))

// RenderDocuments renders the Documents page: docs grouped by where they
// belong, here's group first and the others by their newest document,
// each group newest first. now dates the times; home shortens paths.
func RenderDocuments(docs []DocEntry, here, home string, now time.Time, opts Options) ([]byte, error) {
	var groups []*docGroup
	byWhere := map[string]*docGroup{}
	for _, d := range docs {
		g := byWhere[d.Where]
		if g == nil {
			name := filepath.Base(d.Where)
			if d.Where == "" {
				name = "Other"
			}
			g = &docGroup{Name: name, Where: tilde(d.Where, home), Here: d.Where != "" && d.Where == here}
			byWhere[d.Where] = g
			groups = append(groups, g)
		}
		dir := filepath.Dir(d.Path)
		if rel, err := filepath.Rel(d.Where, dir); d.Where != "" && err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
			dir = rel
		} else {
			dir = tilde(dir, home)
		}
		g.Docs = append(g.Docs, docItem{
			Name:  filepath.Base(d.Path),
			Dir:   dir,
			Agent: d.Agent,
			Link:  template.URL(DocumentsURL + DocumentsOpen + "?path=" + strings.ReplaceAll(url.QueryEscape(d.Path), "+", "%20")),
			When:  shownLabel(d.Shown, now),
			Stamp: d.Shown.Format(time.RFC3339),
		})
	}
	// docs come newest first, so each group is, and groups are in the
	// order of their newest document; here's goes first.
	slices.SortStableFunc(groups, func(a, b *docGroup) int {
		switch {
		case a.Here == b.Here:
			return 0
		case a.Here:
			return -1
		}
		return 1
	})
	asset := func(n string) template.URL { return template.URL(opts.AssetBase + n) }
	var b bytes.Buffer
	err := documentsPage.Execute(&b, map[string]any{
		"CSP":      contentSecurityPolicy(opts.AssetBase, false),
		"Style":    asset(assetStyle),
		"Script":   asset(assetDocuments),
		"ThemeCSS": template.CSS(pageCSS(opts.Theme, opts.System, false)),
		"Groups":   groups,
	})
	if err != nil {
		return nil, fmt.Errorf("present: documents page: %w", err)
	}
	return b.Bytes(), nil
}

// shownLabel says when a document was shown, as briefly as now allows:
// the time today, then "yesterday", the weekday within a week, the date.
func shownLabel(t, now time.Time) string {
	t = t.In(now.Location())
	day := func(x time.Time) time.Time { return time.Date(x.Year(), x.Month(), x.Day(), 0, 0, 0, 0, x.Location()) }
	days := int(day(now).Sub(day(t)).Hours()/24 + 0.5)
	switch {
	case days <= 0:
		return t.Format("15:04")
	case days == 1:
		return "yesterday"
	case days < 7:
		return t.Format("Monday")
	case t.Year() == now.Year():
		return t.Format("Jan 2")
	}
	return t.Format("Jan 2, 2006")
}

// tilde writes path under home as ~/…; "" stays "".
func tilde(path, home string) string {
	if home != "" && path != "" {
		if path == home {
			return "~"
		}
		if rel, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
			return "~/" + rel
		}
	}
	return path
}
