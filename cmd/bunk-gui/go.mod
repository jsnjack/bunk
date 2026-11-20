module bunk/cmd/bunk-gui

go 1.26.0

require (
	bunk v0.0.0-00010101000000-000000000000
	github.com/BurntSushi/toml v1.6.0
	github.com/creack/pty v1.1.24
	github.com/diamondburned/gotk4-adwaita/pkg v0.0.0-20261031204147-41c057c79b39
	github.com/diamondburned/gotk4/pkg v0.4.2-0.20261031093702-169e0f82bca8
	github.com/rivo/uniseg v0.4.7
	github.com/spf13/cobra v1.10.2
	github.com/spf13/pflag v1.1.0
)

require (
	github.com/alecthomas/chroma/v2 v2.27.0 // indirect
	github.com/dlclark/regexp2/v2 v2.13.1 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/yuin/goldmark v1.8.7 // indirect
	golang.org/x/sync v0.25.0 // indirect
)

replace bunk => ../..
