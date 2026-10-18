package evals

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/nilpntr/content-fabrique/internal/agents/editor"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// Recorded human feedback (suites/feedback.yaml): what the founder said about edits, each item with a weight that outweighs any one
// judge dimension and, where it can be measured, a detector that scores it from the timeline and the checks without a model. The judge
// is shown the items too, but the score of an item is the detector's: the human outweighs the judge.

// FeedbackItem is one piece of recorded human feedback.
type FeedbackItem struct {
	ID   string `yaml:"id"`
	Text string `yaml:"text"`
	// Weight is the item's weight in a case's score; DefaultFeedbackWeight when unset.
	Weight float64 `yaml:"weight"`
	// Detector names the measurement that scores it (Detectors); empty leaves it to the judge's rubric only.
	Detector string `yaml:"detector"`
	Source   string `yaml:"source"`
	Date     string `yaml:"date"`
}

// DefaultFeedbackWeight is a feedback item's weight when the file sets none: three judge dimensions.
const DefaultFeedbackWeight = 3

// LoadFeedback reads a feedback file.
func LoadFeedback(path string) ([]FeedbackItem, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- the suite's own feedback file
	if err != nil {
		return nil, fmt.Errorf("feedback: %w", err)
	}
	var f struct {
		Items []FeedbackItem `yaml:"feedback"`
	}
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("feedback %s: %w", path, err)
	}
	var errs []error
	seen := map[string]bool{}
	for i, it := range f.Items {
		if !idRE.MatchString(it.ID) || seen[it.ID] || strings.TrimSpace(it.Text) == "" {
			errs = append(errs, fmt.Errorf("feedback item %q: a unique id and a text are required", it.ID))
		}
		seen[it.ID] = true
		if it.Detector != "" && Detectors[it.Detector] == nil {
			errs = append(errs, fmt.Errorf("feedback item %s: unknown detector %q", it.ID, it.Detector))
		}
		if it.Weight == 0 {
			f.Items[i].Weight = DefaultFeedbackWeight
		}
	}
	return f.Items, errors.Join(errs...)
}

// Status of a measurement.
const (
	Pass = "pass"
	Fail = "fail"
	Skip = "skip"
)

// FeedbackResult is an item measured on one version.
type FeedbackResult struct {
	ID     string  `json:"id"`
	Status string  `json:"status"`
	Detail string  `json:"detail"`
	Weight float64 `json:"weight"`
}

// Measured is what a detector reads: the version's timeline, its check report and the clips' analysis.
type Measured struct {
	Spec   *timeline.Spec
	Report editor.Report
	Assets map[string]editor.AssetAnalysis
}

// Detector scores one feedback item.
type Detector func(m Measured) (status, detail string)

// Detectors are the measurable feedback items.
var Detectors = map[string]Detector{
	"transitions_ai":    detectAITransitions,
	"text_on_body":      detectTextOnBody,
	"static_long_takes": detectStaticLongTakes,
	"music_only":        detectMusicOnly,
}

// aiTransitions are the transitions the founder said read as an automatic AI edit.
var aiTransitions = map[string]bool{
	timeline.TransitionPunch: true, timeline.TransitionZoomThrough: true, timeline.TransitionWhip: true,
	timeline.TransitionWhipLeft: true, timeline.TransitionWhipRight: true, timeline.TransitionFlash: true,
}

func detectAITransitions(m Measured) (string, string) {
	main, _, ok := timeline.MainTrack(m.Spec)
	if !ok {
		return Skip, "no main track"
	}
	var used []string
	for _, it := range main.Items {
		if it.TransitionIn != nil && aiTransitions[it.TransitionIn.Type] {
			used = append(used, fmt.Sprintf("%s into %s", it.TransitionIn.Type, it.ID))
		}
	}
	if len(used) > 0 {
		return Fail, fmt.Sprintf("%d effect transitions: %s", len(used), strings.Join(used, ", "))
	}
	return Pass, "hard cuts (or soft transitions) only"
}

func detectTextOnBody(m Measured) (string, string) {
	for _, c := range m.Report.Checks {
		if c.ID == editor.CheckTextOverSubject {
			switch c.Status {
			case editor.CheckFail:
				return Fail, c.Detail
			case editor.CheckPass:
				return Pass, c.Detail
			}
			return Skip, c.Detail
		}
	}
	return Skip, "the text-over-subject check did not run"
}

// staticTakeMs is how long a shot may hold with no camera motion before it reads as dead.
const staticTakeMs = 4000

func detectStaticLongTakes(m Measured) (string, string) {
	main, _, ok := timeline.MainTrack(m.Spec)
	if !ok {
		return Skip, "no main track"
	}
	var dead []string
	for _, it := range main.Items {
		d := timeline.OutputDuration(it)
		if d <= staticTakeMs || it.Motion.Active() {
			continue
		}
		if a, ok := m.Assets[it.AssetID]; ok && a.Camera == "handheld" {
			continue // footage that moves by itself is not a static take
		}
		dead = append(dead, fmt.Sprintf("%s %.1f s", it.ID, float64(d)/1000))
	}
	if len(dead) > 0 {
		return Fail, fmt.Sprintf("%d long shots with a still camera and no motion: %s", len(dead), strings.Join(dead, ", "))
	}
	return Pass, fmt.Sprintf("no still shot over %.1f s", float64(staticTakeMs)/1000)
}

func detectMusicOnly(m Measured) (string, string) {
	main, _, ok := timeline.MainTrack(m.Spec)
	if !ok {
		return Skip, "no main track"
	}
	// Under music the renderer leaves a clip's ambience bed out; a clip in full mode (speech) still plays, over the ducked song.
	var loud []string
	for _, it := range main.Items {
		if mode := timeline.ClipAudioMode(it.Audio); mode == timeline.AudioModeFull {
			loud = append(loud, it.ID)
		}
	}
	switch {
	case len(loud) > 0:
		return Fail, fmt.Sprintf("the clips' own sound plays over the song: %s", strings.Join(loud, ", "))
	case !timeline.HasMusic(m.Spec):
		return Fail, "the edit has no music"
	}
	return Pass, "the song only; no clip plays its own sound"
}

// MeasureFeedback scores the items that have a detector; the others are skipped (the judge's rubric covers them).
func MeasureFeedback(items []FeedbackItem, m Measured) []FeedbackResult {
	out := make([]FeedbackResult, 0, len(items))
	for _, it := range items {
		r := FeedbackResult{ID: it.ID, Status: Skip, Detail: "judged only", Weight: it.Weight}
		if d := Detectors[it.Detector]; d != nil && m.Spec != nil {
			r.Status, r.Detail = d(m)
		}
		out = append(out, r)
	}
	return out
}
