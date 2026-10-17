// Package textclean cleans a short piece of text before it is stored and shown as a label: an asset's title, a model's
// description or tag. There are two policies, and the difference is who wrote the text.
//
// User text (User) is what a person typed or a file name they chose: "Gino's $100 rows [take 2] | ^heavy~". It keeps every
// ordinary typed character, markup characters included (the client renders a title as text, never as HTML). It removes only
// what cannot be typed on purpose or is invisible: control and format characters (the right-to-left override that makes
// "exe.mp4" read backwards, among them), private-use and surrogate code points, invalid UTF-8; and it turns every run of
// white space into one space.
//
// Model text (Model) is what a model wrote about footage it was shown, which may carry words copied from the picture. It gets
// everything User does and, in addition, loses the characters that only matter in markup, templates and shells (< > ` { } \ | ^
// ~ [ ] $), so nothing in it can be taken for a tag, a template action, a link or a command whatever reads it later.
//
// Both return valid UTF-8 in NFC form, on one line, cut to a bounded length. The cut is at a word boundary when that keeps more
// than half of the text.
package textclean

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// modelMarkup are the characters Model drops on top of what User drops.
const modelMarkup = "<>`{}\\|^~[]$"

// Invisible reports whether r is a character no label should hold: a control or format character (this includes the
// bidirectional overrides and the zero-width characters), a line or paragraph separator, a private-use or surrogate code point
// or the replacement character. White space is not in it: User and Model turn it into spaces, and file names drop it.
func Invisible(r rune) bool {
	return r == utf8.RuneError || unicode.IsControl(r) ||
		unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) ||
		unicode.Is(unicode.Co, r) || unicode.Is(unicode.Cs, r)
}

// User cleans text a person wrote. See the package comment. ok is false when the text is not valid UTF-8 or nothing is left.
// A maxRunes below 1 means no limit beyond 4096 characters.
func User(s string, maxRunes int) (string, bool) { return clean(s, maxRunes, false) }

// Model cleans text a model wrote. See the package comment. ok is as for User; a leading or trailing ". , ; : - _" is also
// trimmed.
func Model(s string, maxRunes int) (string, bool) { return clean(s, maxRunes, true) }

func clean(s string, maxRunes int, model bool) (string, bool) {
	if !utf8.ValidString(s) {
		return "", false
	}
	if maxRunes < 1 || maxRunes > 4096 {
		maxRunes = 4096
	}
	s = norm.NFC.String(s)
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r), unicode.Is(unicode.Zs, r):
			return ' ' // tabs and newlines become spaces: a label is one line
		case Invisible(r):
			return -1
		case model && strings.ContainsRune(modelMarkup, r):
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > maxRunes {
		s = cut(s, maxRunes)
	}
	if model {
		s = strings.Trim(s, " .,;:-_")
	}
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
