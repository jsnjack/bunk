package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// symbolicIcon matches an icon name in bunk-gui's source.
var symbolicIcon = regexp.MustCompile(`"([a-z0-9-]+-symbolic)"`)

// TestIconNames: every symbolic icon bunk-gui asks for is in the Adwaita
// or hicolor theme, which GTK 4 depends on. GTK draws a name that no theme
// has as a broken image.
func TestIconNames(t *testing.T) {
	t.Parallel()
	dirs := os.Getenv("XDG_DATA_DIRS")
	if dirs == "" {
		dirs = "/usr/local/share:/usr/share"
	}
	have := map[string]bool{}
	for _, dir := range filepath.SplitList(dirs) {
		for _, theme := range []string{"Adwaita", "hicolor"} {
			err := filepath.WalkDir(filepath.Join(dir, "icons", theme), func(_ string, d fs.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					have[strings.TrimSuffix(d.Name(), filepath.Ext(d.Name()))] = true
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if !have["window-close-symbolic"] {
		t.Skip("no Adwaita icon theme in " + dirs)
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range symbolicIcon.FindAllSubmatch(src, -1) {
			found++
			if !have[string(m[1])] {
				t.Errorf("%s: no icon %s in Adwaita or hicolor", f, m[1])
			}
		}
	}
	if found == 0 {
		t.Error("found no icon names in the source")
	}
}
