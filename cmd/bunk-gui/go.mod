module bunk/cmd/bunk-gui

go 1.26

require (
	bunk v0.0.0-00010101000000-000000000000
	github.com/diamondburned/gotk4-adwaita/pkg v0.0.0-20260808200908-d4aecaa0ff32
	github.com/diamondburned/gotk4/pkg v0.4.1
	github.com/rivo/uniseg v0.4.7
)

require (
	github.com/BurntSushi/toml v1.6.0 // indirect
	github.com/creack/pty v1.1.24 // indirect
	golang.org/x/sync v0.0.0-20210220032951-036812b2e83c // indirect
)

replace bunk => ../..
