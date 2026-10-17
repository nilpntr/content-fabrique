package ops

import (
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// set_clip_audio takes a mode (P3-12), alone or for every clip, and keeps use_source as the old spelling.
func TestSetClipAudioMode(t *testing.T) {
	base := baseSpec(t)
	modeOf := func(spec timeline.Spec, id string) string {
		for _, tr := range spec.Tracks {
			if vt, ok := tr.(*timeline.VideoTrack); ok {
				for _, it := range vt.Items {
					if it.ID == id {
						return timeline.ClipAudioMode(it.Audio)
					}
				}
			}
		}
		return "?"
	}
	t.Run("every clip at once", func(t *testing.T) {
		out := mustApply(t, base, `[{"op":"set_clip_audio","mode":"bed"}]`)
		for _, tr := range out.Tracks {
			if vt, ok := tr.(*timeline.VideoTrack); ok {
				for _, it := range vt.Items {
					if timeline.ClipAudioMode(it.Audio) != timeline.AudioModeBed {
						t.Errorf("%s: %q", it.ID, timeline.ClipAudioMode(it.Audio))
					}
				}
			}
		}
	})
	t.Run("one clip, with a trim, and the old spelling replaces the mode", func(t *testing.T) {
		out := mustApply(t, base, `[{"op":"set_clip_audio","id":"c1","mode":"full","gain_db":-2}]`)
		if got := modeOf(out, "c1"); got != timeline.AudioModeFull {
			t.Errorf("mode %q", got)
		}
		out = mustApply(t, out, `[{"op":"set_clip_audio","id":"c1","use_source":false}]`)
		if got := modeOf(out, "c1"); got != timeline.AudioModeMute {
			t.Errorf("after use_source false: %q", got)
		}
		out = mustApply(t, out, `[{"op":"set_clip_audio","id":"c1","mode":"bed"}]`)
		for _, tr := range out.Tracks {
			if vt, ok := tr.(*timeline.VideoTrack); ok && vt.Items[0].Audio.UseSource != nil {
				t.Error("use_source survived a mode")
			}
		}
	})
	t.Run("both, nothing and an unknown mode are refused", func(t *testing.T) {
		for _, op := range []Op{
			&SetClipAudio{ID: "c1", Mode: "bed", UseSource: ptr(true)},
			&SetClipAudio{ID: "c1"},
			&SetClipAudio{ID: "c1", Mode: "loud"},
			&SetClipAudio{ID: "c1", Mode: "bed;x"},
			&SetClipAudio{ID: "nope", Mode: "bed"},
		} {
			if _, res := Apply(base, []Op{op}, fixtureCatalog(t)); res.OK() {
				t.Errorf("%+v was accepted", op)
			}
		}
	})
	if timeline.ClipAudioMode(nil) != timeline.AudioModeMute || timeline.ClipAudioMode(&timeline.VideoAudio{UseSource: ptr(true)}) != timeline.AudioModeFull ||
		timeline.ClipAudioMode(&timeline.VideoAudio{}) != timeline.AudioModeMute {
		t.Error("the default modes")
	}
}
