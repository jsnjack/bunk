package main

// Helper programs that window tests run inside panes: the test binary
// itself, started with BUNK_GUI_HELPER set (see TestMain).

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/creack/pty"

	"bunk/internal/ptychild"
)

// runHelper runs the helper BUNK_GUI_HELPER names and exits, or returns.
// With ptychild.Env set it runs the child that floods queries and never
// reads its input (bunk/internal/ptychild).
func runHelper() {
	if ptychild.Requested() {
		os.Exit(ptychild.Main())
	}
	switch os.Getenv("BUNK_GUI_HELPER") {
	case "":
		return
	case "pixels":
		os.Exit(pixelsHelper())
	default:
		fmt.Println("unknown helper", os.Getenv("BUNK_GUI_HELPER"))
		os.Exit(2)
	}
}

// pixelsHelper prints what an image-drawing program would learn about its
// terminal: the window size from TIOCGWINSZ (cells and pixels) and the
// CSI 16 t and 14 t replies. It prints again on SIGWINCH and on "r", and
// quits on "q".
func pixelsHelper() int {
	stty := exec.Command("stty", "raw", "-echo")
	stty.Stdin = os.Stdin
	if err := stty.Run(); err != nil {
		fmt.Println("stty:", err)
		return 1
	}
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	keys, replies := make(chan byte, 16), make(chan string, 16)
	go func() {
		in := bufio.NewReader(os.Stdin)
		for {
			b, err := in.ReadByte()
			if err != nil {
				close(keys)
				return
			}
			if b != 0x1b {
				keys <- b
				continue
			}
			// A reply: ESC [ params t.
			var params []byte
			for {
				c, err := in.ReadByte()
				if err != nil {
					close(keys)
					return
				}
				if c == 't' {
					break
				}
				if c != '[' {
					params = append(params, c)
				}
			}
			replies <- string(params)
		}
	}()
	reply := func() string {
		select {
		case r := <-replies:
			return r
		case <-time.After(2 * time.Second):
			return "none"
		}
	}
	report := func(why string) {
		ws, err := pty.GetsizeFull(os.Stdin)
		if err != nil {
			fmt.Printf("%s: winsize error %v\r\n", why, err)
			return
		}
		fmt.Print("\x1b[16t\x1b[14t")
		r16, r14 := reply(), reply()
		fmt.Printf("%s: winsize %dx%d pixels %dx%d csi16 %s csi14 %s\r\n", why, ws.Cols, ws.Rows, ws.X, ws.Y, r16, r14)
	}
	report("start")
	for {
		select {
		case <-winch:
			// Resizes come in bursts: report once things settle.
			time.Sleep(100 * time.Millisecond)
			for len(winch) > 0 {
				<-winch
			}
			report("winch")
		case k, ok := <-keys:
			if !ok || k == 'q' {
				return 0
			}
			if k == 'r' {
				report("key")
			}
		}
	}
}
