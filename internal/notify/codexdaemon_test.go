package notify

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCodexDaemonOff: the setting goes on the line after the [features]
// header, whatever its spacing, comment or line ending, else into a
// [features] table appended at the end. A setting of the user's, either
// way, stays; features made of dotted keys, an inline table or sub-tables
// alone, and text that is not TOML, are refused.
func TestCodexDaemonOff(t *testing.T) {
	t.Parallel()
	const off = "daemon_auto_start = false\n"
	for _, tc := range []struct {
		name, in, want string
		err            bool
	}{
		{name: "empty", in: "", want: "[features]\n" + off},
		{name: "no features", in: "model = \"o3\"\n\n[tui]\ntheme = \"x\"\n", want: "model = \"o3\"\n\n[tui]\ntheme = \"x\"\n\n[features]\n" + off},
		{name: "no final newline", in: "model = \"o3\"", want: "model = \"o3\"\n\n[features]\n" + off},
		{name: "features table", in: "[features]\nfast_mode = true\n\n[tui]\n", want: "[features]\n" + off + "fast_mode = true\n\n[tui]\n"},
		{name: "spaced header with a comment", in: "  [ features ] # mine\r\nfast_mode = true\r\n",
			want: "  [ features ] # mine\r\n" + off + "fast_mode = true\r\n"},
		{name: "header at the end", in: "model = \"o3\"\n[features]", want: "model = \"o3\"\n[features]\n" + off},
		{name: "sub-table only", in: "[features.sub]\nx = 1\n", err: true},
		{name: "header in a string", in: "x = \"\"\"\n[features]\n\"\"\"\n", want: "x = \"\"\"\n[features]\n\"\"\"\n\n[features]\n" + off},
		{name: "already off", in: "[features]\ndaemon_auto_start = false\n", want: "[features]\ndaemon_auto_start = false\n"},
		{name: "left on by the user", in: "[features]\ndaemon_auto_start = true\n", want: "[features]\ndaemon_auto_start = true\n"},
		{name: "dotted keys", in: "features.fast_mode = true\n", err: true},
		{name: "inline table", in: "features = { fast_mode = true }\n", err: true},
		{name: "not TOML", in: "[hooks", err: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := codexDaemonOff([]byte(tc.in))
			if tc.err {
				if err == nil {
					t.Errorf("got %q, want an error", got)
				}
				return
			}
			if err != nil || string(got) != tc.want {
				t.Errorf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

// TestInstallCodexSetting: installing Codex's hooks adds the setting to a
// config.toml of the user's, keeping its text, its mode and a backup, and
// a second install changes nothing. A setting the user made stays, a
// symbolic link stays one, and a config.toml that cannot take the
// setting stops the install before hooks.json is written. "notify hooks"
// names the missing setting.
func TestInstallCodexSetting(t *testing.T) {
	t.Parallel()
	const mine = "model = \"o3\"\n\n[projects.\"/home/u/src\"]\ntrust_level = \"trusted\"\n" + codexState
	setup := func(t *testing.T, config string) (Dirs, string) {
		t.Helper()
		home := t.TempDir()
		path := filepath.Join(home, ".codex", "config.toml")
		writeFile(t, path, config)
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		return Dirs{Home: home}, path
	}

	t.Run("beside the user's settings", func(t *testing.T) {
		t.Parallel()
		d, path := setup(t, mine)
		var out bytes.Buffer
		runCLI("bunk", []string{"hooks"}, []string{"HOME=" + d.Home}, nil, &out, &out)
		if !strings.Contains(out.String(), "lacks daemon_auto_start = false under [features] in ~/.codex/config.toml\n") {
			t.Errorf("hooks listing:\n%s", out.String())
		}
		done, err := InstallHooks(d, "codex")
		if err != nil || done.Setting != path || done.SettingBackup != path+BackupSuffix {
			t.Fatalf("%+v, %v", done, err)
		}
		if data, _ := os.ReadFile(path); string(data) != mine+codexOff {
			t.Errorf("config.toml:\n%s", data)
		}
		if data, _ := os.ReadFile(done.SettingBackup); string(data) != mine {
			t.Errorf("backup:\n%s", data)
		}
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("mode %v, %v; want the file's own 0600", fi.Mode(), err)
		}
		if h, _ := CheckHook(d, "codex"); h.State != HooksInstalled {
			t.Errorf("after install: %+v", h)
		}
		if again, err := InstallHooks(d, "codex"); err != nil || again.Setting != "" {
			t.Errorf("a second install: %+v, %v", again, err)
		}
	})

	t.Run("left on by the user", func(t *testing.T) {
		t.Parallel()
		on := mine + "\n[features]\ndaemon_auto_start = true\n"
		d, path := setup(t, on)
		if done, err := InstallHooks(d, "codex"); err != nil || done.Setting != "" {
			t.Errorf("%+v, %v", done, err)
		}
		if data, _ := os.ReadFile(path); string(data) != on {
			t.Errorf("config.toml changed:\n%s", data)
		}
	})

	t.Run("symbolic link", func(t *testing.T) {
		t.Parallel()
		d, link := setup(t, "")
		target := filepath.Join(t.TempDir(), "config.toml")
		writeFile(t, target, mine)
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		done, err := InstallHooks(d, "codex")
		if err != nil || done.SettingBackup != target+BackupSuffix {
			t.Fatalf("%+v, %v", done, err)
		}
		if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("the link was replaced: %v", err)
		}
		if data, _ := os.ReadFile(target); string(data) != mine+codexOff {
			t.Errorf("target:\n%s", data)
		}
	})

	t.Run("inline features", func(t *testing.T) {
		t.Parallel()
		inline := "features = { fast_mode = true }\n"
		d, path := setup(t, inline)
		if _, err := InstallHooks(d, "codex"); !errors.Is(err, errFeatures) || !strings.Contains(err.Error(), "~/.codex/config.toml stays as it is") {
			t.Errorf("err %v", err)
		}
		if data, _ := os.ReadFile(path); string(data) != inline {
			t.Errorf("config.toml changed:\n%s", data)
		}
		if _, err := os.Stat(filepath.Join(d.Home, ".codex", "hooks.json")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("hooks.json was written: %v", err)
		}
	})
}

// TestCodexDaemonAbove: a command whose parent, or one of theirs, is
// codex app-server --managed-daemon runs under Codex's shared daemon; one
// under the Codex TUI or an app server Codex did not start as its daemon
// does not. A command name with spaces and parentheses does not confuse
// the parent's pid, and an unreadable process ends the walk.
func TestCodexDaemonAbove(t *testing.T) {
	t.Parallel()
	proc := t.TempDir()
	add := func(pid, ppid, comm string, argv ...string) {
		writeFile(t, filepath.Join(proc, pid, "cmdline"), strings.Join(argv, "\x00")+"\x00")
		writeFile(t, filepath.Join(proc, pid, "stat"), pid+" ("+comm+") S "+ppid+" 1 1 0 -1\n")
	}
	add("50", "1", "codex", "/home/u/.codex/packages/app-server-daemon/current/bin/codex", "app-server", "--listen", "unix://", "--managed-daemon")
	add("60", "50", "codex-code-mo", "codex-code-mode-host", "--listen", "grpc+stdio://")
	add("70", "60", "a (b) c", "bash", "-c", "bunk-gui present plan.html")
	add("80", "1", "codex", "codex")
	add("90", "80", "bash", "bash", "-c", "x")
	add("100", "1", "codex", "codex", "app-server")
	add("110", "100", "bash", "bash")
	add("120", "999", "bash", "bash")
	for pid, want := range map[int]bool{70: true, 60: true, 50: true, 90: false, 110: false, 120: false, 1: false, 999: false} {
		if got := codexDaemonAbove(proc, pid); got != want {
			t.Errorf("pid %d: %v, want %v", pid, got, want)
		}
	}
	if codexDaemonAbove(procDir, os.Getpid()) {
		t.Error("this test runs under Codex's daemon")
	}
}

// TestCodexDaemonHint: the hint names the setting and config.toml where
// Codex reads it, CODEX_HOME's when it is set.
func TestCodexDaemonHint(t *testing.T) {
	t.Parallel()
	want := "Codex runs this command in its shared daemon, which has the BUNK_NOTIFY_SESSION of the pane that started it. " +
		"Set daemon_auto_start = false under [features] in ~/.codex/config.toml (bunk-gui's Codex setup does it), " +
		"then stop the daemon with codex app-server daemon stop.\n"
	if got := codexDaemonHint(Dirs{Home: "/home/u"}); got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	moved := Dirs{Home: "/home/u", Getenv: func(k string) string {
		if k == "CODEX_HOME" {
			return "/etc/codex"
		}
		return ""
	}}
	if got := codexDaemonHint(moved); !strings.Contains(got, " in /etc/codex/config.toml ") {
		t.Errorf("with CODEX_HOME: %q", got)
	}
}
