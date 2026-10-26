package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMakeOrdersTestsBeforeBuild runs make with stand-ins for go and the
// other tools that log each step, and checks that every binary is built
// only after its tests pass: bunk after the core tests, bunk-gui after the
// window tests and packaging checks.
func TestMakeOrdersTestsBeforeBuild(t *testing.T) {
	makefile, err := filepath.Abs("Makefile")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, target, fail string // fail: "core" or "gui" tests fail
		want               string
	}{
		{"check", "check", "", "fmt vet test build build build build lint"},
		{"check: failing tests stop the build", "check", "core", "fmt vet test"},
		{"build", "build", "", "test build build build build vet test validate validate build"},
		{"build: failing core tests stop every build", "build", "core", "test"},
		{"build: failing window tests stop bunk-gui", "build", "gui", "test build build build build vet test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			logPath := filepath.Join(dir, "steps")
			script := `#!/bin/sh
if [ "$1" = env ]; then printf '%s\n' "$BUNK_TEST_TOOLS"; exit; fi
case "$0" in
  */goimports|*/gofmt) step=fmt ;;
  */golangci-lint) step=lint ;;
  */desktop-file-validate|*/appstreamcli) step=validate ;;
  *) step="$1" ;;
esac
printf '%s\n' "$step" >> "$BUNK_TEST_LOG"
if [ "$step" = test ]; then
  case "$BUNK_TEST_FAIL:$PWD" in
    core:*/cmd/bunk-gui) ;;
    core:*) exit 1 ;;
    gui:*/cmd/bunk-gui) exit 1 ;;
  esac
fi
if [ "$step" = build ]; then
  while [ "$#" -gt 0 ]; do
    if [ "$1" = -o ]; then shift; mkdir -p "$(dirname "$1")"; touch "$1"; break; fi
    shift
  done
fi
`
			// fmt formats every Go file with gofmt, so give it one; gui-test
			// runs in cmd/bunk-gui through scripts/headless-gui.sh.
			files := map[string]string{
				"x.go":                    "package x\n",
				"cmd/bunk-gui/.keep":      "",
				"scripts/headless-gui.sh": "#!/bin/sh\nexec \"$@\"\n",
			}
			for name, body := range files {
				path := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			for _, tool := range []string{"go", "gofmt", "goimports", "golangci-lint", "desktop-file-validate", "appstreamcli"} {
				if err := os.WriteFile(filepath.Join(dir, tool), []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("make", "--no-print-directory", "-j8", "-f", makefile, tc.target, "MONOVA=")
			cmd.Dir = dir
			// An outer "make check-all FRESH=1" passes FRESH on through MAKEFLAGS
			// and the environment.
			cmd.Env = append(os.Environ(), "MAKEFLAGS=", "MFLAGS=", "MAKELEVEL=", "FRESH=",
				"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"BUNK_TEST_TOOLS="+dir, "BUNK_TEST_LOG="+logPath, "BUNK_TEST_FAIL="+tc.fail)
			output, err := cmd.CombinedOutput()
			if (err != nil) != (tc.fail != "") {
				t.Fatalf("make %s: %v\n%s", tc.target, err, output)
			}
			data, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if steps := strings.Join(strings.Fields(string(data)), " "); steps != tc.want {
				t.Fatalf("steps = %s, want %s\n%s", steps, tc.want, output)
			}
		})
	}
}
