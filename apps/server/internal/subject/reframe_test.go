package subject

import (
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

var vertical = timeline.Output{Width: 1080, Height: 1920}

func TestCentreIsTheMedian(t *testing.T) {
	tr := Track{Aspect: 16.0 / 9.0, Samples: []Sample{
		{AtMs: 0, X: 0.1, Y: 0.2, W: 0.2, H: 0.6}, {AtMs: 500, X: 0.15, Y: 0.2, W: 0.2, H: 0.6}, {AtMs: 900, X: 0.7, Y: 0.2, W: 0.2, H: 0.6},
	}}
	x, y, ok := tr.Centre(timeline.Source{OutMs: 1000})
	if !ok || x < 0.249 || x > 0.251 || y < 0.499 || y > 0.501 {
		t.Errorf("centre = %v %v %v, want 0.25 0.5 (an outlier does not move the median)", x, y, ok)
	}
	if _, _, ok := (Track{}).Centre(timeline.Source{OutMs: 1000}); ok {
		t.Error("an empty track has a centre")
	}
}

func TestWithFocus(t *testing.T) {
	tr := Track{Aspect: 16.0 / 9.0, Samples: []Sample{{AtMs: 100, X: 0.15, Y: 0.2, W: 0.2, H: 0.7, HeadTopY: 0.22}}}
	got := WithFocus(nil, &tr, timeline.Source{OutMs: 1000}, vertical)
	if got.Mode != timeline.ReframeCover || got.Focus == nil || got.Focus.X < 0.249 || got.Focus.X > 0.251 {
		t.Errorf("no reframe: %+v", got)
	}
	// A focus the model gave stays; blur_fill and a clip of the output's shape are not cropped, so they get none.
	given := &timeline.Reframe{Mode: timeline.ReframeCover, Focus: &timeline.Point{X: 0.9, Y: 0.5}}
	if got := WithFocus(given, &tr, timeline.Source{OutMs: 1000}, vertical); got.Focus.X != 0.9 {
		t.Errorf("given focus changed: %+v", got.Focus)
	}
	if got := WithFocus(&timeline.Reframe{Mode: timeline.ReframeBlurFill}, &tr, timeline.Source{OutMs: 1000}, vertical); got.Focus != nil {
		t.Error("blur_fill got a focus")
	}
	same := Track{Aspect: 9.0 / 16.0, Samples: tr.Samples}
	if got := WithFocus(nil, &same, timeline.Source{OutMs: 1000}, vertical); got.Focus != nil {
		t.Error("a 9:16 clip in a 9:16 output got a focus")
	}
	if got := WithFocus(nil, nil, timeline.Source{OutMs: 1000}, vertical); got.Focus != nil {
		t.Error("no track got a focus")
	}
}

func TestInside(t *testing.T) {
	s := Sample{AtMs: 0, X: 0.15, Y: 0.2, W: 0.2, H: 0.7, HeadTopY: 0.22}
	a := 16.0 / 9.0
	if v := Inside(s, a, timeline.Reframe{Mode: timeline.ReframeCover, Focus: &timeline.Point{X: 0.25, Y: 0.5}}, vertical); v < MinInside {
		t.Errorf("centred: %v", v)
	}
	if v := Inside(s, a, timeline.Reframe{Mode: timeline.ReframeCover, Focus: &timeline.Point{X: 0.9, Y: 0.5}}, vertical); v > 0.1 {
		t.Errorf("wrong focus: %v", v)
	}
}
