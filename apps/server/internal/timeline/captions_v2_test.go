package timeline

import (
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

func captionsWith(mut func(*CaptionsTrack)) *CaptionsTrack {
	c := captionsOn(style.CaptionKeyword)
	mut(c)
	return c
}

// Both caption bands are inside the safe zone of every format, and the upper one is clear of the lower one.
func TestBothCaptionBandsAreSafeEverywhere(t *testing.T) {
	for _, f := range []Format{Format9x16, Format1x1, Format4x5, Format16x9} {
		out := withFormat(baseSpec(), f).Output
		zone, _ := out.Zone()
		for _, kind := range []string{style.CaptionChunk, style.CaptionWordHighlight, style.CaptionKeyword} {
			st, _ := style.CaptionStyle(kind)
			lower := style.CaptionBandAt(zone, out.Width, out.ShortSide(), st, style.CaptionLower)
			upper := style.CaptionBandAt(zone, out.Width, out.ShortSide(), st, style.CaptionUpper)
			if !zone.Safe(lower) || !zone.Safe(upper) {
				t.Errorf("%s %s: lower %+v upper %+v are not both inside the safe zone", f, kind, lower, upper)
			}
			if lower.Overlaps(upper) {
				t.Errorf("%s %s: the bands overlap: %+v %+v", f, kind, lower, upper)
			}
			// A caption box of any height up to the band's sits inside its band.
			for _, h := range []int{lower.Height() / 2, lower.Height()} {
				if box := style.PlaceCaptionAt(zone, out.Width, 400, h, style.CaptionUpper); !upper.Contains(box) && h == lower.Height() {
					t.Errorf("%s %s: a %d px caption box %+v leaves the upper band %+v", f, kind, h, box, upper)
				}
			}
		}
	}
}

func TestCaptionFieldsOfTheCreatorValidate(t *testing.T) {
	ok := captionsWith(func(c *CaptionsTrack) {
		c.Fill, c.Accent = "#F5F0DC", "#FFD60A"
		c.Keywords = []string{"protein", "Creatine"}
		c.Corrections = []CaptionCorrection{{AssetID: assetVideo60, Word: 4, Text: "creatine"}}
	})
	wantCodes(t, validateSpec(t, addTrack(baseSpec(), ok)))
}

// A correction is refused when it is hostile: by the schema when the schema can tell (markup, control characters, a length, a colour),
// and by the validator for what only a Unicode check sees (a right-to-left override, a zero-width character) and for what needs the
// catalog or the whole list (an asset that is not the project's, the same word twice).
func TestHostileCaptionFieldsAreRefused(t *testing.T) {
	fix := func(text string) *CaptionsTrack {
		return captionsWith(func(c *CaptionsTrack) {
			c.Corrections = []CaptionCorrection{{AssetID: assetVideo60, Word: 1, Text: text}}
		})
	}
	schema := map[string]*CaptionsTrack{
		"override tags": fix(`{\an8\fs400}hi`),
		"markup":        fix("<b>hi</b>"),
		"pipe":          fix("a|b"),
		"control":       fix("a\nb"),
		"too long":      fix(strings.Repeat("a", 41)),
		"empty":         fix(""),
		"fill":          captionsWith(func(c *CaptionsTrack) { c.Fill = "red" }),
		"accent":        captionsWith(func(c *CaptionsTrack) { c.Accent = `#FFFFFF}{\fs999` }),
		"keyword tags":  captionsWith(func(c *CaptionsTrack) { c.Keywords = []string{`{\fs9}`} }),
		"keyword space": captionsWith(func(c *CaptionsTrack) { c.Keywords = []string{"two words"} }),
		"negative word": captionsWith(func(c *CaptionsTrack) {
			c.Corrections = []CaptionCorrection{{AssetID: assetVideo60, Word: -1, Text: "x"}}
		}),
		"bad asset id": captionsWith(func(c *CaptionsTrack) {
			c.Corrections = []CaptionCorrection{{AssetID: "nope", Word: 1, Text: "x"}}
		}),
	}
	for name, c := range schema {
		res := validateSpec(t, addTrack(baseSpec(), c))
		if res.OK() {
			t.Errorf("%s: accepted", name)
			continue
		}
		wantCodes(t, res, CodeSchema)
	}
	semantic := map[string]*CaptionsTrack{
		"bidi override": fix("exe\u202efdp"),
		"zero width":    fix("a\u200bb"),
		"double space":  fix("a  b"),
		"same word twice": captionsWith(func(c *CaptionsTrack) {
			c.Corrections = []CaptionCorrection{{AssetID: assetVideo60, Word: 1, Text: "x"}, {AssetID: assetVideo60, Word: 1, Text: "y"}}
		}),
	}
	for name, c := range semantic {
		wantCodesNamed(t, name, validateSpec(t, addTrack(baseSpec(), c)), CodeInvalidCaptions)
	}
	unknown := captionsWith(func(c *CaptionsTrack) {
		c.Corrections = []CaptionCorrection{{AssetID: assetUnknown, Word: 1, Text: "x"}}
	})
	res := validateSpec(t, addTrack(baseSpec(), unknown))
	wantCodes(t, res, CodeUnknownAsset)
	if p := res.Issues[0].Path; p != "/tracks/1/corrections/0/asset_id" {
		t.Errorf("path %q", p)
	}
}

func wantCodesNamed(t *testing.T, name string, res Result, want ...Code) {
	t.Helper()
	t.Run(name, func(t *testing.T) { wantCodes(t, res, want...) })
}
