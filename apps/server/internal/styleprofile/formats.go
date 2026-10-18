package styleprofile

import (
	"fmt"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/camshift"
	"github.com/nilpntr/content-fabrique/internal/formats"
)

// The formats of the creator's videos (docs/design/formats-v1.md): each reference is tagged with the formats of the library it is
// likely in, from its measures and the text the style read already reads (no model call), and the profile keeps the share of each
// format among the creator's videos: "your go-to formats" in the app, and what the editor's list_formats ranks by and marks as new.

// FormatsProfile is the creator's formats: the library they were detected with (a profile tagged with an older library is tagged again
// when it is read), how many references had their text read (without it only the cuts can tell a format), and the shares.
type FormatsProfile struct {
	Library string          `json:"library"`
	Read    int             `json:"read"`
	Shares  []formats.Share `json:"shares,omitempty"`
}

// FormatReference is what format detection reads of one reference: its cuts and shots, the drop, the camera, and its text when it was
// read (the creator's words, only matched, never passed on).
func FormatReference(m Measures) formats.Reference {
	ref := formats.Reference{DurationS: m.DurationS, CutsS: m.CutsS, ShotsS: m.ShotsS, DropS: m.Audio.DropS,
		Handheld: m.Video.Camera.Verdict() == camshift.Handheld}
	if m.Audio.LiftLU > 0 {
		ref.LiftS = m.Audio.IntroS
	}
	if t := m.Text; t != nil {
		ref.TextRead = true
		ref.Pattern = ClassifyText(t)
		if t.TitlePresent {
			ref.Title = t.Text
		}
		ref.Chapters = t.Chapters
		for _, s := range t.ShotTexts {
			ref.Lines = append(ref.Lines, formats.Line{AtS: s.AtS, Text: s.Text})
		}
	}
	return ref
}

// FormatTags are the formats of one reference, the most likely first.
func FormatTags(m Measures) []formats.Tag { return formats.Default().Detect(FormatReference(m)) }

// AggregateFormats tags every reference and counts the formats.
func AggregateFormats(refs []Measures) *FormatsProfile {
	if len(refs) == 0 {
		return nil
	}
	lib := formats.Default()
	out := &FormatsProfile{Library: lib.Version}
	tags := make([][]formats.Tag, len(refs))
	for i, m := range refs {
		if m.Text != nil {
			out.Read++
		}
		tags[i] = lib.Detect(FormatReference(m))
	}
	out.Shares = formats.Shares(tags)
	return out
}

// formatsCurrent says the profile's formats were detected with the library of this build.
func (p *Profile) formatsCurrent() bool {
	return p.Formats != nil && p.Formats.Library == formats.Default().Version
}

// FormatShares are the creator's formats (nil when none was found, or the profile is nil).
func (p *Profile) FormatShares() []formats.Share {
	if p == nil || p.Formats == nil {
		return nil
	}
	return p.Formats.Shares
}

// formatsWords are the creator's formats in plain words: "101 tutorials (3 of 5 videos), numbered lists (2 of 5)"; "" for none.
func (p *Profile) formatsWords() string {
	var parts []string
	for i, s := range p.FormatShares() {
		c, ok := formats.Default().Card(s.ID)
		if !ok {
			continue
		}
		if i == 0 {
			parts = append(parts, fmt.Sprintf("%s (%d of %d %s)", c.Plural, s.Videos, p.References, plural(p.References, "video", "videos")))
		} else {
			parts = append(parts, fmt.Sprintf("%s (%d of %d)", c.Plural, s.Videos, p.References))
		}
	}
	return strings.Join(parts, ", ")
}

// formatsLine is the creator's formats for the editor's block, by id ("" for none).
func (p *Profile) formatsLine() string {
	shares := p.FormatShares()
	if len(shares) == 0 {
		return ""
	}
	var parts []string
	for _, s := range shares {
		parts = append(parts, fmt.Sprintf("%s (%d of %d videos)", s.ID, s.Videos, p.References))
	}
	return "formats: the creator's go-to formats are " + strings.Join(parts, ", ") + "; list_formats ranks the formats by them"
}
