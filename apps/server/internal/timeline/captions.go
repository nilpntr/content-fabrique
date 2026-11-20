package timeline

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/nilpntr/content-fabrique/internal/textclean"
	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

// The limits of the captions track's own fields. The JSON Schema repeats them; the op and the compiler enforce them again, because a
// spec built in Go never went through the schema and a correction is text the creator typed.
const (
	// MaxCaptionCorrections is how many corrected words a captions track holds.
	MaxCaptionCorrections = 200
	// MaxCorrectionRunes is the longest replacement of a word, in characters.
	MaxCorrectionRunes = 40
	// MaxCaptionKeywords is how many key words keyword_emphasis takes, and MaxKeywordRunes the longest of them.
	MaxCaptionKeywords = 12
	MaxKeywordRunes    = 24
	// MaxCorrectionWord is the highest word position a correction may name.
	MaxCorrectionWord = 99999
)

// CleanCorrection checks the replacement text of a correction and returns it. A text is refused, never silently changed, when it
// is empty, too long, not valid UTF-8, or holds a control, format (bidirectional override, zero width), private-use or surrogate
// character, or one of the characters that matter in markup, templates and scripts (< > ` { } \ | ^ ~ [ ] $), or runs of white
// space: textclean.Model would remove or change them, so a text it changes is a text that was not clean.
func CleanCorrection(text string) (string, error) {
	if text == "" {
		return "", errors.New("is empty")
	}
	if n := len([]rune(text)); n > MaxCorrectionRunes {
		return "", fmt.Errorf("has %d characters, at most %d", n, MaxCorrectionRunes)
	}
	clean, ok := textclean.Model(text, MaxCorrectionRunes)
	if !ok || clean != text {
		return "", errors.New("holds control, invisible or markup characters (< > { } \\ | ^ ~ [ ] $ `) or extra white space")
	}
	return clean, nil
}

// ValidateCaptionFields checks what a captions track or a set_captions op carries besides the style: the colours, the key words and
// the corrections. It returns one message per problem, each naming the field.
func ValidateCaptionFields(fill, accent string, keywords []string, corrections []CaptionCorrection) []string {
	var bad []string
	for _, f := range []struct{ name, v string }{{"fill", fill}, {"accent", accent}} {
		if err := (style.Overrides{Fill: f.v}).Validate(); err != nil {
			bad = append(bad, fmt.Sprintf("%s %q is not a colour like #F2E9A0 (a # and six hex digits)", f.name, f.v))
		}
	}
	if len(keywords) > MaxCaptionKeywords {
		bad = append(bad, fmt.Sprintf("keywords has %d words, at most %d", len(keywords), MaxCaptionKeywords))
	}
	for i, k := range keywords {
		if i >= MaxCaptionKeywords {
			break
		}
		if n := len([]rune(k)); n == 0 || n > MaxKeywordRunes || strings.IndexFunc(k, unicode.IsSpace) >= 0 {
			bad = append(bad, fmt.Sprintf("keywords[%d] %q must be one word of 1 to %d characters", i, k, MaxKeywordRunes))
		} else if _, err := CleanCorrection(k); err != nil {
			bad = append(bad, fmt.Sprintf("keywords[%d] %q %v", i, k, err))
		}
	}
	if len(corrections) > MaxCaptionCorrections {
		bad = append(bad, fmt.Sprintf("corrections has %d entries, at most %d", len(corrections), MaxCaptionCorrections))
	}
	type key struct {
		asset string
		word  int
	}
	seen := map[key]bool{}
	for i, c := range corrections {
		if i >= MaxCaptionCorrections {
			break
		}
		if c.AssetID == "" {
			bad = append(bad, fmt.Sprintf("corrections[%d] has no asset_id", i))
		}
		if c.Word < 0 || c.Word > MaxCorrectionWord {
			bad = append(bad, fmt.Sprintf("corrections[%d] word %d is outside 0 to %d", i, c.Word, MaxCorrectionWord))
		}
		if _, err := CleanCorrection(c.Text); err != nil {
			bad = append(bad, fmt.Sprintf("corrections[%d] text %q %v", i, c.Text, err))
		}
		k := key{c.AssetID, c.Word}
		if seen[k] {
			bad = append(bad, fmt.Sprintf("corrections[%d] corrects word %d of %s a second time", i, c.Word, c.AssetID))
		}
		seen[k] = true
	}
	return bad
}

// captionBand returns the band an enabled captions track draws in, and whether there is one. It also checks that the band
// itself is inside the safe zone (unsafe_text on the captions track): the renderer re-chunks and shrinks every caption until it
// fits the band, so the band being safe is what keeps every caption safe.
func (c *checker) captionBand() (style.Rect, bool) {
	out := c.spec.Output
	zone, ok := out.Zone()
	if !ok {
		return style.Rect{}, false
	}
	for ti, t := range c.spec.Tracks {
		tr, ok := t.(*CaptionsTrack)
		if !ok || !tr.Enabled {
			continue
		}
		st, ok := style.CaptionStyle(tr.Style)
		if !ok {
			continue
		}
		band := style.CaptionBand(zone, out.Width, out.ShortSide(), st)
		if !zone.Safe(band) {
			c.add(CodeUnsafeText, fmt.Sprintf("/tracks/%d", ti),
				"the caption band of the %s captions (%d lines of %s, x %d to %d, y %d to %d px) is not inside the %s safe zone",
				tr.Style, style.CaptionLines, st.ID, band.Left, band.Right, band.Top, band.Bottom, out.Format)
		}
		return band, true
	}
	return style.Rect{}, false
}

// checkCaptions checks the captions track's own fields (invalid_captions): the colours, the key words and the corrections, each
// against the limits of the schema, which a spec built in Go never went through, and the assets the corrections name (unknown_asset).
func (c *checker) checkCaptions() {
	for ti, t := range c.spec.Tracks {
		tr, ok := t.(*CaptionsTrack)
		if !ok {
			continue
		}
		if _, ok := style.CaptionStyle(tr.Style); !ok {
			c.add(CodeUnknownStyle, fmt.Sprintf("/tracks/%d/style", ti), "the captions style %q is not one of word_highlight, chunk, keyword_emphasis", tr.Style)
		}
		for _, msg := range ValidateCaptionFields(tr.Fill, tr.Accent, tr.Keywords, tr.Corrections) {
			c.add(CodeInvalidCaptions, fmt.Sprintf("/tracks/%d", ti), "captions track %s: %s", tr.ID, msg)
		}
		for i, cor := range tr.Corrections {
			if i >= MaxCaptionCorrections || cor.AssetID == "" {
				continue
			}
			if _, ok := c.assets.Lookup(cor.AssetID); !ok {
				c.add(CodeUnknownAsset, fmt.Sprintf("/tracks/%d/corrections/%d/asset_id", ti, i),
					"correction %d of the captions names asset %s, which is not in the catalog", i, cor.AssetID)
			}
		}
	}
}

// quoteRunes lists characters for a message, with their code points.
func quoteRunes(rs []rune) string {
	parts := make([]string, 0, len(rs))
	for i, r := range rs {
		if i == 8 {
			parts = append(parts, "...")
			break
		}
		parts = append(parts, fmt.Sprintf("%q U+%04X", r, r))
	}
	return strings.Join(parts, ", ")
}
