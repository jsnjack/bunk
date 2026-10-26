package badges

import (
	"testing"

	"github.com/rivo/uniseg"
)

func TestFitCells(t *testing.T) {
	for _, tc := range []struct {
		name  string
		s     string
		width int
		want  string
	}{
		{"fits", "hello", 5, "hello"},
		{"cut", "hello world", 8, "hello w…"},
		{"cut before a space", "hello world", 7, "hello…"},
		{"wide", "完了しました", 9, "完了しま…"},
		{"wide that does not split", "完了しました", 8, "完了し…"},
		{"emoji cluster", "👍🏽 ok then", 6, "👍🏽 ok…"},
		{"one cell", "hello", 1, "…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := fitCells(tc.s, tc.width)
			if got != tc.want {
				t.Fatalf("fitCells(%q, %d) = %q, want %q", tc.s, tc.width, got, tc.want)
			}
			if w := uniseg.StringWidth(got); w > tc.width {
				t.Fatalf("fitCells(%q, %d) = %q: %d cells", tc.s, tc.width, got, w)
			}
		})
	}
}
