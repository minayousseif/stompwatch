package web

// A bitmap font, in the source, so the rendered PNG needs no font file at
// run time and cannot fail on a machine that has none installed.
//
// It covers the digits, the punctuation, and the letters the timeline image
// actually writes. It is not a Unicode font and is not meant to become one.
// A rune it does not know draws as an empty box, so a missing letter shows
// up instead of quietly disappearing.

// glyphWidth and glyphHeight are the size of one glyph, in pixels, before
// the drawing scales it up.
const (
	glyphWidth  = 5
	glyphHeight = 7
	// glyphGap is the blank column between two glyphs.
	glyphGap = 1
)

// font5x7 is each glyph drawn out row by row, so a reader can see the shape
// it will put on the image.
var font5x7 = map[rune][]string{
	' ': {".....", ".....", ".....", ".....", ".....", ".....", "....."},
	'0': {".###.", "#...#", "#..##", "#.#.#", "##..#", "#...#", ".###."},
	'1': {"..#..", ".##..", "..#..", "..#..", "..#..", "..#..", ".###."},
	'2': {".###.", "#...#", "....#", "...#.", "..#..", ".#...", "#####"},
	'3': {"#####", "...#.", "..#..", "...#.", "....#", "#...#", ".###."},
	'4': {"...#.", "..##.", ".#.#.", "#..#.", "#####", "...#.", "...#."},
	'5': {"#####", "#....", "####.", "....#", "....#", "#...#", ".###."},
	'6': {"..##.", ".#...", "#....", "####.", "#...#", "#...#", ".###."},
	'7': {"#####", "....#", "...#.", "..#..", ".#...", ".#...", ".#..."},
	'8': {".###.", "#...#", "#...#", ".###.", "#...#", "#...#", ".###."},
	'9': {".###.", "#...#", "#...#", ".####", "....#", "...#.", ".##.."},
	':': {".....", "..#..", "..#..", ".....", "..#..", "..#..", "....."},
	'-': {".....", ".....", ".....", "#####", ".....", ".....", "....."},
	'.': {".....", ".....", ".....", ".....", ".....", ".##..", ".##.."},
	',': {".....", ".....", ".....", ".....", ".##..", ".##..", ".#..."},
	'(': {"...#.", "..#..", ".#...", ".#...", ".#...", "..#..", "...#."},
	')': {".#...", "..#..", "...#.", "...#.", "...#.", "..#..", ".#..."},
	'%': {"##..#", "##..#", "...#.", "..#..", ".#...", "#..##", "#..##"},
	// The plus and the slash spell the plus or minus in the caption, which
	// says how well the levels in the picture are known.
	'+': {".....", "..#..", "..#..", "#####", "..#..", "..#..", "....."},
	'/': {"....#", "....#", "...#.", "..#..", ".#...", "#....", "#...."},
	'B': {"####.", "#...#", "#...#", "####.", "#...#", "#...#", "####."},
	'a': {".....", ".....", ".###.", "....#", ".####", "#...#", ".####"},
	'c': {".....", ".....", ".###.", "#...#", "#....", "#...#", ".###."},
	'd': {"....#", "....#", ".####", "#...#", "#...#", "#...#", ".####"},
	'e': {".....", ".....", ".###.", "#...#", "#####", "#....", ".###."},
	'f': {"..##.", ".#..#", ".#...", "###..", ".#...", ".#...", ".#..."},
	'g': {".....", ".####", "#...#", "#...#", ".####", "....#", ".###."},
	'h': {"#....", "#....", "#.##.", "##..#", "#...#", "#...#", "#...#"},
	'i': {"..#..", ".....", ".##..", "..#..", "..#..", "..#..", ".###."},
	'j': {"...#.", ".....", "..##.", "...#.", "...#.", "#..#.", ".##.."},
	'l': {".##..", "..#..", "..#..", "..#..", "..#..", "..#..", ".###."},
	'm': {".....", ".....", "##.#.", "#.#.#", "#.#.#", "#...#", "#...#"},
	'n': {".....", ".....", "#.##.", "##..#", "#...#", "#...#", "#...#"},
	'o': {".....", ".....", ".###.", "#...#", "#...#", "#...#", ".###."},
	'r': {".....", ".....", "#.##.", "##..#", "#....", "#....", "#...."},
	's': {".....", ".....", ".####", "#....", ".###.", "....#", "####."},
	't': {".#...", ".#...", "###..", ".#...", ".#...", ".#..#", "..##."},
	'u': {".....", ".....", "#...#", "#...#", "#...#", "#..##", ".##.#"},
	'v': {".....", ".....", "#...#", "#...#", "#...#", ".#.#.", "..#.."},
}

// missing draws in place of a rune the font does not know, so a letter that
// was never added shows up on the image instead of vanishing.
var missing = []string{"#####", "#...#", "#...#", "#...#", "#...#", "#...#", "#####"}

// hasGlyph reports whether the font knows a rune.
func hasGlyph(r rune) bool { _, ok := font5x7[r]; return ok }

// glyph returns the rows of a rune, or the empty box for one the font does
// not know.
func glyph(r rune) []string {
	if rows, ok := font5x7[r]; ok {
		return rows
	}
	return missing
}

// textWidth is how wide a string is once drawn, in pixels.
func textWidth(s string, scale int) int {
	n := len([]rune(s))
	if n == 0 {
		return 0
	}
	return (n*(glyphWidth+glyphGap) - glyphGap) * scale
}
