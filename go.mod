module bunk

go 1.26.0

// Pinned tcell with pane-safe overline and keypad input extensions.
replace github.com/gdamore/tcell/v2 => ./third_party/tcell

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/alecthomas/chroma/v2 v2.27.0
	github.com/creack/pty v1.1.24
	github.com/gdamore/tcell/v2 v2.13.9
	github.com/rivo/uniseg v0.4.7
	github.com/spf13/cobra v1.10.2
	github.com/spf13/pflag v1.1.0
	github.com/yuin/goldmark v1.8.7
	golang.org/x/sys v0.50.0
	golang.org/x/term v0.48.0
)

require (
	github.com/dlclark/regexp2/v2 v2.13.1 // indirect
	github.com/gdamore/encoding v1.0.1 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.1 // indirect
	golang.org/x/text v0.44.0 // indirect
)
