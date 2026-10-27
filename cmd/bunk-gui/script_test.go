package main

import (
	"os"
	"strings"
	"testing"
)

// TestGUIScript runs an ad-hoc scenario for development and prints what
// bunk-gui printed; it is skipped unless BUNK_GUI_SCRIPT is set:
//
//	BUNK_GUI_SCRIPT='500ms kbd f2; 900ms print-tabs; 1s quit' \
//	BUNK_GUI_ARGS='--size 80x24' go test -run TestGUIScript -v .
func TestGUIScript(t *testing.T) {
	script := os.Getenv("BUNK_GUI_SCRIPT")
	if script == "" {
		t.Skip("BUNK_GUI_SCRIPT unset")
	}
	g := runGUI(t, scenario{args: strings.Fields(os.Getenv("BUNK_GUI_ARGS")), script: strings.Split(script, ";")})
	g.logOutput()
}
