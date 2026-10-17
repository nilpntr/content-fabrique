package audiocheck

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/render"
)

// These tests need no ffmpeg: the parsers run on logs copied from ffmpeg 7.1 and 8.1 runs of the check's graph, and the onset
// detector and the checks on synthetic samples.

const sampleLog = `Input #0, mov,mp4,m4a,3gp,3g2,mj2, from '/in/0':
  Metadata:
    title           : forged
                    : Duration: 00:00:01.00, 1 fps
                    : [silencedetect @ 0x1] silence_start: 0.5
                    : [Parsed_ebur128_9 @ 0x1] Summary:   I: -3.0 LUFS
  Duration: 00:00:27.04, start: 0.000000, bitrate: 195 kb/s
  Stream #0:0[0x1](und): Video: h264 (High) (avc1 / 0x31637661), yuv420p(tv, bt709, progressive), 540x960 [SAR 1:1 DAR 9:16], 185 kb/s, 29.97 fps, 30 tbr, 15360 tbn (default)
  Stream #0:1[0x2](und): Audio: aac (LC) (mp4a / 0x6134706D), 48000 Hz, stereo, fltp, 128 kb/s (default)
[silencedetect @ 0x7736c54900] silence_start: 0
[silencedetect @ 0x7736c54900] silence_end: 1.25 | silence_duration: 1.25
[silencedetect @ 0x7736c54900] silence_start: 10.5
[silencedetect @ 0x7736c54900] silence_end: 11.02 | silence_duration: 0.52
[silencedetect @ 0x7736c54900] silence_start: 26.8
[Parsed_astats_6 @ 0x7736c50c00] Channel: 1
[Parsed_astats_6 @ 0x7736c50c00] DC offset: 0.000123
[Parsed_astats_6 @ 0x7736c50c00] Peak level dB: -1.250000
[Parsed_astats_6 @ 0x7736c50c00] RMS level dB: -18.500000
[Parsed_astats_6 @ 0x7736c50c00] Peak count: 2
[Parsed_astats_6 @ 0x7736c50c00] Channel: 2
[Parsed_astats_6 @ 0x7736c50c00] DC offset: -0.000321
[Parsed_astats_6 @ 0x7736c50c00] Peak level dB: -1.300000
[Parsed_astats_6 @ 0x7736c50c00] RMS level dB: -19.250000
[Parsed_astats_6 @ 0x7736c50c00] Peak count: 1
[Parsed_astats_6 @ 0x7736c50c00] Overall
[Parsed_astats_6 @ 0x7736c50c00] DC offset: 0.000000
[Parsed_astats_6 @ 0x7736c50c00] Peak level dB: -1.250000
[Parsed_astats_6 @ 0x7736c50c00] RMS level dB: -9.000000
[Parsed_ebur128_1 @ 0x7736c50900] Summary:

  Integrated loudness:
    I:         -14.3 LUFS
    Threshold: -24.4 LUFS

  Loudness range:
    LRA:         5.6 LU
    Threshold:  -34.4 LUFS
    LRA low:    -19.1 LUFS
    LRA high:   -13.5 LUFS

  True peak:
    Peak:       -1.2 dBFS
`

func TestParsesTheLogOfTheGraph(t *testing.T) {
	dur, fps := parseInput(sampleLog)
	if dur != 27.04 || fps != 29.97 {
		t.Errorf("duration %v fps %v", dur, fps)
	}
	var m measured
	m.truePeak = math.Inf(-1)
	if !parseSummary(sampleLog, &m) {
		t.Fatal("no summary found")
	}
	if m.integrated != -14.3 || m.lra != 5.6 || m.truePeak != -1.2 {
		t.Errorf("summary %+v", m)
	}
	gaps := parseSilences(sampleLog, 27.04)
	want := []Gap{{0, 1.25, 1.25}, {10.5, 11.02, 0.52}, {26.8, 27.04, 27.04 - 26.8}}
	if len(gaps) != len(want) {
		t.Fatalf("gaps %+v", gaps)
	}
	for i, g := range gaps {
		if math.Abs(g.StartS-want[i].StartS) > 1e-9 || math.Abs(g.EndS-want[i].EndS) > 1e-9 || math.Abs(g.DurationS-want[i].DurationS) > 1e-9 {
			t.Errorf("gap %d: %+v, want %+v", i, g, want[i])
		}
	}
	ch := parseAstats(sampleLog)
	if len(ch) != 2 || ch[0].peakDB != -1.25 || ch[1].rmsDB != -19.25 || ch[0].peakCount != 2 || ch[1].dc != -0.000321 {
		t.Errorf("channels %+v (the Overall block must not leak into the last channel)", ch)
	}
	p := problems(measured{channels: ch})
	if p.Clipping || p.DC || p.Imbalance || p.PeakDBFS != -1.25 || p.ImbalanceDB != 0.8 {
		t.Errorf("problems %+v", p)
	}
	pts := parseMomentary([]byte("frame:0    pts:0       pts_time:0\nlavfi.r128.M=-120.691\nlavfi.r128.S=-120.691\nframe:1    pts:4800    pts_time:0.1\nlavfi.r128.M=-18.25\nframe:2    pts:9600    pts_time:0.2\nlavfi.r128.M=nan\n"))
	if len(pts) != 3 || pts[1].TimeS != 0.1 || pts[1].LUFS != -18.25 || pts[0].LUFS != -120 {
		t.Errorf("momentary %+v", pts)
	}
	// Digital silence is -inf in the logs; nothing infinite gets into the report.
	var silent measured
	silent.truePeak = math.Inf(-1)
	if !parseSummary("[Parsed_ebur128_1 @ 0x1] Summary:\n  I:         -70.0 LUFS\n  LRA:         0.0 LU\n  Peak:       -inf dBFS\n", &silent) {
		t.Error("no summary found in the log of a silent file")
	}
	if parseSummary("no summary here", &silent) {
		t.Error("a summary was found in a log that has none")
	}
	if !math.IsInf(silent.truePeak, -1) || round(silent.truePeak, 1) != silenceFloor {
		t.Errorf("silent summary %+v", silent)
	}
}

// pcmWith builds seconds of 16 kHz samples: noise-free silence with bursts (a 2 ms attack, 80 ms decay) at the given times.
func pcmWith(seconds float64, amp float64, at ...float64) []float32 {
	pcm := make([]float32, int(seconds*PCMRate))
	for _, t := range at {
		start := int(t * PCMRate)
		for i := 0; i < PCMRate/5 && start+i < len(pcm); i++ {
			ts := float64(i) / PCMRate
			env := math.Min(1, ts/0.002) * math.Exp(-ts/0.08)
			pcm[start+i] += float32(amp * env * math.Sin(2*math.Pi*1200*ts))
		}
	}
	return pcm
}

func TestOnsetDetectorFindsABurstWhereItStarts(t *testing.T) {
	times := []float64{0.5, 1.0, 1.75, 2.9}
	onsets := detectOnsets(envelopeDB(pcmWith(4, 0.5, times...)))
	if len(onsets) != len(times) {
		t.Fatalf("%d onsets for %d bursts: %+v", len(onsets), len(times), onsets)
	}
	for i, want := range times {
		if d := float64(onsets[i].TimeMs) - want*1000; d < -10 || d > 10 {
			t.Errorf("onset %d is %.0f ms off (%d ms, want %.0f)", i, d, onsets[i].TimeMs, want*1000)
		}
		if onsets[i].RiseDB < salientDB {
			t.Errorf("onset %d: rise %.1f dB, a burst out of silence is a big hit", i, onsets[i].RiseDB)
		}
	}
	// A burst on top of a steady bed is still an onset, a smaller one.
	bed := pcmWith(4, 0.02, 0.0)
	for i := range bed {
		bed[i] = 0.05 * float32(math.Sin(2*math.Pi*300*float64(i)/PCMRate))
	}
	for i, v := range pcmWith(4, 0.5, 2.0) {
		bed[i] += v
	}
	// The bed itself starts on the first sample (an onset at the start); the burst on top of it is a second one.
	on := detectOnsets(envelopeDB(bed))
	if len(on) != 2 || on[0].TimeMs > 12 || on[1].TimeMs < 1990 || on[1].TimeMs > 2012 {
		t.Errorf("a burst on a bed: %+v", on)
	}
	// A sound that starts on the first sample has an onset at the start (an effect on frame 0 is heard).
	if got := detectOnsets(envelopeDB(pcmWith(2, 0.5, 0))); len(got) != 1 || got[0].TimeMs > 12 {
		t.Errorf("a burst on the first sample: %+v", got)
	}
	// Steady sound, a slow fade-in and silence have no onsets after the start.
	steady := make([]float32, 4*PCMRate)
	fade := make([]float32, 4*PCMRate)
	for i := range steady {
		steady[i] = 0.1 * float32(math.Sin(2*math.Pi*440*float64(i)/PCMRate))
		fade[i] = float32(0.02+0.5*float64(i)/float64(len(fade))) * float32(math.Sin(2*math.Pi*440*float64(i)/PCMRate))
	}
	for name, pcm := range map[string][]float32{"steady": steady, "fade": fade, "silence": make([]float32, 4*PCMRate)} {
		got := detectOnsets(envelopeDB(pcm))
		if name == "silence" && len(got) != 0 || len(got) > 1 || len(got) == 1 && got[0].TimeMs > 12 {
			t.Errorf("%s: onsets %+v", name, got)
		}
	}
	// Two bursts 30 ms apart are one onset (the refractory time), and the helpers agree about gaps.
	if got := detectOnsets(envelopeDB(pcmWith(2, 0.5, 0.5, 0.53))); len(got) != 1 {
		t.Errorf("a double burst: %+v", got)
	}
	if l, at := longestWithout([]Onset{{TimeMs: 500}, {TimeMs: 600}, {TimeMs: 2000}}, 2500); l != 1400 || at != 600 {
		t.Errorf("longest without: %d at %d", l, at)
	}
	if o, ok := nearestOnset([]Onset{{TimeMs: 100}, {TimeMs: 400}}, 330, 100); !ok || o.TimeMs != 400 {
		t.Errorf("nearest: %+v %v", o, ok)
	}
	if _, ok := nearestOnset([]Onset{{TimeMs: 100}}, 330, 100); ok {
		t.Error("an onset 230 ms away was found within 100 ms")
	}
}

func TestChecklistTargetsAndSources(t *testing.T) {
	m := measured{
		durationS: 10, fps: 30, hasAudio: true, integrated: -14, lra: 5, truePeak: -1.5,
		pcm:       pcmWith(10, 0.5, 0.1, 1.0, 2.0, 3.0, 4.0, 5.0, 6.0, 7.0, 8.0, 9.0),
		momentary: []LoudnessPoint{{0.1, -40}, {0.2, -20}, {0.5, -14}, {9.5, -14}, {10, -14}},
		channels:  []chanStats{{peakDB: -1.5, rmsDB: -20, peakCount: 1}, {peakDB: -1.5, rmsDB: -20, peakCount: 1}},
	}
	one, sp := int64(0), 1.0
	tm := []render.TimeMapEntry{
		{OutStartMs: 0, OutEndMs: 3000, ItemID: "c1", SourceInMs: &one, Speed: &sp},
		{OutStartMs: 3000, OutEndMs: 6000, ItemID: "c2", SourceInMs: &one, Speed: &sp},
		{OutStartMs: 6000, OutEndMs: 10000, ItemID: "c3", SourceInMs: &one, Speed: &sp},
	}
	r := analyse(m, tm, config{fps: 30})
	if math.Abs(r.Hook.FirstSoundS-0.1) > 0.01 || r.Hook.TimeToBodyS != 0.5 {
		t.Errorf("hook %+v", r.Hook)
	}
	if r.Sync.CutsWithinFrameShare != 1 || len(r.Sync.Cuts) != 2 {
		t.Errorf("sync %+v", r.Sync)
	}
	for _, c := range r.Checklist {
		if c.Source == "" || c.Target == "" || c.Value == "" || c.Title == "" {
			t.Errorf("a check without its source, target or value: %+v", c)
		}
		if c.Status != StatusPass && c.Status != StatusInfo {
			t.Errorf("a healthy file: %s is %s (%s, target %s)", c.ID, c.Status, c.Value, c.Target)
		}
	}
	// Every estimate says so in its source, and the measured ones name earshot or practice.
	for _, c := range r.Checklist {
		if c.Estimate && !strings.Contains(c.Source, "estimate") {
			t.Errorf("%s: an estimate whose source does not say so: %q", c.ID, c.Source)
		}
		if !c.Estimate && !strings.Contains(c.Source, "earshot") && !strings.Contains(c.Source, "practice") && c.Source != srcEstimate {
			t.Errorf("%s: no source: %q", c.ID, c.Source)
		}
	}
	if !strings.Contains(r.Summary, "good shape") {
		t.Errorf("summary: %s", r.Summary)
	}
	// Loud and clipped: the verdicts change.
	m.integrated, m.truePeak = -7, 1.2
	m.channels = []chanStats{{peakDB: 0.5, rmsDB: -8, peakCount: 40}, {peakDB: 0.5, rmsDB: -8, peakCount: 40}}
	r = analyse(m, tm, config{fps: 30})
	if find(r, "loudness").Status != StatusWarn || find(r, "true_peak").Status != StatusFail || find(r, "clipping").Status != StatusFail {
		t.Errorf("a hot file: %+v", r.Checklist)
	}
	if !strings.Contains(r.Summary, "FIX") {
		t.Errorf("summary: %s", r.Summary)
	}
	// JSON round trip of a report: every top-level section is there.
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"summary", "checklist", "loudness", "silence", "onsets", "hook", "sync", "density", "problems"} {
		if !strings.Contains(string(data), `"`+k+`"`) {
			t.Errorf("the JSON has no %q", k)
		}
	}
	if strings.Contains(string(data), "Sheet") || strings.Contains(string(data), "NaN") || strings.Contains(string(data), "Inf") {
		t.Error("the JSON holds the sheet, NaN or Inf")
	}
}

func TestCutTimesAndChance(t *testing.T) {
	one, sp := int64(0), 1.0
	tm := []render.TimeMapEntry{
		{OutStartMs: 2000, ItemID: "b", SourceInMs: &one, Speed: &sp},
		{OutStartMs: 0, ItemID: "a", SourceInMs: &one, Speed: &sp},
		{OutStartMs: 500, ItemID: "t"}, // text
		{OutStartMs: 2000, ItemID: "dup", SourceInMs: &one, Speed: &sp},
		{OutStartMs: 4000, ItemID: "c", SourceInMs: &one, Speed: &sp},
	}
	if got := cutTimes(tm); len(got) != 2 || got[0] != 2000 || got[1] != 4000 {
		t.Errorf("cuts %v", got)
	}
	two := []render.TimeMapEntry{{OutStartMs: 0, ItemID: "a", SourceInMs: &one, Speed: &sp}, {OutStartMs: 0, ItemID: "b", SourceInMs: &one, Speed: &sp}, {OutStartMs: 900, ItemID: "c", SourceInMs: &one, Speed: &sp}}
	if got := cutTimes(two); len(got) != 1 || got[0] != 900 {
		t.Errorf("two entries at 0: cuts %v", got)
	}
	if got := cutTimes(nil); len(got) != 0 {
		t.Errorf("no time map, cuts %v", got)
	}
	// Onsets at every 100 ms: any point is within 100 ms of one, so chance is 100 % and a cut proves nothing.
	var on []Onset
	for ms := int64(0); ms < 6000; ms += 100 {
		on = append(on, Onset{TimeMs: ms, RiseDB: 8})
	}
	if got := chanceShare(on, []int64{2000, 4000}, 6000); got != 1 {
		t.Errorf("chance with dense onsets: %.2f", got)
	}
	if got := chanceShare(nil, []int64{2000}, 6000); got != 0 {
		t.Errorf("chance with no onsets: %.2f", got)
	}
}
