package render

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/subject"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The b-roll proof of docs/design/broll-cutaways.md (slice 1): the speech under a cutaway is untouched (the plan's audio is byte-identical
// and the rendered PCM hash is equal, in both profiles), the captions follow main, the time map keeps its main entries, the cutaway is
// frame-exact (the golden plans and frames of render-broll.txt), and text keeps off the body that is in the picture.

var brollFixtures = []string{"v52_broll_9x16", "v53_broll_1x1", "v54_broll_4x5", "v55_broll_16x9"}

// withoutBroll is a copy of the spec without its b-roll track.
func withoutBroll(spec *timeline.Spec) *timeline.Spec {
	cp := *spec
	cp.Tracks = slices.DeleteFunc(slices.Clone(spec.Tracks), func(t timeline.Track) bool { return t.TrackType() == timeline.TrackBroll })
	return &cp
}

// audioStatements are the statements of the filter graph after the one that ends the video ([vout]): Compile writes the video graph and
// then the audio graph.
func audioStatements(t *testing.T, p Plan) []string {
	t.Helper()
	st := splitStatements(p.FilterComplex)
	for i, s := range st {
		if strings.HasSuffix(s, "[vout]") {
			return st[i+1:]
		}
	}
	t.Fatal("no [vout] in the graph")
	return nil
}

// audioOf is everything of a plan the audio is made from: the audio graph, the audio inputs, the loudness passes, the bed and the mix flags.
func audioOf(t *testing.T, p Plan) string {
	t.Helper()
	var audioInputs []Input
	for _, in := range p.Inputs {
		if in.Role == RoleAudio {
			audioInputs = append(audioInputs, in)
		}
	}
	b, err := json.Marshal(struct {
		Statements []string
		Inputs     []Input
		Loudnorm   *LoudnormPlan
		Bed        *BedPlan
		Flags      [3]any
	}{audioStatements(t, p), audioInputs, p.Loudnorm, p.Bed, [3]any{p.SoundReady, p.MusicLed, p.StemLabel}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// withSong is a copy of the spec with a licensed song under the speech, ducked by it (the music-led mix, D-34).
func withSong(spec *timeline.Spec) *timeline.Spec {
	cp := *spec
	gain := -6.0
	cp.Tracks = append(slices.Clone(spec.Tracks), &timeline.AudioTrack{ID: "sound", Items: []timeline.AudioItem{&timeline.LicensedTrack{
		AudioCommon: timeline.AudioCommon{ID: "s1", Export: timeline.ExportLicensed, GainDB: &gain},
		AssetID:     "ast_01j0h9rew727hk2pt0v5b31h44", LicenseID: "lic_01jg1bfdc146rw2b0vngmx6p6f",
	}}})
	return &cp
}

func TestCutawaysLeaveTheAudioPlanAlone(t *testing.T) {
	cat := testAssets(t)
	for _, name := range brollFixtures {
		for _, song := range []bool{false, true} {
			spec := loadFixture(t, name, cat)
			if song {
				spec = withSong(spec)
				if !strings.Contains(audioOf(t, compileFake(t, spec, Final, cat)), "sidechaincompress") {
					t.Fatalf("%s with a song: the song is not ducked under the speech", name)
				}
			}
			for _, prof := range Profiles() {
				with, err := Compile(spec, prof, fakeResolver(cat))
				if err != nil {
					t.Fatal(err)
				}
				without, err := Compile(withoutBroll(spec), prof, fakeResolver(cat))
				if err != nil {
					t.Fatal(err)
				}
				if a, b := audioOf(t, with), audioOf(t, without); a != b {
					t.Errorf("%s/%s: the audio plan changed with the cutaways\nwith:    %s\nwithout: %s", name, prof.Name, a, b)
				}
				// No audio pad is ever read from a cutaway's input, and each is opened with -an (the -i that follows an -an is the k-th
				// input, counted from the start).
				var opened []int
				for i, a := range with.Args {
					if a == "-i" {
						if with.Args[i-1] == "-an" {
							opened = append(opened, len(opened))
						} else {
							opened = append(opened, -1)
						}
					}
				}
				for _, in := range with.Inputs {
					if (in.Role == RoleBroll) != (in.Index < len(opened) && opened[in.Index] == in.Index) {
						t.Errorf("%s: input %d (%s) is opened with -an: %v, want %v", name, in.Index, in.Role, !(in.Role == RoleBroll), in.Role == RoleBroll)
					}
					if in.Role == RoleBroll && strings.Contains(with.FilterComplex, fmt.Sprintf("[%d:a]", in.Index)) {
						t.Errorf("%s: the graph reads the audio of cutaway input %d", name, in.Index)
					}
				}
				// The main entries of the time map, the cut list and the frame count are the same; the cutaways only add entries.
				var mainWith, cutsWith []TimeMapEntry
				for _, e := range with.TimeMap {
					if e.Track == TrackBroll {
						cutsWith = append(cutsWith, e)
					} else {
						mainWith = append(mainWith, e)
					}
				}
				if !reflect.DeepEqual(mainWith, without.TimeMap) || !reflect.DeepEqual(with.CutList, without.CutList) || with.TotalFrames != without.TotalFrames {
					t.Errorf("%s/%s: the main entries of the time map changed", name, prof.Name)
				}
				if len(cutsWith) != len(timeline.Cutaways(spec)) {
					t.Errorf("%s: %d cutaway entries for %d cutaways", name, len(cutsWith), len(timeline.Cutaways(spec)))
				}
				// Every output time still resolves to exactly one main item, a time under a cutaway included.
				for _, e := range cutsWith {
					for _, ms := range []int64{e.OutStartMs, (e.OutStartMs + e.OutEndMs) / 2, e.OutEndMs - 1} {
						got, _, ok := with.SourceTimeMs(ms)
						want, _, _ := without.SourceTimeMs(ms)
						if !ok || got.ItemID != want.ItemID || got.Track != "" {
							t.Errorf("%s: %d ms resolves to %+v, want main item %s", name, ms, got, want.ItemID)
						}
						if src, ok := e.SourceAtMs(float64(ms)); !ok || int64(src) != *e.SourceInMs+ms-e.OutStartMs {
							t.Errorf("%s: cutaway %s at %d ms is source %v", name, e.ItemID, ms, src)
						}
					}
				}
				// The captions and the text are drawn from the same script.
				if !reflect.DeepEqual(with.Subtitles, without.Subtitles) {
					t.Errorf("%s/%s: the subtitles changed with the cutaways", name, prof.Name)
				}
			}
		}
	}
}

func compileFake(t testing.TB, spec *timeline.Spec, prof Profile, cat timeline.MemoryCatalog) Plan {
	t.Helper()
	p, err := Compile(spec, prof, fakeResolver(cat))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestCutawayIsFrameExact: each cutaway is its own input, seeked to its in point for exactly its span, played into exactly the frames it
// owns and shifted to its first frame; and the time map says the same frames.
func TestCutawayIsFrameExact(t *testing.T) {
	cat := testAssets(t)
	spec := loadFixture(t, "v54_broll_4x5", cat) // 25 fps, a cutaway at 333 ms (frame 8.3)
	plan, err := Compile(spec, Preview, fakeResolver(cat))
	if err != nil {
		t.Fatal(err)
	}
	// b1: 333 to 1033 ms at 25 fps is frames 8 to 26 (Frame rounds half up): 18 frames from 320 ms.
	for _, want := range []string{
		"trim=end=0.700,setpts=PTS-STARTPTS,fps=25,tpad=stop=-1:stop_mode=clone,trim=end_frame=18,",
		"setpts=PTS+0.320000/TB[b0]",
		"[vj][b0]overlay=0:0:eof_action=pass:repeatlast=0[vb0]",
		"[vb0][b1]overlay=0:0:eof_action=pass:repeatlast=0[vc]",
	} {
		if !strings.Contains(plan.FilterComplex, want) {
			t.Errorf("the graph has no %q:\n%s", want, PrettyFilter(plan.FilterComplex))
		}
	}
	e := plan.TimeMap[len(plan.TimeMap)-2]
	if e.ItemID != "b1" || e.OutStartMs != 320 || e.OutEndMs != 1040 || *e.SourceInMs != 70000 || e.Track != TrackBroll {
		t.Errorf("time map entry %+v", e)
	}
}

// TestCaptionsFollowMainUnderACutaway is the caption sync: with a captions track the words, their times and their places are those of
// main's transcript, cutaway or not.
func TestCaptionsFollowMainUnderACutaway(t *testing.T) {
	cat := testAssets(t)
	for _, name := range brollFixtures {
		spec := loadFixture(t, name, cat)
		spec.Tracks = append(spec.Tracks, &timeline.CaptionsTrack{ID: "captions", From: "transcript", Style: "chunk", Enabled: true})
		for _, prof := range Profiles() {
			with, err := Compile(spec, prof, fakeResolver(cat))
			if err != nil {
				t.Fatal(err)
			}
			without, err := Compile(withoutBroll(spec), prof, fakeResolver(cat))
			if err != nil {
				t.Fatal(err)
			}
			if with.Subtitles == nil || with.Subtitles.CaptionEvents == 0 {
				t.Fatalf("%s: no captions", name)
			}
			if with.Subtitles.ASS != without.Subtitles.ASS {
				t.Errorf("%s/%s: the captions differ under the cutaways", name, prof.Name)
			}
		}
	}
}

// TestTextKeepsOffTheCutawaysSubject: while a cutaway is on screen the title placement reads the subject of the cutaway's asset, not of
// the clip under it. A title at the top over a clip with nobody in it moves off the head of the person in the cutaway; the same title
// without the cutaway stays where it is.
func TestTextKeepsOffTheCutawaysSubject(t *testing.T) {
	const person = "ast_01jdsx72atvnb7ssgbc57zbxj9"
	tr := subject.Track{Aspect: 0.5625, Samples: []subject.Sample{{AtMs: 2500, X: 0.2, Y: 0.2, W: 0.6, H: 0.7, HeadTopY: 0.22}}}
	res := ResolverFunc(func(id string, _ Variant) (ResolvedAsset, error) {
		ra := ResolvedAsset{Location: "/tmp/" + id + ".mp4", HasAudio: true}
		if id == person {
			ra.Subject = &tr
		}
		return ra, nil
	})
	spec := loadFixture(t, "v52_broll_9x16", testAssets(t)) // b1 shows the person from 2.4 to 3.6 s
	spec.Tracks = append(spec.Tracks, &timeline.TextTrack{ID: "text", Items: []timeline.TextItem{
		{ID: "t1", AtMs: 2500, DurationMs: 900, Text: "AAA\nBBB", Style: "title_series", Anchor: "upper_third"},
	}})
	with, err := Compile(spec, Final, res)
	if err != nil {
		t.Fatal(err)
	}
	without, err := Compile(withoutBroll(spec), Final, res)
	if err != nil {
		t.Fatal(err)
	}
	_, yWith := eventAt(t, with.Subtitles.ASS, "AAA")
	_, yWithout := eventAt(t, without.Subtitles.ASS, "AAA")
	if yWith >= yWithout-30 {
		t.Errorf("the title stayed on the cutaway's subject: y %v with the cutaway, %v without", yWith, yWithout)
	}
}

// TestCompileRefusesCutawaysItCannotDraw: the validator owns the rules; Compile still refuses a cutaway it could not draw.
func TestCompileRefusesCutawaysItCannotDraw(t *testing.T) {
	cat := testAssets(t)
	for name, mutate := range map[string]func(b *timeline.BrollTrack){
		"empty range":   func(b *timeline.BrollTrack) { b.Items[0].Source.OutMs = b.Items[0].Source.InMs },
		"past the end":  func(b *timeline.BrollTrack) { b.Items[1].AtMs = 5500 },
		"share a frame": func(b *timeline.BrollTrack) { b.Items[1].AtMs = 3500 },
		"too many": func(b *timeline.BrollTrack) {
			for len(b.Items) <= timeline.MaxCutaways {
				b.Items = append(b.Items, b.Items[0])
			}
		},
	} {
		spec := loadFixture(t, "v52_broll_9x16", cat)
		bt, i, _ := timeline.BrollTrackOf(spec)
		cp := *bt
		cp.Items = slices.Clone(bt.Items)
		mutate(&cp)
		spec.Tracks[i] = &cp
		if _, err := Compile(spec, Preview, fakeResolver(cat)); err == nil {
			t.Errorf("%s: compiled", name)
		}
	}
}

// FuzzCutaways places random cutaways on a speech edit with a song ducked under it: whatever the validator accepts compiles, and its
// audio plan and main time map are those of the edit without them.
func FuzzCutaways(f *testing.F) {
	f.Add([]byte{0, 10, 40, 3, 120, 9})
	f.Add([]byte{1, 0, 255, 2, 7, 7, 7, 7})
	f.Add([]byte{3, 200, 3, 1, 1, 30, 2, 2, 200, 100})
	cat := testAssets(f)
	base := withSong(loadFixture(f, "v55_broll_16x9", cat))
	assets := sortedAssetIDs(cat)
	f.Fuzz(func(t *testing.T, data []byte) {
		var items []timeline.BrollItem
		for k := 0; k+3 < len(data) && len(items) < timeline.MaxCutaways+1; k += 4 {
			asset := assets[int(data[k])%len(assets)]
			in := int64(data[k+1]) * 150
			span := 100 + int64(data[k+2])*12
			at := int64(data[k+3]) * 25
			items = append(items, timeline.BrollItem{ID: fmt.Sprintf("b%d", len(items)+1), AssetID: asset, Source: timeline.Source{InMs: in, OutMs: in + span}, AtMs: at})
		}
		spec := withoutBroll(base)
		spec.Tracks = append(spec.Tracks, &timeline.BrollTrack{ID: timeline.BrollTrackID, Items: items})
		doc, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		if !timeline.Validate(doc, cat).OK() {
			return
		}
		for _, prof := range Profiles() {
			with, err := Compile(spec, prof, fakeResolver(cat))
			if err != nil {
				t.Fatalf("a valid spec did not compile: %v\n%s", err, doc)
			}
			without, err := Compile(withoutBroll(spec), prof, fakeResolver(cat))
			if err != nil {
				t.Fatal(err)
			}
			if audioOf(t, with) != audioOf(t, without) {
				t.Fatalf("the audio plan changed:\n%s", doc)
			}
			if len(with.TimeMap) != len(without.TimeMap)+len(items) || !reflect.DeepEqual(with.TimeMap[:len(without.TimeMap)], without.TimeMap) {
				t.Fatalf("the main time map changed:\n%s", doc)
			}
		}
	})
}

// pcmHash decodes the audio of a rendered file to raw 48 kHz stereo PCM and hashes it.
func pcmHash(t *testing.T, ffmpeg, file string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), ffmpeg, "-hide_banner", "-loglevel", "error", "-i", file, "-map", "0:a", "-c:a", "pcm_s16le", "-f", "s16le", "-") //nolint:gosec // test helper
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("decode audio: %v\n%s", err, stderr.String())
	}
	if stdout.Len() == 0 {
		t.Fatal("no audio decoded")
	}
	sum := sha256.Sum256(stdout.Bytes())
	return hex.EncodeToString(sum[:])
}

// TestCutawaysLeaveTheSoundAlone renders each b-roll fixture with and without its cutaways, in both profiles: the decoded audio is the
// same to the sample (the speech keeps playing under a cutaway, the song is ducked the same way), and the picture is not.
func TestCutawaysLeaveTheSoundAlone(t *testing.T) {
	ffmpeg, _ := ffmpegTools(t)
	dir, cat := ensureMedia(t)
	resolver := mediaResolver(dir, cat)
	for _, name := range brollFixtures {
		spec := loadFixture(t, name, cat)
		for _, prof := range Profiles() {
			t.Run(name+"/"+string(prof.Name), func(t *testing.T) {
				t.Parallel()
				toolSem <- struct{}{}
				defer func() { <-toolSem }()
				hashes := map[bool]string{}
				frames := map[bool]image.Image{}
				for _, broll := range []bool{true, false} {
					s := spec
					if !broll {
						s = withoutBroll(spec)
					}
					plan, err := Compile(s, prof, resolver)
					if err != nil {
						t.Fatal(err)
					}
					out := filepath.Join(t.TempDir(), "out.mp4")
					if err := Execute(context.Background(), plan, testRunner(t), ExecuteOptions{Output: out}); err != nil {
						t.Fatal(err)
					}
					hashes[broll] = pcmHash(t, ffmpeg, out)
					cw := timeline.Cutaways(spec)[0]
					mid := timeline.Frame(cw.AtMs+cw.DurationMs()/2, spec.Output.FPS)
					frames[broll] = extractFrames(t, ffmpeg, out, []int64{mid}, goldenWidth)[mid]
				}
				if hashes[true] != hashes[false] {
					t.Errorf("the decoded audio differs with the cutaways: %s against %s", hashes[true], hashes[false])
				}
				if ssim(frames[true], frames[false]) >= minSSIM {
					t.Error("the frame in the middle of the first cutaway is the same without it: the cutaway is not drawn")
				}
			})
		}
	}
}
