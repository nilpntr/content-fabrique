// Package motion is the motion-graphics overlay layer (D-35, docs/design/motion-graphics-v1.md): a versioned library of HTML
// templates animated with the Web Animations API, rendered frame by frame by headless Chrome inside the media sandbox (the cf-motion
// runner of the media-tools image) and composited over the edit by ffmpeg.
//
// The editor never writes HTML. A graphics item of the timeline names a template and gives it params; this package turns the item
// into what the template draws (its box, laid out like a text item with the fonts' real widths, kept in the safe zone and off the
// subject; its lines, colours and motion; the times its elements enter and leave), builds the job the runner renders, and runs it.
package motion

import (
	"slices"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// Template describes one template of the library, for the editor (list_motion_templates) and the design note.
type Template struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
	// Description says what it looks like; Use says when to use it.
	Description string `json:"description"`
	Use         string `json:"use"`
	// Params summarises the params the template takes beyond the common ones (text, style, fill, accent, scale, align, shadow,
	// backing, energy, accents_ms); the schema is the contract.
	Params string `json:"params"`
	// Accents says what the accents_ms land on.
	Accents string `json:"accents"`
	// Fallback is the ASS animation drawn when motion graphics are off.
	Fallback string `json:"fallback"`
	// Example is a params object that renders well.
	Example map[string]any `json:"example"`
}

var catalog = []Template{
	{
		Name: timeline.TemplateTitlePop, Version: 1,
		Description: "The title arrives word by word: each word scales up from small with a blur, overshoots a little and settles; then it floats and pulses on the beat, and leaves last word first.",
		Use:         "The hook title and any big title on a lively or hype edit. Put one accent per word on the beats so the words land on the music.",
		Params:      "text (1 or 2 short lines), style (the creator's title style), accents_ms (one beat per word; extra beats pulse the title)",
		Accents:     "word i lands (its overshoot peak) on accent i; the accents after the last word pulse the whole title",
		Fallback:    timeline.FallbackAnimation(timeline.TemplateTitlePop),
		Example:     map[string]any{"text": "BACK DAY 101", "style": "title_series", "energy": "hype"},
	},
	{
		Name: timeline.TemplateBlurIn, Version: 1,
		Description: "The title sharpens out of a blur while it drifts into place, line by line, and leaves drifting up into a blur. No overshoot: quieter than title_pop.",
		Use:         "A calm title, a quote, a lowercase one-liner, or the title of a slow, moody edit.",
		Params:      "text, style",
		Accents:     "line i is sharp on accent i; later accents give a soft blur pulse",
		Fallback:    timeline.FallbackAnimation(timeline.TemplateBlurIn),
		Example:     map[string]any{"text": "how it feels", "style": "title_lowercase", "energy": "calm"},
	},
	{
		Name: timeline.TemplateChapterCard, Version: 1,
		Description: "A numbered chapter title (\"1. SHRUGS VOOR UPPER TRAPS\"): the number pops first in the accent colour with a small turn, then the line sweeps in after it word by word, wiped in from the left. two_line puts the number on its own line above.",
		Use:         "One per exercise or section, when it starts: the viewer always knows where they are. Number them in order.",
		Params:      "text (the chapter title without its number), number (1 to 99), layout (one_line or two_line)",
		Accents:     "the number pops on the first accent and the line sweeps in on the second; later accents pulse the number",
		Fallback:    timeline.FallbackAnimation(timeline.TemplateChapterCard),
		Example:     map[string]any{"text": "SEATED ROW", "number": 2, "style": "title_series", "accent": "#FFD60A"},
	},
	{
		Name: timeline.TemplateHighlightWord, Version: 1,
		Description: "The line rises in, then one word gets a marker: a slanted bar in the accent colour swipes in behind it from the left and the word turns to the colour that reads on it.",
		Use:         "A cue or a key word: \"SQUEEZE AT THE TOP\" with SQUEEZE marked. One per moment.",
		Params:      "text, word (which word gets the marker, from 1)",
		Accents:     "the marker swipes in on the first accent; later accents pulse the marked word",
		Fallback:    timeline.FallbackAnimation(timeline.TemplateHighlightWord),
		Example:     map[string]any{"text": "SQUEEZE AT THE TOP", "word": 1},
	},
	{
		Name: timeline.TemplateCounter, Version: 1,
		Description: "A number that counts from one value to another (tabular digits, so nothing jitters) and lands with a pop, its label sliding in next to it: 0 to 12 REPS, 60 to 100 KG.",
		Use:         "A rep count, a weight, a time: a number the viewer should remember. Land it on a beat or on the last rep.",
		Params:      "text (the label after the number, such as REPS or KG), from, to, decimals (0 to 2)",
		Accents:     "the count lands on the first accent; later accents pulse the number",
		Fallback:    timeline.FallbackAnimation(timeline.TemplateCounter),
		Example:     map[string]any{"text": "REPS", "from": 0, "to": 12},
	},
	{
		Name: timeline.TemplateLowerLabel, Version: 1,
		Description: "A small label on a dark plate: an accent stripe drops in at the left, the plate wipes out from it and the words rise in from below its edge; it leaves the other way.",
		Use:         "A name, an exercise name or a tag, low or high in the frame, out of the way of the title.",
		Params:      "text (a few words), style (default label_box)",
		Accents:     "it enters on the first accent; later accents flick the stripe",
		Fallback:    timeline.FallbackAnimation(timeline.TemplateLowerLabel),
		Example:     map[string]any{"text": "LAT PULLDOWN"},
	},
	{
		Name: timeline.TemplateOutroCard, Version: 1,
		Description: "The loop card at the end: the letters drop in one after another with a blur, an accent line grows from the centre under them, the card breathes, and it pushes toward the viewer and blurs out on the last frame so the video loops back to its hook.",
		Use:         "The last 1.5 to 3 s: the call to save or follow, or a line that sends the viewer back to the start.",
		Params:      "text (one or two short lines)",
		Accents:     "the letters start on the first accent; later accents flick the line",
		Fallback:    timeline.FallbackAnimation(timeline.TemplateOutroCard),
		Example:     map[string]any{"text": "SAVE FOR\nBACK DAY"},
	},
}

// Templates returns the library, in the schema's order.
func Templates() []Template { return slices.Clone(catalog) }

// Lookup returns a template by name.
func Lookup(name string) (Template, bool) {
	for _, t := range catalog {
		if t.Name == name {
			return t, true
		}
	}
	return Template{}, false
}
