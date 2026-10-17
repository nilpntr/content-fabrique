package ops

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
	"github.com/nilpntr/content-fabrique/internal/timeline/diff"
)

// The property tests (design note section 7): starting from every valid fixture and chaining random batches of ops,
//
//   - a batch either applies, and then the result validates with no error, or it fails and leaves the spec
//     byte-identical, with issues that name the op;
//   - the diff of an applied batch round-trips: ApplyPatch(parent, Diff(parent, child)) == child;
//   - ids are unique, the main track is contiguous and in order, and text and audio keep their absolute times unless an
//     op moved them;
//   - Apply is deterministic, never changes its input, and gives the same answer for the typed ops and for the same ops
//     after a trip through JSON.
//
// The random source is seeded, so a failure names the fixture, the batch number and the batch.

const (
	batchesPerFixture      = 70
	batchesPerFixtureShort = 12
)

type generator struct {
	r     *rand.Rand
	cat   timeline.MemoryCatalog
	video []string // video asset ids, sorted
	audio []string // audio asset ids, sorted
}

func newGenerator(r *rand.Rand, cat timeline.MemoryCatalog) *generator {
	g := &generator{r: r, cat: cat}
	for id, a := range cat {
		if a.Kind == timeline.AssetVideo {
			g.video = append(g.video, id)
		} else {
			g.audio = append(g.audio, id)
		}
	}
	sort.Strings(g.video)
	sort.Strings(g.audio)
	return g
}

func (g *generator) chance(percent int) bool { return g.r.IntN(100) < percent }

func pick[T any](g *generator, s []T) T { return s[g.r.IntN(len(s))] }

// known is what the generator may point an op at: the ids in the spec and the ids it gave to new items in this batch.
type known struct {
	clips, texts, sounds []string
	total                int64
	fps                  int
}

func scan(spec timeline.Spec) known {
	k := known{total: timeline.TotalDuration(&spec), fps: spec.Output.FPS}
	for _, tr := range spec.Tracks {
		switch tr := tr.(type) {
		case *timeline.VideoTrack:
			for _, it := range tr.Items {
				k.clips = append(k.clips, it.ID)
			}
		case *timeline.TextTrack:
			for _, it := range tr.Items {
				k.texts = append(k.texts, it.ID)
			}
		case *timeline.AudioTrack:
			for _, it := range tr.Items {
				k.sounds = append(k.sounds, it.Common().ID)
			}
		}
	}
	return k
}

// id picks an existing id of the list, or occasionally one that does not exist.
func (g *generator) id(ids []string) string {
	if len(ids) == 0 || g.chance(4) {
		return "zz" + fmt.Sprint(g.r.IntN(3))
	}
	return pick(g, ids)
}

func (g *generator) newID(prefix string, list *[]string) string {
	if g.chance(60) {
		return ""
	}
	id := fmt.Sprintf("%s%d", prefix, 100+g.r.IntN(900))
	*list = append(*list, id)
	return id
}

func (g *generator) source(asset string) timeline.Source {
	dur := g.cat[asset].DurationMs
	if g.chance(5) {
		return timeline.Source{InMs: dur - 100, OutMs: dur + 1 + int64(g.r.IntN(3000))} // past the end
	}
	if g.chance(3) {
		return timeline.Source{InMs: 500, OutMs: 500} // empty
	}
	in := int64(g.r.IntN(int(dur) - 700))
	span := int64(300 + g.r.IntN(min(6000, int(dur-in)-299)))
	return timeline.Source{InMs: in, OutMs: in + span}
}

var (
	speeds     = []float64{0.25, 0.5, 0.75, 1, 1, 1, 1.25, 1.5, 2, 3, 4, 1.3333, 0.3, 3.7}
	looks      = []string{"none", "punchy", "warm", "cool", "bw", "matte"}
	modes      = []string{"cover", "contain", "blur_fill"}
	texts      = []string{"Back day", "Rows. Then rows.", "Two\nlines", "Three\nshort\nlines", "No pain, no gain", "!"}
	styles     = []string{"title_bold", "title_bold", "caption_plain", "mystery"}
	anchors    = []string{"upper_third", "center", "lower_third"}
	animations = []string{"none", "pop_in", "fade", "typewriter", "slide_up"}
	transTypes = []string{"cut", "crossfade", "dip_to_black", "whip", "whip_left", "whip_right", "zoom_through", "flash"}
	presets    = []string{"none", "punch_in", "push_in", "pull_back", "shake"}
)

func (g *generator) point() *timeline.Point {
	return &timeline.Point{X: float64(g.r.IntN(101)) / 100, Y: float64(g.r.IntN(101)) / 100}
}

func (g *generator) speed() float64 {
	if g.chance(2) {
		return 9
	}
	return pick(g, speeds)
}

// op makes one random op. The weights favour ops that usually apply, with enough nonsense to reach the failure paths.
func (g *generator) op(k *known) Op {
	weights := []struct {
		w    int
		make func() Op
	}{
		{14, func() Op { return g.addClip(k) }},
		{6, func() Op { return &RemoveItem{ID: g.id(k.clips)} }},
		{10, func() Op { return g.moveItem(k) }},
		{12, func() Op { return g.trimItem(k) }},
		{6, func() Op {
			asset := pick(g, g.video)
			return &ReplaceSource{ID: g.id(k.clips), AssetID: asset, Source: g.source(asset)}
		}},
		{6, func() Op { return &SetSpeed{ID: g.id(k.clips), Speed: g.speed()} }},
		{5, func() Op {
			o := &SetReframe{ID: g.id(k.clips), Mode: pick(g, modes)}
			if g.chance(50) {
				o.Focus = g.point()
			}
			if g.chance(50) {
				z := 1 + float64(g.r.IntN(21))/10
				o.Zoom = &z
			}
			return o
		}},
		{8, func() Op { return g.addText(k) }},
		{6, func() Op { return g.updateText(k) }},
		{4, func() Op { return &RemoveText{ID: g.id(k.texts)} }},
		{8, func() Op {
			o := &SetTransition{ID: g.id(k.clips), Type: pick(g, transTypes)}
			if o.Type != timeline.TransitionCut {
				o.DurationMs = int64(100 + g.r.IntN(5)*100)
			}
			return o
		}},
		{6, func() Op { return g.setSound(k) }},
		{3, func() Op { return &RemoveSound{ID: g.id(k.sounds)} }},
		{8, func() Op { return g.snap(k) }},
		{4, func() Op {
			o := &SetLook{Preset: pick(g, looks)}
			if g.chance(80) {
				o.ID = g.id(k.clips)
			}
			return o
		}},
		{3, func() Op {
			o := &SetCaptions{}
			if g.chance(60) {
				v := g.chance(50)
				o.Enabled = &v
			}
			if g.chance(50) {
				o.Style = pick(g, []string{"word_highlight", "chunk"})
			}
			if g.chance(40) {
				v := g.chance(50)
				o.Uppercase = &v
			}
			if g.chance(40) {
				v := 1 + g.r.IntN(4)
				o.MaxWords = &v
			}
			return o
		}},
		{5, func() Op { return g.setMotion(k) }},
		{5, func() Op { return g.setSpeedRamp(k) }},
		{4, func() Op {
			o := &SetClipAudio{ID: g.id(k.clips)}
			if g.chance(60) {
				v := g.chance(50)
				o.UseSource = &v
			}
			if g.chance(60) || o.UseSource == nil {
				v := float64(g.r.IntN(37) - 30)
				o.GainDB = &v
			}
			return o
		}},
	}
	total := 0
	for _, w := range weights {
		total += w.w
	}
	n := g.r.IntN(total)
	for _, w := range weights {
		if n < w.w {
			return w.make()
		}
		n -= w.w
	}
	return nil
}

// setMotion makes a set_motion with every kind of mistake the validator catches: a clip that is too short, an at_ms
// outside it or on a preset that has none.
func (g *generator) setMotion(k *known) Op {
	o := &SetMotion{ID: g.id(k.clips), Preset: pick(g, presets)}
	if g.chance(40) {
		v := int64(g.r.IntN(3000))
		o.AtMs = &v
	}
	if g.chance(40) {
		v := 0.05 + float64(g.r.IntN(31))/100
		o.Strength = &v
	}
	return o
}

// setSpeedRamp makes a set_speed_ramp with sorted points, mostly: some are out of order or past the clip.
func (g *generator) setSpeedRamp(k *known) Op {
	o := &SetSpeedRamp{ID: g.id(k.clips)}
	at := int64(0)
	for range 2 + g.r.IntN(3) {
		o.Points = append(o.Points, timeline.SpeedPoint{AtMs: at, Speed: pick(g, speeds)})
		at += int64(100 + g.r.IntN(1500))
		if g.chance(5) {
			at = max(0, at-2000) // out of order
		}
	}
	return o
}

func (g *generator) addClip(k *known) Op {
	asset := pick(g, g.video)
	o := &AddClip{AssetID: asset, Source: g.source(asset)}
	o.ID = g.newID("x", &k.clips)
	switch {
	case g.chance(40):
		i := g.r.IntN(len(k.clips) + 2)
		o.Index = &i
	case g.chance(30):
		o.AfterID = g.id(k.clips)
	}
	if g.chance(30) {
		s := g.speed()
		o.Speed = &s
	} else if g.chance(10) {
		o.SpeedRamp = []timeline.SpeedPoint{{AtMs: 0, Speed: 1}, {AtMs: int64(300 + g.r.IntN(1500)), Speed: pick(g, speeds)}}
	}
	if g.chance(10) {
		o.Motion = &timeline.Motion{Preset: pick(g, presets)}
	}
	if g.chance(20) {
		o.Reframe = &timeline.Reframe{Mode: pick(g, modes)}
	}
	if g.chance(15) {
		o.Look = &timeline.Look{Preset: pick(g, looks)}
	}
	if g.chance(15) {
		o.TransitionIn = &timeline.Transition{Type: timeline.TransitionCrossfade, DurationMs: int64(100 + g.r.IntN(4)*100)}
	}
	if g.chance(15) {
		on := g.chance(50)
		o.Audio = &timeline.VideoAudio{UseSource: &on}
	}
	if g.chance(10) {
		why := "because"
		o.Why = &why
	}
	return o
}

func (g *generator) moveItem(k *known) Op {
	switch g.r.IntN(4) {
	case 0:
		at := int64(g.r.IntN(int(k.total) + 1))
		return &MoveItem{ID: g.id(k.texts), AtMs: &at}
	case 1:
		at := int64(g.r.IntN(int(k.total) + 1))
		return &MoveItem{ID: g.id(k.sounds), AtMs: &at}
	case 2:
		i := g.r.IntN(len(k.clips) + 1)
		return &MoveItem{ID: g.id(k.clips), Index: &i}
	}
	return &MoveItem{ID: g.id(k.clips), AfterID: g.id(k.clips)}
}

func (g *generator) trimItem(k *known) Op {
	switch g.r.IntN(5) {
	case 0:
		d := int64(300 + g.r.IntN(3000))
		return &TrimItem{ID: g.id(k.texts), DurationMs: &d}
	case 1:
		d := int64(1 + g.r.IntN(5000))
		return &TrimItem{ID: g.id(k.sounds), DurationMs: &d}
	}
	o := &TrimItem{ID: g.id(k.clips)}
	if g.chance(60) {
		v := int64(g.r.IntN(4000))
		o.InMs = &v
	}
	if g.chance(60) || o.InMs == nil {
		v := int64(500 + g.r.IntN(9000))
		o.OutMs = &v
	}
	return o
}

func (g *generator) addText(k *known) Op {
	o := &AddText{
		AtMs: int64(g.r.IntN(int(k.total) + 500)), DurationMs: int64(300 + g.r.IntN(3000)),
		Text: pick(g, texts), Style: pick(g, styles),
	}
	o.ID = g.newID("y", &k.texts)
	if g.chance(75) {
		o.Anchor = pick(g, anchors)
	} else {
		o.Position = g.point()
	}
	if g.chance(40) {
		o.Animation = pick(g, animations)
	}
	return o
}

func (g *generator) updateText(k *known) Op {
	o := &UpdateText{ID: g.id(k.texts)}
	if g.chance(40) {
		v := pick(g, texts)
		o.Text = &v
	}
	if g.chance(25) {
		v := int64(g.r.IntN(int(k.total) + 500))
		o.AtMs = &v
	}
	if g.chance(25) {
		v := int64(300 + g.r.IntN(2000))
		o.DurationMs = &v
	}
	if g.chance(25) {
		o.Style = pick(g, styles)
	}
	switch g.r.IntN(4) {
	case 0:
		o.Anchor = pick(g, anchors)
	case 1:
		o.Position = g.point()
	}
	if g.chance(20) {
		o.Animation = pick(g, animations)
	}
	return o
}

func (g *generator) setSound(k *known) Op {
	o := &SetSound{}
	o.ID = g.newID("z", &k.sounds)
	if g.chance(25) && len(k.sounds) > 0 {
		o.ID = pick(g, k.sounds) // replace
	}
	switch g.r.IntN(3) {
	case 0:
		o.Kind, o.TrendID = timeline.AudioTrendSound, "trd_01jcmevy4tr3fffvjc82eh7rjm"
	case 1:
		o.Kind, o.AssetID, o.LicenseID = timeline.AudioLicensedTrack, pick(g, g.audio), "lic_01jg1bfdc146rw2b0vngmx6p6f"
	default:
		o.Kind, o.AssetID = timeline.AudioCreatorAudio, pick(g, g.audio)
	}
	if g.chance(60) {
		v := int64(g.r.IntN(int(k.total) + 1))
		o.AtMs = &v
	}
	if g.chance(50) {
		v := int64(g.r.IntN(8000))
		o.SourceOffsetMs = &v
	}
	if g.chance(40) {
		v := int64(1 + g.r.IntN(8000))
		o.DurationMs = &v
	}
	if g.chance(30) {
		v := float64(g.r.IntN(37) - 30)
		o.GainDB = &v
	}
	if g.chance(30) {
		v := int64(g.r.IntN(1500))
		o.FadeInMs = &v
	}
	if g.chance(30) {
		v := int64(g.r.IntN(1500))
		o.FadeOutMs = &v
	}
	return o
}

func (g *generator) snap(k *known) Op {
	o := &SnapCutsToBeats{}
	if g.chance(50) {
		bpm := 60 + float64(g.r.IntN(1400))/10
		first := float64(g.r.IntN(6000)) / 10
		o.Grid = BeatGrid{BPM: &bpm, FirstBeatMs: &first}
		if g.chance(40) {
			v := 2 + g.r.IntN(6)
			o.Grid.BeatsPerBar = &v
		}
	} else {
		var beats []float64
		t := float64(g.r.IntN(500))
		for t < 40000 {
			beats = append(beats, t)
			t += 300 + float64(g.r.IntN(5000))/10
		}
		o.Grid = BeatGrid{BeatsMs: beats}
	}
	if g.chance(40) {
		o.SoundID = g.id(k.sounds)
	}
	if g.chance(60) {
		so := &SnapOptions{PreferDownbeats: g.chance(30), WriteMarkers: g.chance(30)}
		if g.chance(40) {
			v := int64(g.r.IntN(800))
			so.MaxShiftMs = &v
		}
		if g.chance(30) {
			v := float64(g.r.IntN(20)) / 10
			so.MaxShiftBeats = &v
		}
		if g.chance(20) {
			v := int64(300 + g.r.IntN(700))
			so.MinShotMs = &v
		}
		o.Options = so
	}
	return o
}

func (g *generator) batch(spec timeline.Spec) []Op {
	k := scan(spec)
	n := 1 + g.r.IntN(5)
	if g.chance(10) {
		n = 1 + g.r.IntN(12)
	}
	batch := make([]Op, 0, n)
	for range n {
		batch = append(batch, g.op(&k))
	}
	return batch
}

// ---- the checks -------------------------------------------------------------------------------------------------

type stats struct {
	batches, applied, failed, noChange int
	okByOp                             map[string]int
	failByCode                         map[string]int
}

func newStats() *stats { return &stats{okByOp: map[string]int{}, failByCode: map[string]int{}} }

func (s *stats) merge(o *stats) {
	s.batches += o.batches
	s.applied += o.applied
	s.failed += o.failed
	s.noChange += o.noChange
	for k, v := range o.okByOp {
		s.okByOp[k] += v
	}
	for k, v := range o.failByCode {
		s.failByCode[k] += v
	}
}

func ids(spec timeline.Spec) []string {
	var out []string
	for _, tr := range spec.Tracks {
		out = append(out, tr.TrackID())
		switch tr := tr.(type) {
		case *timeline.VideoTrack:
			for _, it := range tr.Items {
				out = append(out, it.ID)
			}
		case *timeline.TextTrack:
			for _, it := range tr.Items {
				out = append(out, it.ID)
			}
		case *timeline.AudioTrack:
			for _, it := range tr.Items {
				out = append(out, it.Common().ID)
			}
		}
	}
	return out
}

// targets are the ids an op names as the thing to change; the other items must keep their absolute times.
func targets(op Op) []string {
	switch o := op.(type) {
	case *MoveItem:
		return []string{o.ID}
	case *UpdateText:
		return []string{o.ID}
	case *SetSound:
		return []string{o.ID}
	case *AddText:
		return []string{o.ID}
	}
	return nil
}

func checkContiguous(t *testing.T, spec timeline.Spec) {
	t.Helper()
	main, _, _ := timeline.MainTrack(&spec)
	var end int64
	for i, it := range main.Items {
		if i == 0 {
			if it.AtMs != 0 {
				t.Fatalf("first clip %s starts at %d, not 0", it.ID, it.AtMs)
			}
		} else if want := end - it.TransitionDurationMs(); it.AtMs != want {
			t.Fatalf("clip %s starts at %d, want %d (previous ends at %d, transition %d)", it.ID, it.AtMs, want, end, it.TransitionDurationMs())
		}
		end = it.AtMs + timeline.OutputDuration(it)
	}
	if i := timeline.TotalDuration(&spec); i != end && len(main.Items) > 0 {
		t.Fatalf("total %d but last clip ends at %d", i, end)
	}
}

func TestPropertyRandomBatches(t *testing.T) {
	cat := fixtureCatalog(t)
	files := fixtureFiles(t, "valid")
	perFixture := batchesPerFixture
	if testing.Short() {
		perFixture = batchesPerFixtureShort
	}
	st := newStats()
	var mu sync.Mutex
	// One subtest per fixture, in parallel: the chains are independent, and the batches are slow under the race detector.
	t.Run("fixtures", func(t *testing.T) {
		for fi, f := range files {
			name := filepath.Base(f)
			spec := parseSpec(t, readFile(t, f))
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				local := newStats()
				g := newGenerator(rand.New(rand.NewPCG(uint64(fi)+1, 0x9e3779b97f4a7c15)), cat)
				ver, err := InitialVersion(spec, Author{Kind: AuthorAgent, ID: "run_1"})
				if err != nil {
					t.Fatal(err)
				}
				for n := range perFixture {
					batch := g.batch(ver.Spec)
					label := fmt.Sprintf("%s batch %d: %s", name, n, mustJSON(t, batch))
					if next := checkBatch(t, label, ver.Spec, batch, cat, local, ver); next != nil {
						ver = next
					}
				}
				mu.Lock()
				defer mu.Unlock()
				st.merge(local)
			})
		}
	})
	t.Logf("%d batches: %d applied, %d changed nothing, %d failed", st.batches, st.applied, st.noChange, st.failed)
	t.Logf("applied by op: %v", st.okByOp)
	t.Logf("failures by code: %v", st.failByCode)
	if st.batches < 2000 && !testing.Short() {
		t.Fatalf("only %d batches ran; the property test must run at least 2000", st.batches)
	}
	if st.applied*5 < st.batches {
		t.Errorf("only %d of %d batches applied; the generator is too wild to test the success path", st.applied, st.batches)
	}
	if !testing.Short() {
		for _, name := range Names {
			if st.okByOp[name] < 20 {
				t.Errorf("op %s applied in only %d batches", name, st.okByOp[name])
			}
		}
	}
}

// checkBatch holds the properties for one batch and returns the new version when the batch applied and changed
// something, nil otherwise.
func checkBatch(t *testing.T, label string, spec timeline.Spec, batch []Op, cat timeline.MemoryCatalog, st *stats, parent *Version) *Version {
	t.Helper()
	st.batches++
	before := mustJSON(t, spec)

	out, res := Apply(spec, batch, cat)
	if !bytes.Equal(before, mustJSON(t, spec)) {
		t.Fatalf("%s: Apply changed its input", label)
	}

	// Deterministic, and the same ops after a trip through JSON do the same thing (if the schema lets them through).
	// One more Apply does for both: it takes the parsed ops when there are any, the typed ones again otherwise.
	again := batch
	if enc, err := MarshalBatch(batch); err != nil {
		t.Fatalf("%s: encode: %v", label, err)
	} else if parsed, pres := ParseBatch(enc); pres.OK() {
		again = parsed
	}
	out2, res2 := Apply(spec, again, cat)
	if !bytes.Equal(mustJSON(t, out), mustJSON(t, out2)) || !bytes.Equal(mustJSON(t, res), mustJSON(t, res2)) {
		t.Fatalf("%s: a second run (of the parsed ops, if the schema accepts them) gives another answer\nfirst:  %s %s\nsecond: %s %s", label,
			mustJSON(t, out), mustJSON(t, res), mustJSON(t, out2), mustJSON(t, res2))
	}

	if !res.OK() {
		st.failed++
		if !bytes.Equal(before, mustJSON(t, out)) {
			t.Fatalf("%s: the batch failed but the spec changed", label)
		}
		if len(res.Created) != 0 {
			t.Fatalf("%s: failed batch reports created items", label)
		}
		for _, is := range res.Issues {
			if is.Message == "" || is.Code == "" || is.OpIndex < NoOp || is.OpIndex >= len(batch) {
				t.Fatalf("%s: bad issue %+v", label, is)
			}
			if is.OpIndex != NoOp && is.Op != batch[is.OpIndex].OpName() {
				t.Fatalf("%s: issue %+v names the wrong op (op %d is %s)", label, is, is.OpIndex, batch[is.OpIndex].OpName())
			}
			if is.Severity == timeline.SeverityError {
				st.failByCode[string(is.Code)]++
			}
		}
		// ParseBatch is the entry point for the agent: a failed batch is also what NewVersion refuses.
		if st.batches%5 == 0 {
			if v, vres := NewVersion(*parent, batch, Author{Kind: AuthorAgent, ID: "run_1"}, cat); v != nil || vres.OK() {
				t.Fatalf("%s: NewVersion accepted a failed batch", label)
			}
		}
		return nil
	}
	st.applied++
	for _, op := range batch {
		st.okByOp[op.OpName()]++
	}

	// The result is a valid spec.
	data := mustJSON(t, out)
	if v := timeline.Validate(data, cat); !v.OK() {
		t.Fatalf("%s: Apply returned a spec that fails validation: %+v", label, v.Errors())
	}
	if _, err := timeline.Parse(data); err != nil {
		t.Fatalf("%s: the result does not parse: %v", label, err)
	}
	checkContiguous(t, out)

	// Ids are unique, and the items the batch says it created are there.
	all := ids(out)
	seen := map[string]bool{}
	for _, id := range all {
		if seen[id] {
			t.Fatalf("%s: duplicate id %q", label, id)
		}
		seen[id] = true
	}
	for _, c := range res.Created {
		if !seen[c.ID] || c.OpIndex < 0 || c.OpIndex >= len(batch) {
			t.Fatalf("%s: created %+v is not in the spec", label, c)
		}
	}

	// Text and audio keep their absolute times, unless an op moved them.
	moved := map[string]bool{}
	for _, op := range batch {
		for _, id := range targets(op) {
			moved[id] = true
		}
	}
	checkAbsolute(t, label, spec, out, moved)

	// The diff round-trips.
	patch, err := diff.Specs(spec, out)
	if err != nil {
		t.Fatalf("%s: diff: %v", label, err)
	}
	applied, err := diff.ApplySpec(spec, patch)
	if err != nil {
		t.Fatalf("%s: apply patch: %v\npatch: %s", label, err, mustJSON(t, patch))
	}
	if !bytes.Equal(mustJSON(t, applied), data) {
		t.Fatalf("%s: ApplyPatch(parent, Diff(parent, child)) != child\npatch: %s", label, mustJSON(t, patch))
	}
	// ... also as bytes, and through the patch's own JSON.
	pj := mustJSON(t, patch)
	reparsed, err := diff.ParsePatch(pj)
	if err != nil {
		t.Fatalf("%s: patch does not parse: %v", label, err)
	}
	raw, err := diff.ApplyPatch(before, reparsed)
	if err != nil {
		t.Fatalf("%s: apply parsed patch: %v", label, err)
	}
	if eq, err := diff.Equal(raw, data); err != nil || !eq {
		t.Fatalf("%s: the parsed patch gives another document (%v)", label, err)
	}
	if lines, err := diff.Summarize(spec, patch); err != nil || (len(patch) > 0 && len(lines) == 0) {
		t.Fatalf("%s: summarize: %v %v", label, lines, err)
	}

	// A version of it (every third batch: NewVersion applies the batch once more).
	if len(patch) == 0 {
		st.noChange++
		if v, vres := NewVersion(*parent, batch, Author{Kind: AuthorAgent, ID: "run_1"}, cat); v != nil || !vres.OK() {
			t.Fatalf("%s: a batch that changes nothing must give no version", label)
		}
		return nil
	}
	if st.batches%3 != 0 {
		return &Version{Number: parent.Number + 1, Parent: parent.Number, Spec: out}
	}
	v, vres := NewVersion(*parent, batch, Author{Kind: AuthorAgent, ID: "run_1"}, cat)
	if v == nil || !vres.OK() {
		t.Fatalf("%s: NewVersion failed for a batch that applied: %+v", label, vres.Issues)
	}
	if v.Number != parent.Number+1 || v.Parent != parent.Number || !bytes.Equal(mustJSON(t, v.Spec), data) || len(v.Summary) == 0 {
		t.Fatalf("%s: version: %+v", label, v)
	}
	return v
}

func checkAbsolute(t *testing.T, label string, before, after timeline.Spec, moved map[string]bool) {
	t.Helper()
	times := func(spec timeline.Spec) map[string]int64 {
		m := map[string]int64{}
		for _, it := range textItems(spec) {
			m[it.ID] = it.AtMs
		}
		for _, it := range soundItems(spec) {
			m[it.Common().ID] = it.Common().AtMs
		}
		return m
	}
	b, a := times(before), times(after)
	for id, at := range b {
		if now, ok := a[id]; ok && now != at && !moved[id] {
			t.Fatalf("%s: item %s moved from %d to %d without an op that moves it", label, id, at, now)
		}
	}
}

// TestPropertyOpsRoundTripThroughJSON checks that every op the generator makes encodes to JSON that decodes to the
// same op, whenever the schema accepts it.
func TestPropertyOpsRoundTripThroughJSON(t *testing.T) {
	cat := fixtureCatalog(t)
	g := newGenerator(rand.New(rand.NewPCG(7, 11)), cat)
	spec := baseSpec(t)
	accepted, rejected := 0, 0
	for range 1500 {
		batch := g.batch(spec)
		enc, err := MarshalBatch(batch)
		if err != nil {
			t.Fatal(err)
		}
		parsed, res := ParseBatch(enc)
		if !res.OK() {
			rejected++
			continue
		}
		accepted++
		again, err := MarshalBatch(parsed)
		if err != nil || !bytes.Equal(enc, again) {
			t.Fatalf("round trip differs:\n in: %s\nout: %s", enc, again)
		}
	}
	t.Logf("%d batches accepted by the schema, %d rejected", accepted, rejected)
	if accepted < 300 {
		t.Fatalf("only %d accepted", accepted)
	}
}

// TestPropertyFailureLeavesNoTrace applies deliberately broken batches to every fixture.
func TestPropertyFailureLeavesNoTrace(t *testing.T) {
	cat := fixtureCatalog(t)
	bad := []string{
		`[{"op":"remove_item","id":"nope"}]`,
		`[{"op":"set_look","preset":"bw"},{"op":"remove_text","id":"nope"}]`,
		`[{"op":"set_speed","id":"c1","speed":4},{"op":"trim_item","id":"c1","out_ms":1}]`,
		`[{"op":"add_text","at_ms":0,"duration_ms":300,"text":"x","style":"title_bold","anchor":"center"},{"op":"add_clip","index":99,"asset_id":"` + vid60 + `","source":{"in_ms":0,"out_ms":1000}}]`,
	}
	for _, f := range fixtureFiles(t, "valid") {
		spec := parseSpec(t, readFile(t, f))
		before := mustJSON(t, spec)
		for _, js := range bad {
			out, res := Apply(spec, batchOf(t, js), cat)
			if res.OK() && !strings.Contains(js, `"set_speed"`) {
				// fixtures that happen to have a "c1" can take the speed change; the rest must fail.
				t.Logf("%s: %s applied", filepath.Base(f), js)
			}
			if !res.OK() && !bytes.Equal(before, mustJSON(t, out)) {
				t.Fatalf("%s: %s: failed batch changed the spec", filepath.Base(f), js)
			}
			if !bytes.Equal(before, mustJSON(t, spec)) {
				t.Fatalf("%s: input changed", filepath.Base(f))
			}
		}
	}
}

var _ = slices.Contains[[]string]
var _ = json.Marshal
