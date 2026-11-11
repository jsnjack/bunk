package main

import (
	"image"
	"strings"
	"testing"
)

// demoReport is the report an agent presents in the documents shots: the
// kit's badges, callout, tabs, reply buttons and sortable table, an
// ECharts chart and a Graphviz graph, in the theme's colours.
const demoReport = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Rate limiter: load test</title>
<link rel="stylesheet" href="bunk-present://assets/bunk-doc.css">
<script src="bunk-present://assets/bunk-doc.js" defer></script>
</head><body><article id="bunk-doc">
<h1 id="load-test">Rate limiter: load test</h1>
<p><span class="bunk-badge good">p99 within budget</span> <span class="bunk-badge warn">1 flaky test</span> <span class="bunk-badge info">feat/rate-limit</span></p>
<div class="bunk-callout tip"><p class="bunk-callout-title">Result</p>
<p>The token bucket holds p99 at 41 ms up to 2,000 requests per second, under the 50 ms budget. Past 2,500 it sheds load with 429s.</p></div>
<h2 id="latency">Latency under load</h2>
<pre class="bunk-chart" data-height="250">{"tooltip": {"trigger": "axis"}, "legend": {"top": 0},
 "grid": {"left": 44, "right": 20, "top": 34, "bottom": 30},
 "xAxis": {"type": "category", "name": "req/s", "data": ["250", "500", "1000", "1500", "2000", "2500"]},
 "yAxis": {"type": "value", "name": "ms"},
 "series": [{"name": "p50", "type": "line", "smooth": true, "data": [4, 5, 7, 9, 12, 19]},
  {"name": "p99", "type": "line", "smooth": true, "data": [11, 14, 22, 31, 41, 78],
   "markLine": {"silent": true, "symbol": "none", "lineStyle": {"type": "dashed"}, "label": {"formatter": "budget"}, "data": [{"yAxis": 50}]}}]}</pre>
<h2 id="design">Where the limiter sits</h2>
<pre class="bunk-graphviz">digraph { rankdir=LR; node [shape=box, style=rounded];
  Client -> Gateway -> "Rate limiter" -> API; "Rate limiter" -> Redis [style=dashed, label=" tokens"]; }</pre>
<h2 id="buckets">Where to keep the buckets</h2>
<div class="bunk-tabs">
<section data-title="Redis"><p>Shared by every API instance, so a client cannot spread its requests to get more. Adds 0.4 ms per request.</p></section>
<section data-title="In memory"><p>No extra hop, but each of the 6 instances counts on its own.</p></section>
</div>
<p><button data-bunk-reply="Go with Redis for the buckets">Go with Redis</button> <button data-bunk-reply="Keep the buckets in memory">Keep them in memory</button></p>
<h2 id="runs">Runs</h2>
<table class="bunk-sort"><thead><tr><th>Run</th><th>req/s</th><th>p99</th><th>429s</th></tr></thead><tbody>
<tr><td>baseline</td><td>2,000</td><td>63 ms</td><td>0</td></tr>
<tr><td>bucket 100/s</td><td>2,000</td><td>41 ms</td><td>0.2%</td></tr>
<tr><td>bucket 100/s</td><td>2,500</td><td>78 ms</td><td>4.1%</td></tr>
</tbody></table>
</article></body></html>`

// documentsScreenshots: an agent's report in the panel, then a review in
// progress: two comments, their passages highlighted, counted on the
// Review button.
func documentsScreenshots(t *testing.T) {
	g := runGUI(t, scenario{
		cost:   5,
		config: "tabs = \"rail\"\nrail_open = false\nfont = \"Noto Sans Mono\"\nfont_size = 11\n",
		args:   []string{"--size", "100x28"},
		// In docs/: the shots land in the scenario's directory, and an HTML
		// page reloads for images beside it.
		files: map[string]string{"docs/rate-limit-report.html": demoReport},
		script: []string{
			"maximize",
			"wait window.maximized=true focused.line=$ focused.settled=true",
			"present docs/rate-limit-report.html check",
			"wait tab.1.panel.open=true tab.1.panel.docs.0.loaded=true tab.1.panel.checked!=null",
			"present-wait document.querySelector('.bunk-chart-box svg') && document.querySelector('pre.bunk-graphviz svg')",
			"wait idle 500ms",
			"snap report",
			"shot report-window.png",
			"present-select under the 50 ms budget",
			"present-ask",
			"wait tab.1.panel.ask!=null",
			"present-review-add Is 50 ms the budget for every route?",
			"wait tab.1.panel.review.#=1",
			"present-select so a client cannot spread its requests to get more",
			"present-ask",
			"wait tab.1.panel.ask!=null",
			"present-review-add Redis, then: we already run it for sessions.",
			"wait tab.1.panel.review.#=2",
			"present-wait CSS.highlights.get('bunk-review') && CSS.highlights.get('bunk-review').size === 2 && window.getSelection().removeAllRanges() === undefined",
			// The "Added to the review" toast leaves after 3 s.
			"sleep 3500ms",
			"wait idle 500ms",
			"snap review",
			"shot review-window.png",
			"quit",
		},
	})
	report := g.snap("report").Tabs[0].Panel
	if report == nil || report.Rect == nil || report.Checked == nil || len(*report.Checked) != 0 {
		t.Fatalf("report: panel %+v", report)
	}
	review := g.snap("review").Tabs[0].Panel
	if len(review.Review) != 2 || review.Review[0].Comment != "Is 50 ms the budget for every route?" || !strings.HasSuffix(review.Review[1].Doc, "rate-limit-report.html") {
		t.Errorf("review: comments %+v", review.Review)
	}
	// The report without the status line, which names the test's
	// temporary directory; the review as a strip with the Review button
	// and the first highlighted passage.
	r := report.Rect
	scale := float64(r.DH) / r.H
	cropShot(t, g, "report-window.png", "gui-report.png", image.Rect(r.DX, r.DY, r.DX+r.DW, r.DY+r.DH-int(28*scale)))
	r = review.Rect
	cropShot(t, g, "review-window.png", "gui-review.png", image.Rect(r.DX, r.DY, r.DX+r.DW, r.DY+int(270*scale)))
	saveShots(t, g, "gui-report.png", "gui-review.png")
}
