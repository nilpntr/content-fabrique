package render

import (
	"regexp"
	"strconv"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

// A proof render (docs/design/proof-edit-v1.md) numbers every shot at the top right, inside the safe zone, from its first frame to the
// first frame of the next shot, in the order timeline.Shots numbers them; the preview and the final draw no numbers.
func TestAProofNumbersEveryShot(t *testing.T) {
	spec := specOf(30, []timeline.VideoItem{
		// Out of order in the track: the numbers follow the playing order.
		clipAt("c2", assetB, 0, 3000, 2000),
		clipAt("c1", assetA, 1000, 3000, 0),
		clipAt("c3", assetA, 5000, 7000, 5000),
	})
	proof := mustCompile(t, spec, Proof)
	if proof.Subtitles == nil || proof.Subtitles.ShotNumbers != 3 || !proof.Subtitles.Watermark || proof.Subtitles.TextEvents != 0 {
		t.Fatalf("proof subtitles %+v", proof.Subtitles)
	}
	if !proof.Profile.ShotNumbers || proof.Profile.Name != ProfilePreview {
		t.Errorf("the proof profile %+v", proof.Profile)
	}
	shots := timeline.Shots(spec)
	var numbers []dialogue
	for _, d := range parseDialogues(t, proof.Subtitles.ASS) {
		if d.style == "shot_number" {
			numbers = append(numbers, d)
		}
	}
	if len(numbers) != len(shots) {
		t.Fatalf("%d numbers for %d shots", len(numbers), len(shots))
	}
	zone, _ := spec.Output.Zone()
	pos := regexp.MustCompile(`\\an6\\pos\(([0-9.]+),([0-9.]+)\)\}(\d+)$`)
	for i, d := range numbers {
		s := shots[i]
		m := pos.FindStringSubmatch(d.text)
		if m == nil {
			t.Fatalf("number %d: text %q", i+1, d.text)
		}
		if m[3] != strconv.Itoa(s.Number) || d.layer != layerShotNumber {
			t.Errorf("number %d draws %q on layer %d", i+1, m[3], d.layer)
		}
		x, _ := strconv.ParseFloat(m[1], 64)
		y, _ := strconv.ParseFloat(m[2], 64)
		if x > float64(zone.Inner.Right) || x < float64(zone.Inner.Right)-100 || y < float64(zone.Inner.Top) || y > float64(zone.Inner.Top)+200 {
			t.Errorf("number %d at (%g, %g): not at the top right of the safe zone %+v", i+1, x, y, zone.Inner)
		}
		if got, want := firstFrameAt(t, d.start, 30), timeline.Frame(s.AtMs, 30); got != want {
			t.Errorf("number %d shows from frame %d, the shot starts on frame %d", i+1, got, want)
		}
		if got, want := firstFrameAt(t, d.end, 30), timeline.Frame(s.EndMs, 30); got != want {
			t.Errorf("number %d is gone on frame %d, the shot ends on frame %d", i+1, got, want)
		}
	}
	for _, prof := range Profiles() {
		if p := mustCompile(t, spec, prof); p.Subtitles != nil && p.Subtitles.ShotNumbers != 0 {
			t.Errorf("the %s profile draws shot numbers", prof.Name)
		}
	}
}

// Without the watermark the numbers take the corner itself; with it they go under it, and never over it.
func TestShotNumbersSitBelowTheWatermark(t *testing.T) {
	spec := specOf(30, []timeline.VideoItem{clipAt("c1", assetA, 0, 3000, 0)})
	bare := Proof
	bare.Watermark = false
	top := func(p Plan, st string) float64 {
		re := regexp.MustCompile(`\\pos\(([0-9.]+),([0-9.]+)\)`)
		for _, d := range parseDialogues(t, p.Subtitles.ASS) {
			if d.style == st {
				y, _ := strconv.ParseFloat(re.FindStringSubmatch(d.text)[2], 64)
				return y
			}
		}
		t.Fatalf("no %s event", st)
		return 0
	}
	alone, under := mustCompile(t, spec, bare), mustCompile(t, spec, Proof)
	zone, _ := spec.Output.Zone()
	// The em, which is what drawtext's fontsize sets: the creator's own proofs use 28 px.
	if em := style.ShotNumber().EmPx(spec.Output.ShortSide()); em < 26 || em > 30 {
		t.Errorf("the number's em is %g px at 1080 px: the creator's proofs use about 28", em)
	}
	if y := top(alone, "shot_number"); y < float64(zone.Inner.Top) || y > float64(zone.Inner.Top)+60 {
		t.Errorf("alone, the number's centre is at y %g; the zone starts at %d", y, zone.Inner.Top)
	}
	if top(under, "shot_number") <= top(under, "watermark")+20 {
		t.Errorf("the number (y %g) is not below the watermark (y %g)", top(under, "shot_number"), top(under, "watermark"))
	}
}
