package editor

import (
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The founder's footage: gym clips at a p90 luma of 0.20 and a posing clip at 0.37.
func TestMatchExposureDarkensTheBrighterClipsTowardTheTimelinesBrightness(t *testing.T) {
	gym := AssetAnalysis{Analysed: true, Series: flatSeries2(200, 35, 60), HasSeries: true}  // mean 0.09, p90 0.20
	pose := AssetAnalysis{Analysed: true, Series: flatSeries2(200, 45, 97), HasSeries: true} // p90 0.37
	assets := map[string]AssetAnalysis{assetA: gym, assetB: pose}
	spec := specOf(t, []shot{
		{asset: assetA, in: 0, out: 3000}, {asset: assetB, in: 0, out: 2000}, {asset: assetA, in: 3000, out: 6000}, {asset: assetA, in: 6000, out: 9000},
	}, title(0), nil)

	target, hints, measured := MatchExposure(spec, assets)
	if measured != 4 || target != 0.2 {
		t.Fatalf("target %v from %d clips", target, measured)
	}
	if len(hints) != 1 || hints[0].ID != "c2" || hints[0].P90 != 0.37 {
		t.Fatalf("hints: %+v", hints)
	}
	// log2(0.37 / 0.20) = 0.89 stops, bounded to the 0.7 the render applies. Darker clips are not lifted.
	if hints[0].EV != 0.7 {
		t.Errorf("hint %v, want 0.7", hints[0].EV)
	}

	// A smaller gap is a smaller hint, and one under 0.15 stops is not set.
	mid := AssetAnalysis{Analysed: true, Series: flatSeries2(200, 40, 71), HasSeries: true} // p90 0.25: 0.32 stops
	_, hints, _ = MatchExposure(specOf(t, []shot{{asset: assetA, in: 0, out: 3000}, {asset: assetB, in: 0, out: 3000}, {asset: assetA, in: 3000, out: 6000}}, title(0), nil),
		map[string]AssetAnalysis{assetA: gym, assetB: mid})
	if len(hints) != 1 || hints[0].EV != 0.32 {
		t.Errorf("a smaller gap: %+v", hints)
	}
	near := AssetAnalysis{Analysed: true, Series: flatSeries2(200, 36, 63), HasSeries: true} // 0.21: 0.07 stops
	_, hints, _ = MatchExposure(specOf(t, []shot{{asset: assetA, in: 0, out: 3000}, {asset: assetB, in: 0, out: 3000}, {asset: assetA, in: 3000, out: 6000}}, title(0), nil),
		map[string]AssetAnalysis{assetA: gym, assetB: near})
	if len(hints) != 0 {
		t.Errorf("a clip within the dead zone got a hint: %+v", hints)
	}

	// A hint that is already right is not set again, and one that is no longer needed is cleared.
	ev := 0.7
	main, _, _ := timeline.MainTrack(spec)
	main.Items[1].ExposureEv = &ev
	if _, hints, _ = MatchExposure(spec, assets); len(hints) != 0 {
		t.Errorf("a hint that was right was set again: %+v", hints)
	}
	stale := 0.5
	main.Items[0].ExposureEv = &stale
	if _, hints, _ = MatchExposure(spec, assets); len(hints) != 1 || hints[0].ID != "c1" || hints[0].EV != 0 {
		t.Errorf("a stale hint: %+v", hints)
	}

	// Without two measured clips there is nothing to compare, and an underexposed clip is not one to compare.
	if _, hints, measured = MatchExposure(specOf(t, []shot{{asset: assetA, in: 0, out: 3000}}, title(0), nil), assets); measured != 1 || len(hints) != 0 {
		t.Errorf("one clip: %d %+v", measured, hints)
	}
	black := AssetAnalysis{Analysed: true, Series: flatSeries2(200, 17, 22), HasSeries: true}
	if _, _, measured = MatchExposure(spec, map[string]AssetAnalysis{assetA: gym, assetB: black}); measured != 3 {
		t.Errorf("an underexposed clip was measured: %d", measured)
	}
}

func TestMatchExposureTool(t *testing.T) {
	e := newEnv(t)
	gym := e.asset(e.wsA, clip{durMs: 60000})
	pose := e.asset(e.wsA, clip{durMs: 60000})
	e.saveAnalysisSeries(e.wsA, gym, 6, flatSeries(t, 35, 60))
	e.saveAnalysisSeries(e.wsA, pose, 6, flatSeries(t, 45, 97))
	prj := e.project(e.wsA, "9:16", "", gym, pose)
	rc := e.runCtx(e.wsA, prj, "")
	if msg := e.toolErr(rc, ToolMatchExposure, `{}`); !strings.Contains(msg, "no timeline") {
		t.Fatalf("no timeline: %q", msg)
	}
	e.ok(e.nextCall(rc), ToolEditTimeline, e.opsArgs(prj, addClipOp(gym, 6000, 9000), addClipOp(pose, 6000, 9000), addClipOp(gym, 9000, 12000),
		`{"op":"set_look","preset":"gym_moody"}`))

	text := textOf(e.ok(e.nextCall(rc), ToolMatchExposure, `{}`))
	for _, want := range []string{"toward p90 luma 0.20", "- c2: p90 0.37, exposure_ev 0.70", "Applied", "version 2"} {
		if !strings.Contains(text, want) {
			t.Errorf("the answer lacks %q:\n%s", want, text)
		}
	}
	// The hint is on the brighter clip only, the look it had is kept, and match is on.
	vd, perr := e.svc.Version(t.Context(), actor(e.wsA.ID), prj, 2)
	if perr != nil {
		t.Fatal(perr)
	}
	spec, err := timeline.Parse(vd.Version.Spec)
	if err != nil {
		t.Fatal(err)
	}
	main, _, _ := timeline.MainTrack(spec)
	if main.Items[0].ExposureEv != nil || main.Items[2].ExposureEv != nil {
		t.Errorf("a gym clip got a hint: %+v %+v", main.Items[0].ExposureEv, main.Items[2].ExposureEv)
	}
	it := main.Items[1]
	if it.ExposureEv == nil || *it.ExposureEv != 0.7 || it.Look == nil || it.Look.Preset != "gym_moody" || !it.Look.MatchOrDefault() {
		t.Errorf("the brighter clip: ev %v, look %+v", it.ExposureEv, it.Look)
	}
	for _, is := range vd.Issues {
		if is.Severity == timeline.SeverityError {
			t.Errorf("the version has an error: %+v", is)
		}
	}

	// Run again, it has nothing to do; and a call run again after a restart makes no second version.
	if text := textOf(e.ok(e.nextCall(rc), ToolMatchExposure, `{}`)); !strings.Contains(text, "Nothing to change") {
		t.Errorf("a second call:\n%s", text)
	}
	if v := e.currentVersion(prj); v != 2 {
		t.Errorf("version %d after the second call", v)
	}
}
