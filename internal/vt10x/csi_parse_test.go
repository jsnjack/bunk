package vt10x

import (
	"math/rand/v2"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// parseCSIWithSplit is the string-splitting parameter parser that
// csiEscape.parse replaced; the two must agree on every input.
func parseCSIWithSplit(buf []byte) (args []int, priv bool, mode byte) {
	mode = buf[len(buf)-1]
	if len(buf) == 1 {
		return nil, false, mode
	}
	s := string(buf)
	if s[0] == '?' {
		priv = true
		s = s[1:]
	}
	s = s[:len(s)-1]
	for _, p := range strings.Split(s, ";") {
		if idx := strings.IndexByte(p, ':'); idx >= 0 {
			main, err := strconv.Atoi(p[:idx])
			if err != nil {
				break
			}
			subParts := strings.Split(p[idx+1:], ":")
			sub, err := strconv.Atoi(subParts[0])
			if err != nil {
				sub = 0
			}
			args = append(args, main*10000+sub+1)
			for _, sp := range subParts[1:] {
				v, err := strconv.Atoi(sp)
				if err != nil {
					break
				}
				args = append(args, v)
			}
		} else {
			i, err := strconv.Atoi(p)
			if err != nil {
				break
			}
			args = append(args, i)
		}
	}
	return args, priv, mode
}

func TestCSIParseMatchesSplitParser(t *testing.T) {
	alphabet := []string{"0", "1", "2", "38", "5", "255", ";", ";", ":", "?", "$", " ", ">", "-", "+", "99999999999999999999"}
	rng := rand.New(rand.NewPCG(7, 0))
	for i := 0; i < 50000; i++ {
		var b strings.Builder
		for n := rng.IntN(12); n > 0; n-- {
			b.WriteString(alphabet[rng.IntN(len(alphabet))])
		}
		b.WriteByte("mHJKpqrhl"[rng.IntN(9)])
		buf := []byte(b.String())

		var c csiEscape
		c.buf = append(c.buf, buf...)
		c.parse()
		wantArgs, wantPriv, wantMode := parseCSIWithSplit(buf)
		if len(c.args) == 0 {
			c.args = nil
		}
		if !reflect.DeepEqual(c.args, wantArgs) || c.priv != wantPriv || c.mode != wantMode {
			t.Fatalf("CSI %q: got args=%v priv=%v mode=%q, want args=%v priv=%v mode=%q", buf, c.args, c.priv, c.mode, wantArgs, wantPriv, wantMode)
		}
	}
}
