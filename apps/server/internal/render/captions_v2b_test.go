package render

import (
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

// A fix remembers the word it was made for: when the transcript has another word there now, the fix is skipped and the renderer says so.
func TestAStaleCorrectionIsSkipped(t *testing.T) {
	words := fixtureTranscript(captionAsset, 6000)
	heard := strings.Trim(words[2].Text, ".")
	mk := func(was string) Plan {
		caps := &timeline.CaptionsTrack{Style: "chunk", MaxWords: ptr(1), Corrections: []timeline.CaptionCorrection{{AssetID: captionAsset, Word: 2, Text: "Creatine", Was: was}}}
		plan, err := Compile(talkingSpec(timeline.Format9x16, caps), Final, captionResolver(nil))
		if err != nil {
			t.Fatal(err)
		}
		return plan
	}
	if fresh := mk(words[2].Text); !strings.Contains(fresh.Subtitles.ASS, "CREATINE") || len(fresh.Warnings) != 0 {
		t.Errorf("a fix with the right word was not applied: %+v", fresh.Warnings)
	}
	stale := mk("something else")
	if strings.Contains(stale.Subtitles.ASS, "CREATINE") || !strings.Contains(strings.ToUpper(stale.Subtitles.ASS), strings.ToUpper(heard)) {
		t.Error("a stale fix was applied")
	}
	var warned bool
	for _, w := range stale.Warnings {
		warned = warned || w.Code == "correction_stale"
	}
	if !warned {
		t.Errorf("warnings %+v", stale.Warnings)
	}
}

// Captions never span a cut: each clip's captions use that clip's band, so a face in the lower band of one clip moves only its captions.
func TestCaptionBandsFollowTheClip(t *testing.T) {
	spec := talkingSpec(timeline.Format9x16, &timeline.CaptionsTrack{Style: "chunk"})
	main, ok := spec.Tracks[0].(*timeline.VideoTrack)
	if !ok {
		t.Fatal("no main track")
	}
	main.Items = []timeline.VideoItem{
		{ID: "c1", AssetID: captionAsset, Source: timeline.Source{InMs: 0, OutMs: 3000}, AtMs: 0},
		{ID: "c2", AssetID: "ast_01jex6xx15ayjx8epfhfjj9s11", Source: timeline.Source{InMs: 0, OutMs: 3000}, AtMs: 3000},
	}
	zone, _ := spec.Output.Zone()
	st, _ := style.CaptionStyle("chunk")
	lower := style.CaptionBand(zone, 1080, 1080, st)
	faceLow := speakerAt(0.5625, float64(lower.Top-20)/1920, float64(lower.Bottom+20)/1920)
	res := ResolverFunc(func(id string, _ Variant) (ResolvedAsset, error) {
		ra := ResolvedAsset{Location: "/tmp/" + id + ".mp4", HasAudio: true, Transcript: fixtureTranscript(id, 3000)}
		if id == captionAsset {
			ra.Subject = faceLow
		}
		return ra, nil
	})
	plan, err := Compile(spec, Final, res)
	if err != nil {
		t.Fatal(err)
	}
	events, ys := captionEvents(t, plan.Subtitles.ASS)
	var up, low int
	for i, d := range events {
		inUpper := int(ys[i]) < lower.Top
		if d.start < 290 && !inUpper { // centiseconds: before 3 s
			t.Errorf("a caption of c1 at %d cs is in the lower band: %q y %v", d.start, d.text, ys[i])
		}
		if d.start >= 310 && inUpper {
			t.Errorf("a caption of c2 at %d cs is in the upper band", d.start)
		}
		if inUpper {
			up++
		} else {
			low++
		}
	}
	if up == 0 || low == 0 {
		t.Errorf("%d upper, %d lower", up, low)
	}
}
