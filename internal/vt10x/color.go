package vt10x

// ANSI color values
const (
	Black Color = iota
	Red
	Green
	Yellow
	Blue
	Magenta
	Cyan
	LightGrey
	DarkGrey
	LightRed
	LightGreen
	LightYellow
	LightBlue
	LightMagenta
	LightCyan
	White
)

// Default colors are potentially distinct to allow for special behavior.
// For example, a transparent background. Otherwise, the simple case is to
// map default colors to another color.
const (
	DefaultFG Color = 1<<25 + iota
	DefaultBG
	DefaultCursor
	DefaultUL // underline color unset — inherits from FG
)

// rgbFlag marks a truecolour value. Without it, a colour with zero red and
// green and blue below 256 would equal a palette index.
const rgbFlag Color = 1 << 24

// Color maps to the ANSI colors [0, 16), the xterm colors [16, 256),
// truecolour values from RGB in [1<<24, DefaultFG), and the defaults.
type Color uint32

// RGB returns a truecolour value. Its low 24 bits hold r<<16 | g<<8 | b,
// and it is below DefaultFG, so callers can test c < 256 for the palette,
// then c < DefaultFG for truecolour.
func RGB(r, g, b uint8) Color {
	return rgbFlag | Color(r)<<16 | Color(g)<<8 | Color(b)
}

// ANSI returns true if Color is within [0, 16).
func (c Color) ANSI() bool {
	return (c < 16)
}
