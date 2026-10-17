// Package textclean cleans a short piece of text before it is stored and shown as a label: an asset's title, a model's
// description or tag. It is the one definition of "clean" for those, so a title a person types, a title a model writes and
// a file name turned into a title all end up in the same safe shape.
//
// Clean text is valid UTF-8 in NFC form, has no control or invisible formatting characters (the right-to-left override that
// makes "exe.mp4" read backwards, among them), no markup or template syntax characters (< > ` { } and the like), one space
// between words, and a bounded length. The result is safe to store and to render as text; it is not an escape for HTML, which
// the client does by rendering it as text.
package textclean

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// markup are the characters dropped from a label because they only matter in markup, templates and shells.
const markup = "<>`{}\\|^~[]$"

// Clean returns s cleaned and cut to at most maxRunes characters (at a word boundary when it can). ok is false when the text
// is not valid UTF-8 or nothing is left after cleaning. A maxRunes below 1 means no limit beyond 4096 characters.
func Clean(s string, maxRunes int) (string, bool) {
	if !utf8.ValidString(s) {
		return "", false
	}
	if maxRunes < 1 || maxRunes > 4096 {
		maxRunes = 4096
	}
	s = norm.NFC.String(s)
	s = strings.Map(func(r rune) rune {
		switch {
		case r == utf8.RuneError:
			return -1
		case unicode.IsSpace(r), unicode.Is(unicode.Zs, r), unicode.Is(unicode.Zl, r), unicode.Is(unicode.Zp, r):
			return ' ' // tabs and newlines become spaces: a label is one line
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), unicode.Is(unicode.Co, r), unicode.Is(unicode.Cs, r):
			return -1
		case strings.ContainsRune(markup, r):
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > maxRunes {
		s = cut(s, maxRunes)
	}
	s = strings.Trim(s, " .,;:-_")
	return s, s != ""
}

// cut shortens s to at most n runes, at the last space when that keeps more than half of it.
func cut(s string, n int) string {
	out := string([]rune(s)[:n])
	if i := strings.LastIndex(out, " "); i > len(out)/2 {
		out = out[:i]
	}
	return strings.TrimSpace(out)
}
