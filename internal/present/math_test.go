package present

import (
	"strings"
	"testing"
)

func TestMath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, src string
		want      []string
	}{
		{
			name: "inline",
			src:  "Energy $E = mc^2$ here.\n",
			want: []string{`Energy <span class="math math-inline">E = mc^2</span> here.`},
		},
		{
			name: "underscores and stars stay TeX",
			src:  "$a_1 * b_2 * c_3$ and _em_\n",
			want: []string{`<span class="math math-inline">a_1 * b_2 * c_3</span> and <em>em</em>`},
		},
		{
			name: "escaped dollar inside",
			src:  `$\text{\$} x$` + "\n",
			want: []string{`<span class="math math-inline">\text{\$} x</span>`},
		},
		{
			name: "html in TeX is escaped",
			src:  "$a<b>c$\n",
			want: []string{`<span class="math math-inline">a&lt;b&gt;c</span>`},
		},
		{
			name: "inline across lines",
			src:  "Sum $a +\nb$ done.\n",
			want: []string{"<span class=\"math math-inline\">a +\nb</span>"},
		},
		{
			name: "inline display",
			src:  "So $$\\frac{a}{b}$$ holds.\n",
			want: []string{`So <span class="math math-display">\frac{a}{b}</span> holds.`},
		},
		{
			name: "display closing a paragraph",
			src:  "Here: $$\nx^2\n$$\n\nNext.\n",
			want: []string{"<p>Here: <span class=\"math math-display\">\nx^2\n</span></p>", "<p>Next.</p>"},
		},
		{
			name: "display block",
			src:  "$$\n\\int_0^1 f(x)\\,dx\n$$\n\nAfter _em_.\n",
			want: []string{"<div class=\"math math-display\">\\int_0^1 f(x)\\,dx\n</div>", "<p>After <em>em</em>.</p>"},
		},
		{
			name: "display block on one line",
			src:  "$$E=mc^2$$\n",
			want: []string{`<div class="math math-display">E=mc^2</div>`},
		},
		{
			name: "display block closed on its last line",
			src:  "$$\na = b\nc = d $$\n",
			want: []string{"<div class=\"math math-display\">a = b\nc = d </div>"},
		},
		{
			name: "display block in a list",
			src:  "- item\n\n  $$\n  x_1\n  $$\n",
			want: []string{"<div class=\"math math-display\">x_1\n</div>\n</li>"},
		},
		{
			name: "math fence",
			src:  "```math\na < b\n```\n",
			want: []string{"<div class=\"math math-display\">a &lt; b\n</div>"},
		},
		{
			name: "math after prices",
			src:  "Price $5 and $6. Math $a_1$.\n",
			want: []string{`Price $5 and $6. Math <span class="math math-inline">a_1</span>.`},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := renderBody(t, tc.src)
			for _, w := range tc.want {
				if !strings.Contains(body, w) {
					t.Errorf("body lacks %q:\n%s", w, body)
				}
			}
		})
	}
}

func TestCurrencyIsNotMath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, src string
	}{
		{"two prices", "It costs $5 and $6.\n"},
		{"prices with separators", "From $20,000 to $30,000.\n"},
		{"price then digit", "Pay $5 or 6$5 more.\n"},
		{"space after opening", "a $ x$ b\n"},
		{"space before closing", "a $x $ b\n"},
		{"amount between dollars", "both $5$ and $1.50$\n"},
		{"escaped dollars", `\$x\$` + "\n"},
		{"lone dollar", "US$ only\n"},
		{"prices across lines", "Was $5\nnow $4 only.\n"},
		{"empty display", "a $$ $$ b\n"},
		{"three dollars", "$$$x$$$\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if body := renderBody(t, tc.src); strings.Contains(body, "math") {
				t.Errorf("%q became math:\n%s", tc.src, body)
			}
		})
	}
}
