package timeline

import (
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

func captionsOn(kind string) *CaptionsTrack {
	return &CaptionsTrack{ID: "captions", From: "transcript", Style: kind, Enabled: true}
}

// A character the style's font has no glyph for cannot be measured, so the text is refused: unsupported_character names the
// characters and the style.
func TestUnsupportedCharacter(t *testing.T) {
	for _, text := range []string{"Go \U0001F600", "日本語", "Anton has no Cyrillic: Привет"} {
		sty := "title_bold"
		if strings.Contains(text, "Cyrillic") {
			sty = "title_outline" // Anton
		}
		item := TextItem{ID: "t1", AtMs: 0, DurationMs: 1000, Text: text, Style: sty, Anchor: AnchorUpperThird}
		res := validateSpec(t, addTrack(baseSpec(), textTrack(item)))
		wantCodes(t, res, CodeUnsupportedCharacter)
		if res.Issues[0].Path != "/tracks/1/items/0/text" || !strings.Contains(res.Issues[0].Message, "U+") {
			t.Errorf("%q: %+v", text, res.Issues[0])
		}
	}
	// Letters, digits, punctuation, accents, Cyrillic in a font that has it, and zero-width characters are fine.
	for _, text := range []string{"Squat: 5x5 - 100 kg! ‘quoted’ … €", "Ünïcödé ÀÉÎÕÜ", "Привет", "a\u2060b"} {
		item := TextItem{ID: "t1", AtMs: 0, DurationMs: 1000, Text: text, Style: "title_bold", Anchor: AnchorUpperThird}
		wantCodes(t, validateSpec(t, addTrack(baseSpec(), textTrack(item))))
	}
}

// While an enabled captions track draws in its band, a text item that covers the band is text_overlaps_captions; the
// anchors upper_third and lower_third and a position clear of the band are fine, and a disabled track reserves nothing.
func TestTextOverlapsCaptions(t *testing.T) {
	for _, f := range []Format{Format9x16, Format1x1, Format4x5, Format16x9} {
		spec := func(it TextItem, c *CaptionsTrack) *Spec {
			return addTrack(addTrack(withFormat(baseSpec(), f), textTrack(it)), c)
		}
		center := TextItem{ID: "t1", AtMs: 0, DurationMs: 1000, Text: "Hi", Style: "title_bold", Anchor: AnchorCenter}
		res := validateSpec(t, spec(center, captionsOn("chunk")))
		wantCodes(t, res, CodeTextOverlapsCaptions)
		if !strings.Contains(res.Issues[0].Message, "captions") || res.Issues[0].Path != "/tracks/1/items/0/anchor" {
			t.Errorf("%s: %+v", f, res.Issues[0])
		}
		off := captionsOn("word_highlight")
		off.Enabled = false
		wantCodes(t, validateSpec(t, spec(center, off)))
		for _, a := range []string{AnchorUpperThird, AnchorLowerThird} {
			it := center
			it.Anchor = a
			wantCodes(t, validateSpec(t, spec(it, captionsOn("chunk"))))
		}
	}
}

// The caption band is inside every format's safe zone for both caption styles, so every caption the renderer fits to it is safe.
func TestCaptionBandIsSafeEverywhere(t *testing.T) {
	for _, f := range []Format{Format9x16, Format1x1, Format4x5, Format16x9} {
		for _, kind := range []string{style.CaptionChunk, style.CaptionWordHighlight} {
			wantCodes(t, validateSpec(t, addTrack(withFormat(baseSpec(), f), captionsOn(kind))))
			out := withFormat(baseSpec(), f).Output
			zone, _ := out.Zone()
			st, _ := style.CaptionStyle(kind)
			if band := style.CaptionBand(zone, out.Width, out.ShortSide(), st); !zone.Safe(band) {
				t.Errorf("%s %s: band %+v", f, kind, band)
			}
		}
	}
}
