package audiocheck

import (
	"math"
	"slices"

	"github.com/nilpntr/content-fabrique/internal/render"
)

// The numbers the report is held to, and where each comes from. "earshot" is the measured profile of seven professional launch films
// in https://github.com/Leonxlnx/earshot (MIT), docs/launch-video-sound.md: web-delivered MP4s with 128 kbps AAC, modern SaaS and AI
// product launches and motion reels. Seven films are a small sample: its authors say to read the numbers as "what pros did here", not
// as laws. "practice" is delivery practice for web video that earshot cites and did not measure (YouTube normalises to about -14 LUFS
// and only turns loud uploads down; Apple's players aim at about -16; X, LinkedIn and website autoplay players do not normalise).
// "estimate" is ours: a number chosen from those two and the edit style of docs/design/edit-style-v1.md, not measured on anything.
const (
	// Integrated loudness: earshot measured -22.2 to -7.3 LUFS (median -14.8, 4 of 7 inside -16..-9); practice is about -14 for the
	// web and up to about -10 for a punchy social cut. The check passes in the web window earshot scores, -16 to -9.
	targetLUFS        = -14.0
	lufsWindowLo      = -16.0
	lufsWindowHi      = -9.0
	lufsWarnMargin    = 3.0
	truePeakLimit     = -1.0 // practice: <= -1 dBTP on the master, because AAC/MP3 encoding adds up to about 1 dB of overshoot
	truePeakDecoded   = 0.0  // earshot scores an already-decoded MP4 against 0 dBTP (6 of 7 films were measured above it: a flaw)
	rangeMaxLU        = 12.5 // earshot: loudness range 1.7 to 12.2 LU (median 5.5); only the upper end is a target, a flat mix is fine
	rangeWarnMaxLU    = 18.0 // estimate: speech with pauses reads wide, but past this the level wanders
	firstSoundMaxS    = 0.5  // earshot: first sound within 0.04 to 0.48 s (median 0.13 s); no silent cold open
	firstSoundWarnS   = 1.0  // estimate
	bodyWithinLU      = 3.0  // estimate: earshot: 6 of 7 films open within -1.8..+2.2 LU of the integrated level
	bodyMaxS          = 1.5  // estimate: the edit style's hook is in the first 2 s; the momentary window itself needs about 0.3 s
	bodyWarnS         = 3.0  // estimate
	gapMaxS           = 0.5  // earshot: no silent hole longer than 0.04 s except one film's deliberate 1.66 s; a stop-down is 0.15 to 1.5 s
	gapWarnS          = 1.7  // earshot: the longest deliberate silence measured
	endSilenceMaxS    = 0.5  // earshot: dead air after the sound, median 0.04 s; practice: finish within about 0.5 s of the last frame
	endSilenceWarnS   = 1.5  // estimate
	endLUMin          = -4.0 // earshot: the last 3 s reach body level, -2.2 to +5.6 LU against the integrated level (median +0.2)
	endLUMax          = 6.0
	cutSyncMinShare   = 0.5   // estimate: earshot measured a median of 81 % of cuts with an onset within 100 ms, but 86 % by chance
	sfxFrameMinShare  = 0.8   // estimate: our effects start on their sample, so almost all should land within a frame unless masked
	sfxPer10sMax      = 4.0   // estimate: the edit style asks for about one effect per 3 to 4 cuts; earshot's 1.2-1.7 salient hits a second include the music
	sfxPer10sWarnMax  = 8.0   // estimate
	quietMaxS         = 1.5   // estimate: earshot's films have 2.5 to 8.2 onsets a second; a speech edit is sparser, so only a long stretch is flagged
	quietWarnMaxS     = 3.0   // estimate
	dcMax             = 0.005 // estimate: 0.5 % of full scale; a healthy file is orders of magnitude lower
	imbalanceMaxDB    = 3.0   // estimate: stereo channels more than 3 dB apart are a lopsided mix (earshot's side-to-mid check is for width, not balance)
	clipPeakDBFS      = -0.1  // at or over this, with several samples at the peak, the file is clipped
	chanFloorDB       = -60.0
	sfxSearchBeforeMs = 100 // where to look for the onset of a planned effect: from this long before its at_ms
	sfxSearchAfterMs  = 250 // to this long after it
)

// chanceOffsetsMs are the shifts that give the chance level of the cut sync: the same cuts moved off the picture.
var chanceOffsetsMs = []int64{-900, -600, -300, 300, 600, 900}

// analyse turns the measurements into the report.
func analyse(m measured, timeMap []render.TimeMapEntry, cfg config) Report {
	r := Report{DurationS: round(m.durationS, 3), FPS: m.fps, HasAudio: m.hasAudio}
	fps := m.fps
	if cfg.fps > 0 {
		fps = cfg.fps
	}
	if fps <= 0 {
		fps = 30
	}
	r.FPS, r.Sync.FPS = fps, fps
	r.Silence = Silence{ThresholdDB: silenceDB, MinGapS: silenceMinS, Gaps: []Gap{}}
	r.Onsets.List = []Onset{}
	r.Sync.Cuts, r.Sync.SFX = []CutSync{}, []SFXSync{}
	r.Loudness.Momentary = []LoudnessPoint{}
	r.Problems.ChannelRMSDB = []float64{}

	cuts := cutTimes(timeMap)
	for _, p := range cfg.sfx {
		if p.AtMs < int64(math.Round(m.durationS*1000)) { // an effect that starts at or after the end plays nothing
			r.Density.SFXCount++
		}
	}
	if m.durationS > 0 {
		r.Density.SFXPer10s = round(float64(r.Density.SFXCount)/m.durationS*10, 2)
	}
	if !m.hasAudio {
		r.Checklist = checklist(r, cfg)
		r.Summary = summarise(r)
		r.Sheet = drawSheet(r, m, cuts, cfg)
		return r
	}

	// Loudness.
	r.Loudness.IntegratedLUFS = round(m.integrated, 1)
	r.Loudness.TruePeakDBTP = round(m.truePeak, 1)
	r.Loudness.RangeLU = round(m.lra, 1)
	r.Loudness.Momentary = m.momentary
	if r.Loudness.Momentary == nil {
		r.Loudness.Momentary = []LoudnessPoint{}
	}
	r.Loudness.EndLU = round(endLU(m), 1)

	// Silence.
	for _, g := range m.silences {
		g.StartS, g.EndS, g.DurationS = round(g.StartS, 3), round(g.EndS, 3), round(g.DurationS, 3)
		r.Silence.Gaps = append(r.Silence.Gaps, g)
		if g.DurationS > r.Silence.LongestS {
			r.Silence.LongestS = g.DurationS
		}
		if m.durationS-g.EndS < 0.05 {
			r.Silence.EndS = g.DurationS
		}
	}

	// Onsets.
	env := envelopeDB(m.pcm)
	onsets := detectOnsets(env)
	r.Onsets.List = onsets
	if onsets == nil {
		r.Onsets.List = []Onset{}
	}
	r.Onsets.Count = len(onsets)
	for _, o := range onsets {
		if o.RiseDB >= salientDB {
			r.Onsets.SalientCount++
		}
	}
	durMs := int64(math.Round(m.durationS * 1000))
	if m.durationS > 0 {
		r.Onsets.PerSecond = round(float64(r.Onsets.Count)/m.durationS, 2)
		r.Onsets.SalientPerSecond = round(float64(r.Onsets.SalientCount)/m.durationS, 2)
	}
	longest, at := longestWithout(onsets, durMs)
	r.Onsets.LongestWithoutS, r.Onsets.LongestWithoutAtS = float64(longest)/1000, float64(at)/1000
	r.Density.OnsetsPerS = r.Onsets.PerSecond
	r.Density.LongestQuietS, r.Density.LongestQuietAt = r.Onsets.LongestWithoutS, r.Onsets.LongestWithoutAtS

	// The hook.
	r.Hook = Hook{FirstSoundS: firstSoundS(env), BodyWithinLU: bodyWithinLU, TimeToBodyS: -1}
	if m.integrated > -69 {
		for _, p := range m.momentary {
			if p.LUFS >= m.integrated-bodyWithinLU {
				r.Hook.TimeToBodyS = p.TimeS
				break
			}
		}
	}

	// Sync: every cut and the nearest onset, and every planned effect.
	frameMs := 1000 / fps
	var within, within100 int
	for _, c := range cuts {
		cs := CutSync{AtMs: c, OnsetMs: -1}
		if o, ok := nearestOnset(onsets, c, 1000); ok {
			cs.OnsetMs, cs.DeltaMs = o.TimeMs, o.TimeMs-c
			if float64(abs64(cs.DeltaMs)) <= frameMs {
				within++
			}
			if abs64(cs.DeltaMs) <= 100 {
				within100++
			}
		}
		r.Sync.Cuts = append(r.Sync.Cuts, cs)
	}
	if len(cuts) > 0 {
		r.Sync.CutsWithinFrameShare = round(float64(within)/float64(len(cuts)), 3)
		r.Sync.CutsWithin100msShare = round(float64(within100)/float64(len(cuts)), 3)
		r.Sync.ChanceWithin100msShare = round(chanceShare(onsets, cuts, durMs), 3)
	}
	var sFrame, s100 int
	for _, p := range cfg.sfx {
		ss := SFXSync{ID: p.ID, Sfx: p.Sfx, Variant: p.Variant, AtMs: p.AtMs, OnsetMs: -1}
		if p.AtMs < durMs {
			r.Sync.SFXPlanned++
			// The first onset in the window, nearest to at_ms: an effect has one transient, the mix may have others around it.
			var best Onset
			found := false
			for _, o := range onsets {
				if o.TimeMs < p.AtMs-sfxSearchBeforeMs || o.TimeMs > p.AtMs+sfxSearchAfterMs {
					continue
				}
				if !found || abs64(o.TimeMs-p.AtMs) < abs64(best.TimeMs-p.AtMs) {
					best, found = o, true
				}
			}
			if found {
				ss.OnsetMs, ss.DeltaMs = best.TimeMs, best.TimeMs-p.AtMs
				r.Sync.SFXFound++
				if float64(abs64(ss.DeltaMs)) <= frameMs {
					ss.WithinFrame = true
					sFrame++
				}
				if abs64(ss.DeltaMs) <= 100 {
					s100++
				}
			}
		}
		r.Sync.SFX = append(r.Sync.SFX, ss)
	}
	if r.Sync.SFXPlanned > 0 {
		r.Sync.SFXWithinFrameShare = round(float64(sFrame)/float64(r.Sync.SFXPlanned), 3)
		r.Sync.SFXWithin100msShare = round(float64(s100)/float64(r.Sync.SFXPlanned), 3)
	}

	// Problems.
	r.Problems = problems(m)

	r.Checklist = checklist(r, cfg)
	r.Summary = summarise(r)
	r.Sheet = drawSheet(r, m, cuts, cfg)
	return r
}

// silenceFloor is what a level of minus infinity (digital silence) is written as: JSON has no infinity.
const silenceFloor = -120.0

func round(x float64, places int) float64 {
	switch {
	case math.IsNaN(x):
		return 0
	case math.IsInf(x, -1):
		return silenceFloor
	case math.IsInf(x, 1):
		return -silenceFloor
	}
	p := math.Pow(10, float64(places))
	return math.Round(x*p) / p
}

// cutTimes are the cuts of the edit: the start of every main-track entry of the time map but the first (which starts the video).
func cutTimes(timeMap []render.TimeMapEntry) []int64 {
	var starts []int64
	for _, e := range timeMap {
		if e.SourceInMs != nil {
			starts = append(starts, e.OutStartMs)
		}
	}
	slices.Sort(starts)
	starts = slices.Compact(starts)
	// The first item starts the video: a start at 0 is not a cut.
	return slices.DeleteFunc(starts, func(ms int64) bool { return ms <= 0 })
}

// chanceShare is the share of the cuts, moved by each of the chance offsets, that have an onset within 100 ms.
func chanceShare(onsets []Onset, cuts []int64, durMs int64) float64 {
	var sum float64
	var n int
	for _, off := range chanceOffsetsMs {
		var hit, total int
		for _, c := range cuts {
			t := c + off
			if t < 0 || t > durMs {
				continue
			}
			total++
			if _, ok := nearestOnset(onsets, t, 100); ok {
				hit++
			}
		}
		if total > 0 {
			sum += float64(hit) / float64(total)
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// endLU is the level of the last 3 s against the integrated loudness: the energy mean of the momentary loudness of the windows
// that end in them.
func endLU(m measured) float64 {
	if m.integrated <= -69 || len(m.momentary) == 0 {
		return 0
	}
	from := math.Max(m.durationS-3, 0.4)
	var sum float64
	var n int
	for _, p := range m.momentary {
		if p.TimeS >= from && p.TimeS <= m.durationS+0.05 {
			sum += math.Pow(10, p.LUFS/10)
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return 10*math.Log10(sum/float64(n)) - m.integrated
}

func problems(m measured) Problems {
	p := Problems{PeakDBFS: math.Inf(-1), ChannelRMSDB: []float64{}}
	minRMS, maxRMS := math.Inf(1), math.Inf(-1)
	for _, c := range m.channels {
		p.PeakDBFS = math.Max(p.PeakDBFS, c.peakDB)
		p.PeakCount = max(p.PeakCount, c.peakCount)
		if math.Abs(c.dc) > math.Abs(p.DCOffset) {
			p.DCOffset = c.dc
		}
		p.ChannelRMSDB = append(p.ChannelRMSDB, round(c.rmsDB, 1))
		if c.rmsDB > chanFloorDB {
			minRMS, maxRMS = math.Min(minRMS, c.rmsDB), math.Max(maxRMS, c.rmsDB)
		}
	}
	p.PeakDBFS = round(p.PeakDBFS, 2)
	p.DCOffset = round(p.DCOffset, 5)
	p.Clipping = p.PeakDBFS > 0 || (p.PeakDBFS >= clipPeakDBFS && p.PeakCount >= 3)
	p.DC = math.Abs(p.DCOffset) > dcMax
	if len(m.channels) >= 2 && !math.IsInf(minRMS, 1) {
		p.ImbalanceDB = round(maxRMS-minRMS, 1)
		p.Imbalance = p.ImbalanceDB > imbalanceMaxDB
	}
	return p
}
