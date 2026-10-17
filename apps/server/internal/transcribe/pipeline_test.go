package transcribe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/spend"
	"github.com/nilpntr/content-fabrique/internal/spend/spendtest"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

type failure struct {
	reason string
	source *time.Time
}

type fakeStore struct {
	asset     store.Asset
	assetErr  error
	derivs    []store.Derivative
	statuses  []string
	failures  []failure
	saved     *store.NewTranscript
	usage     []store.UsageEntry
	saveErr   error
	statusErr error
	usageErr  error
}

func (f *fakeStore) GetAsset(context.Context, ids.WorkspaceID, ids.AssetID) (store.Asset, error) {
	return f.asset, f.assetErr
}

func (f *fakeStore) ListAssetDerivatives(context.Context, ids.WorkspaceID, []ids.AssetID) ([]store.Derivative, error) {
	return f.derivs, nil
}

func (f *fakeStore) SetTranscriptStatus(_ context.Context, _ ids.WorkspaceID, _ ids.AssetID, status string) error {
	f.statuses = append(f.statuses, status)
	return f.statusErr
}

func (f *fakeStore) FailTranscript(_ context.Context, _ ids.WorkspaceID, _ ids.AssetID, reason string, source *time.Time) error {
	f.failures = append(f.failures, failure{reason, source})
	return f.statusErr
}

func (f *fakeStore) SaveTranscript(_ context.Context, _ ids.WorkspaceID, _ ids.AssetID, t store.NewTranscript) (store.Transcript, error) {
	if f.saveErr != nil {
		return store.Transcript{}, f.saveErr
	}
	f.saved = &t
	return store.Transcript{}, nil
}

func (f *fakeStore) InsertUsage(_ context.Context, e store.UsageEntry) error {
	if f.usageErr != nil {
		return f.usageErr
	}
	f.usage = append(f.usage, e)
	return nil
}

type fakeObjects struct {
	body []byte
	keys []string
}

func (o *fakeObjects) Get(_ context.Context, key string) (storage.Object, error) {
	o.keys = append(o.keys, key)
	return storage.Object{Body: io.NopCloser(bytes.NewReader(o.body)), Size: int64(len(o.body))}, nil
}

type fakeProvider struct {
	words []Word
	calls []Call
	err   error
	runs  int
	// pricePerMinute makes the provider cost money: CostMicros is the audio's minutes times it.
	pricePerMinute int64
}

func (p *fakeProvider) Name() string  { return "fake" }
func (p *fakeProvider) Model() string { return "fake-model" }
func (p *fakeProvider) CostMicros(ms int64) (int64, string) {
	if p.pricePerMinute == 0 {
		return 0, "local"
	}
	return (ms*p.pricePerMinute + 59_999) / 60_000, "fake-prices"
}

func (p *fakeProvider) Transcribe(context.Context, Audio, Options) (Transcript, error) {
	p.runs++
	return Transcript{Language: "en", Words: p.words, Calls: p.calls}, p.err
}

// offProvider is a provider that reports it is not configured (Workers AI without an account or a price).
type offProvider struct{ *fakeProvider }

func (*offProvider) Configured() bool { return false }

type fakeDetector struct {
	segs []Segment
	err  error
}

func (d fakeDetector) DetectSpeech(context.Context, Audio) ([]Segment, error) { return d.segs, d.err }

type rig struct {
	ws   ids.WorkspaceID
	id   ids.AssetID
	st   *fakeStore
	obj  *fakeObjects
	prov *fakeProvider
	det  fakeDetector
	// mem is the in-memory spend backend (holds, counters, ledger rows); gate sits on it with the caps below.
	mem *spendtest.Memory
	// spendCap is the workspace's daily cap in micro-dollars; mins the daily minutes (0: no limit). noGate runs without a gate.
	spendCap int64
	platCap  int64
	mins     int
	noGate   bool
	gen      int64
	job      *JobRef
}

// usage is the ledger rows the pipeline wrote.
func (r *rig) usage() []store.UsageEntry { return r.mem.Rows() }

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{ws: ids.WorkspaceID{UUID: uuid.New()}, id: ids.AssetID{UUID: uuid.New()}, prov: &fakeProvider{}, mem: &spendtest.Memory{}, spendCap: 1 << 40, platCap: 1 << 40}
	key, err := storage.DerivativeKey(r.ws, r.id, "audio_16k.wav")
	if err != nil {
		t.Fatal(err)
	}
	ingested := time.Date(2026, 10, 17, 12, 0, 0, 123456000, time.UTC)
	r.st = &fakeStore{
		asset:  store.Asset{Status: store.AssetReady, Kind: store.AssetKindVideo, IngestedAt: &ingested},
		derivs: []store.Derivative{{Kind: store.DerivativeAudio16k, R2Key: key}},
	}
	r.gen = store.IngestGeneration(r.st.asset)
	// 4 s of audio: speech-like bursts at 0.5-1.0 s and 2.0-2.6 s.
	r.obj = &fakeObjects{body: EncodeWAV(speechLike(4000, [][2]int64{{500, 1000}, {2000, 2600}}))}
	r.det = fakeDetector{segs: []Segment{{StartMs: 500, EndMs: 1000}, {StartMs: 2000, EndMs: 2600}}}
	return r
}

func (r *rig) pipeline(t *testing.T) *Pipeline {
	t.Helper()
	return r.pipelineFor(t, r.prov)
}

func (r *rig) gate() *spend.Gate {
	if r.noGate {
		return nil
	}
	return spend.NewGate(r.mem, spend.Options{
		Caps: store.SpendCaps{WorkspaceDailyMicros: r.spendCap, PlatformDailyMicros: r.platCap, WorkspaceDailyAudioMs: int64(r.mins) * 60_000},
		Now:  func() time.Time { return testNow },
	})
}

func (r *rig) pipelineFor(t *testing.T, prov Provider) *Pipeline {
	t.Helper()
	p, err := New(Config{Store: r.st, Objects: r.obj, Provider: prov, Detector: r.det, Gate: r.gate(), BilledTo: store.BilledToTest, TempDir: t.TempDir(), now: func() time.Time { return testNow }})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// testNow is the clock of the pipeline and the gate: the day the ledger rows and the counters are on.
var testNow = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

func (r *rig) run(t *testing.T) (Result, error) {
	t.Helper()
	ctx := context.Background()
	if r.job != nil {
		ctx = WithJob(ctx, *r.job)
	}
	return r.pipeline(t).Run(ctx, r.ws, r.id, r.gen)
}

// seedMinutes puts audio that was transcribed earlier today in the ledger.
func (r *rig) seedMinutes(ms int64) {
	r.mem.Seed(store.UsageEntry{WorkspaceID: r.ws, Kind: store.KindTranscription, AudioMs: ms, BilledTo: store.BilledToTest, CreatedAt: testNow.Add(-time.Hour)})
}

func TestRunTranscribesSpeechAndMetersIt(t *testing.T) {
	r := newRig(t)
	r.prov.words = []Word{
		{Text: "Hello,", AnchorMs: 900, Confidence: 0.9}, {Text: "um", AnchorMs: 2400},
		{Text: "ghost", AnchorMs: 3500}, // in the silence after the last speech: invented
	}
	r.prov.pricePerMinute = 6000
	r.prov.calls = []Call{{AudioMs: 4000}}
	res, err := r.run(t)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != store.TranscriptReady || res.Words != 2 || res.SpeechMs != 1100 || res.AudioMs != 4000 {
		t.Fatalf("%+v", res)
	}
	if r.prov.runs != 1 || r.st.saved == nil || !r.st.saved.Speech || r.st.saved.Language != "en" || r.st.saved.Provider != "fake" {
		t.Fatalf("provider runs %d, saved %+v", r.prov.runs, r.st.saved)
	}
	if r.st.saved.SourceIngestedAt == nil || !r.st.saved.SourceIngestedAt.Equal(*r.st.asset.IngestedAt) {
		t.Errorf("the transcript does not name the ingest it was made from: %v", r.st.saved.SourceIngestedAt)
	}
	var words []Word
	if err := json.Unmarshal(r.st.saved.Words, &words); err != nil || len(words) != 2 {
		t.Fatalf("%v %s", err, r.st.saved.Words)
	}
	near(t, "Hello start", words[0].StartMs, 500, 30)
	near(t, "um start", words[1].StartMs, 2000, 30)
	var st Structure
	if err := json.Unmarshal(r.st.saved.Structure, &st); err != nil || len(st.Fillers) != 1 || st.Fillers[0].Text != "um" {
		t.Fatalf("%v %s", err, r.st.saved.Structure)
	}
	if len(r.usage()) != 1 {
		t.Fatalf("ledger rows %+v", r.usage())
	}
	u := r.usage()[0]
	if u.Kind != store.KindTranscription || u.Stage != "transcription" || u.Provider != "fake" || u.AudioMs != 4000 || u.ListCostMicros != 400 || u.PriceTableVersion != "fake-prices" || u.BilledTo != store.BilledToTest || u.WorkspaceID != r.ws {
		t.Fatalf("ledger row %+v", u)
	}
	if u.Unit != store.UnitAudioMs || u.Quantity != 4000 || u.IdempotencyKey == "" || u.Estimated {
		t.Errorf("ledger row quantity %d %s key %q estimated %v", u.Quantity, u.Unit, u.IdempotencyKey, u.Estimated)
	}
	if r.mem.Reserves != 1 || r.mem.Settles != 1 || len(r.mem.Holds()) != 0 {
		t.Errorf("the worst case must be reserved once and settled with the rows: reserves %d settles %d holds %d", r.mem.Reserves, r.mem.Settles, len(r.mem.Holds()))
	}
	if len(r.obj.keys) != 1 {
		t.Errorf("fetched %v", r.obj.keys)
	}
}

func TestRunSkipsTheProviderWithoutSpeech(t *testing.T) {
	r := newRig(t)
	r.det.segs = []Segment{{StartMs: 100, EndMs: 400}} // 300 ms: a cough
	res, err := r.run(t)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != store.TranscriptNoSpeech || r.prov.runs != 0 {
		t.Fatalf("%+v, %d provider runs", res, r.prov.runs)
	}
	if r.st.saved == nil || r.st.saved.Speech || len(r.usage()) != 0 || r.mem.Reserves != 0 {
		t.Fatalf("saved %+v, usage %+v: a clip without speech is recorded and not metered", r.st.saved, r.usage())
	}
}

func TestRunMetersAProviderThatFindsNoWordsInTheSpeech(t *testing.T) {
	r := newRig(t)
	r.prov.words = nil
	res, err := r.run(t)
	if err != nil || res.Status != store.TranscriptNoSpeech {
		t.Fatalf("%+v %v", res, err)
	}
	if len(r.usage()) != 1 || r.st.saved.Speech {
		t.Fatalf("the call was made and is metered: saved %+v usage %+v", r.st.saved, r.usage())
	}
}

func TestRunWritesARowPerChunkAndKeepsPaidWorkWhenALaterChunkFails(t *testing.T) {
	r := newRig(t)
	r.prov.pricePerMinute = 6000
	r.prov.calls = []Call{{AudioMs: 2000}, {AudioMs: 1000}} // two chunks were answered (and billed), the third failed
	r.prov.err = errors.New("workers ai: 500")
	if _, err := r.run(t); err == nil {
		t.Fatal("the failure was swallowed")
	}
	if len(r.usage()) != 2 || r.usage()[0].AudioMs != 2000 || r.usage()[1].AudioMs != 1000 || r.usage()[0].ListCostMicros != 200 || r.usage()[1].ListCostMicros != 100 {
		t.Fatalf("rows %+v: paid chunks must be in the ledger even though the run failed", r.usage())
	}
	if r.st.saved != nil {
		t.Error("a transcript was saved from a failed run")
	}
	if len(r.mem.Holds()) != 0 || r.mem.Releases != 0 {
		t.Errorf("the reservation must be settled by the paid rows (holds left %d, releases %d)", len(r.mem.Holds()), r.mem.Releases)
	}
	// A provider that fails before answering anything bills nothing.
	r = newRig(t)
	r.prov.err = errors.New("dial")
	if _, err := r.run(t); err == nil || len(r.usage()) != 0 || len(r.mem.Holds()) != 0 || r.mem.Releases != 1 {
		t.Fatalf("%v %+v holds %d releases %d: nothing billed, so the hold goes back", err, r.usage(), len(r.mem.Holds()), r.mem.Releases)
	}
}

func TestRunRefusesWhenTheDailySpendCapCannotCoverTheCall(t *testing.T) {
	r := newRig(t)
	r.prov.pricePerMinute = 6_000_000 // $6 per minute: a 4 s clip reserves $0.40
	r.spendCap = 100_000
	res, err := r.run(t)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != store.TranscriptFailed || res.Reason != store.TranscriptSpendCap || r.prov.runs != 0 || len(r.usage()) != 0 || r.st.saved != nil {
		t.Fatalf("%+v, provider runs %d, rows %+v", res, r.prov.runs, r.usage())
	}
	if len(r.st.failures) != 1 || r.st.failures[0].reason != store.TranscriptSpendCap || r.st.failures[0].source == nil {
		t.Fatalf("failures %+v: the reason must be recorded for this ingest", r.st.failures)
	}
}

func TestRunFailsClosedWhenTheSpendCapCannotBeChecked(t *testing.T) {
	r := newRig(t)
	r.prov.pricePerMinute = 6000
	r.mem.FailReserve = errors.New("ledger unreachable")
	if _, err := r.run(t); err == nil || r.prov.runs != 0 {
		t.Fatalf("err %v, provider runs %d", err, r.prov.runs)
	}
	if len(r.st.failures) != 0 {
		t.Error("an outage of the ledger must be retried, not recorded as a spend cap")
	}
	// A provider that costs money and no guard at all.
	r = newRig(t)
	r.prov.pricePerMinute = 6000
	r.noGate = true
	if _, err := r.run(t); err == nil || r.prov.runs != 0 {
		t.Fatalf("err %v, provider runs %d", err, r.prov.runs)
	}
}

func TestRunEnforcesTheDailyMinutes(t *testing.T) {
	r := newRig(t)
	r.mins = 1
	r.seedMinutes(57_000) // 57 s of the minute used; this clip is 4 s
	res, err := r.run(t)
	if err != nil || res.Status != store.TranscriptFailed || res.Reason != store.TranscriptMinutesCap || r.prov.runs != 0 {
		t.Fatalf("%+v %v, provider runs %d", res, err, r.prov.runs)
	}
	if len(r.st.failures) != 1 || r.st.failures[0].reason != store.TranscriptMinutesCap {
		t.Fatalf("failures %+v", r.st.failures)
	}
	// Exactly fitting is allowed.
	r = newRig(t)
	r.mins = 1
	r.seedMinutes(56_000)
	r.prov.words = []Word{{Text: "hi", AnchorMs: 900}}
	if res, err := r.run(t); err != nil || res.Status != store.TranscriptReady {
		t.Fatalf("%+v %v", res, err)
	}
	// Runs in flight count, in the database: with 1 minute, a hold of 58 s (another worker's run) leaves no room for this 4 s clip,
	// until it is released.
	r = newRig(t)
	r.mins = 1
	other, err := r.gate().Reserve(context.Background(), spend.Request{Workspace: r.ws, BilledTo: store.BilledToTest, Kind: store.KindTranscription, AudioMs: 58_000})
	if err != nil {
		t.Fatal(err)
	}
	if res, err := r.run(t); err != nil || res.Reason != store.TranscriptMinutesCap {
		t.Errorf("in-flight audio was not counted: %+v %v", res, err)
	}
	if err := other.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.prov.words = []Word{{Text: "hi", AnchorMs: 900}}
	if res, err := r.run(t); err != nil || res.Status != store.TranscriptReady {
		t.Errorf("released audio still counted: %+v %v", res, err)
	}
}

// The platform breaker pauses transcription instead of failing the transcript: nothing is sent, the job waits.
func TestRunWaitsWhileThePlatformBreakerIsOpen(t *testing.T) {
	r := newRig(t)
	r.prov.pricePerMinute = 6_000_000 // a 4 s clip reserves $0.40
	r.platCap = 100_000
	r.mem.Seed(store.UsageEntry{WorkspaceID: ids.WorkspaceID{UUID: uuid.New()}, ListCostMicros: 95_000, BilledTo: store.BilledToPlatform, CreatedAt: testNow.Add(-time.Hour)})
	r.spendCap = 1 << 40
	p, err := New(Config{Store: r.st, Objects: r.obj, Provider: r.prov, Detector: r.det, Gate: r.gate(), BilledTo: store.BilledToPlatform, TempDir: t.TempDir(), now: func() time.Time { return testNow }})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Run(context.Background(), r.ws, r.id, r.gen)
	if !errors.Is(err, ErrPlatformPaused) || !errors.Is(err, spend.ErrPlatformPaused) || r.prov.runs != 0 || len(r.st.failures) != 0 || r.st.saved != nil {
		t.Fatalf("err %v, provider runs %d, failures %+v: the breaker must pause, not fail, the transcript", err, r.prov.runs, r.st.failures)
	}
}

// A chunk whose outcome is unknown after it was sent is a row marked estimated, at the price reserved for its audio; the rows of the
// attempt have stable keys that a retried write reproduces and another attempt does not.
func TestRunWritesAnEstimatedRowForAChunkWithAnUnknownOutcome(t *testing.T) {
	r := newRig(t)
	r.prov.pricePerMinute = 6000
	r.prov.calls = []Call{{AudioMs: 2000}, {AudioMs: 1000, Estimated: true}}
	r.prov.err = errors.New("workers ai: read response: unexpected EOF")
	r.job = &JobRef{ID: 77, Attempt: 2}
	if _, err := r.run(t); err == nil {
		t.Fatal("the failure was swallowed")
	}
	rows := r.usage()
	if len(rows) != 2 || rows[0].Estimated || !rows[1].Estimated || rows[1].AudioMs != 1000 || rows[1].ListCostMicros != 100 {
		t.Fatalf("rows %+v", rows)
	}
	if rows[0].IdempotencyKey != "transcribe:77:2:0" || rows[1].IdempotencyKey != "transcribe:77:2:1" {
		t.Errorf("keys %q %q", rows[0].IdempotencyKey, rows[1].IdempotencyKey)
	}
}

// A settle that wrote its rows and then failed (the answer was lost) is retried by the pipeline: the rows' keys make the retry a no-op,
// so nothing is counted twice.
func TestRunARetriedLedgerWriteDoesNotDoubleCount(t *testing.T) {
	r2 := newRig(t)
	r2.prov.pricePerMinute = 6000
	r2.prov.calls = []Call{{AudioMs: 4000}}
	r2.prov.words = []Word{{Text: "hi", AnchorMs: 900}}
	r2.job = &JobRef{ID: 78, Attempt: 1}
	r2.mem.FailSettleAfterWrite = 1
	if res, err := r2.run(t); err != nil || res.Status != store.TranscriptReady {
		t.Fatalf("%+v %v", res, err)
	}
	if rows := r2.usage(); len(rows) != 1 || r2.mem.Settles != 2 {
		t.Fatalf("a settle whose answer was lost was counted twice: %d rows after %d settles", len(rows), r2.mem.Settles)
	}
	counter := r2.mem.Counter(r2.ws, store.DayPeriod(testNow))
	if counter.TestMicros != 400 {
		t.Errorf("counter %+v", counter)
	}
}

func TestRunDoesNothingForAnIngestThatIsNoLongerTheAssets(t *testing.T) {
	r := newRig(t)
	r.gen += 1000 // the job was made for another generation than the asset has now
	res, err := r.run(t)
	if err != nil || res.Status != StatusStale {
		t.Fatalf("%+v %v", res, err)
	}
	if len(r.obj.keys) != 0 || r.prov.runs != 0 || r.st.saved != nil || len(r.st.statuses) != 0 || len(r.st.failures) != 0 || len(r.usage()) != 0 {
		t.Error("a stale job touched something")
	}
	// An asset that is being ingested again is stale as well.
	r = newRig(t)
	r.st.asset.Status = store.AssetProcessing
	if res, err := r.run(t); err != nil || res.Status != StatusStale {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestRunADroppedSaveIsAStaleRunButStillMetered(t *testing.T) {
	r := newRig(t)
	r.prov.words = []Word{{Text: "hi", AnchorMs: 900}}
	r.st.saveErr = store.ErrStaleTranscript // the asset was ingested again while the provider worked
	res, err := r.run(t)
	if err != nil || res.Status != StatusStale {
		t.Fatalf("%+v %v", res, err)
	}
	if len(r.usage()) != 1 {
		t.Errorf("the call was made and paid for: rows %+v", r.usage())
	}
}

func TestRunAnAssetWithoutAudioHasNothingToTranscribe(t *testing.T) {
	r := newRig(t)
	r.st.derivs = nil
	res, err := r.run(t)
	if err != nil || res.Status != store.TranscriptNone || len(r.st.statuses) != 1 || r.st.statuses[0] != store.TranscriptNone {
		t.Fatalf("%+v %v %v", res, err, r.st.statuses)
	}
	if len(r.obj.keys) != 0 || r.prov.runs != 0 {
		t.Error("it fetched or transcribed")
	}
}

func TestRunRefusesAnAudioKeyOfAnotherAsset(t *testing.T) {
	r := newRig(t)
	other := ids.AssetID{UUID: uuid.New()}
	key, _ := storage.DerivativeKey(r.ws, other, "audio_16k.wav")
	r.st.derivs[0].R2Key = key
	if _, err := r.run(t); err == nil || len(r.obj.keys) != 0 {
		t.Fatalf("err %v, fetched %v", err, r.obj.keys)
	}
}

func TestRunErrors(t *testing.T) {
	r := newRig(t)
	r.st.assetErr = store.ErrNotFound
	if _, err := r.run(t); !errors.Is(err, ErrAssetGone) {
		t.Errorf("gone: %v", err)
	}

	r = newRig(t)
	r.obj.body = []byte("not a wav file at all")
	res, err := r.run(t)
	if err != nil || res.Status != store.TranscriptFailed || len(r.st.failures) != 1 || r.st.failures[0].reason != store.TranscriptUnusableAudio {
		t.Errorf("unusable audio: %+v %v %v", res, err, r.st.failures)
	}

	r = newRig(t)
	r.det.err = errors.New("sandbox down")
	if _, err := r.run(t); err == nil || r.st.saved != nil {
		t.Errorf("detector error: %v", err)
	}

	r = newRig(t)
	r.prov.err = ErrNotConfigured
	if _, err := r.run(t); !errors.Is(err, ErrNotConfigured) || r.st.saved != nil || len(r.usage()) != 0 {
		t.Errorf("not configured: %v", err)
	}

	r = newRig(t)
	r.prov.err = ErrUnusableAudio
	if res, err := r.run(t); err != nil || res.Status != store.TranscriptFailed {
		t.Errorf("provider refuses the audio: %+v %v", res, err)
	}

	r = newRig(t)
	r.prov.words = []Word{{Text: "hi", AnchorMs: 900}}
	r.st.saveErr = store.ErrNotFound
	if _, err := r.run(t); !errors.Is(err, ErrAssetGone) {
		t.Errorf("deleted while working: %v", err)
	}

	// A ledger that cannot be written does not throw away a paid result (a retry would pay for the audio again): the row is
	// logged for reconciliation and the transcript is saved.
	r = newRig(t)
	r.prov.words = []Word{{Text: "hi", AnchorMs: 900}}
	r.mem.FailSettle = 100
	if res, err := r.run(t); err != nil || res.Status != store.TranscriptReady || r.st.saved == nil {
		t.Errorf("a ledger outage lost the transcript: %+v %v", res, err)
	}

	// A provider that is switched off waits before anything is fetched or limited, even for a workspace over its minutes.
	r = newRig(t)
	r.mins = 1
	r.seedMinutes(59_000)
	off := &offProvider{fakeProvider: r.prov}
	if _, err := r.pipelineFor(t, off).Run(context.Background(), r.ws, r.id, r.gen); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("a switched-off provider: %v", err)
	}
	if len(r.obj.keys) != 0 || len(r.st.failures) != 0 || r.prov.runs != 0 {
		t.Errorf("a switched-off provider fetched audio or failed the transcript: keys %v, failures %+v", r.obj.keys, r.st.failures)
	}
}

func TestFailMarksFailedForThatIngestOnly(t *testing.T) {
	r := newRig(t)
	p := r.pipeline(t)
	if err := p.Fail(context.Background(), r.ws, r.id, r.gen); err != nil || len(r.st.failures) != 1 || r.st.failures[0].reason != store.TranscriptGaveUp {
		t.Fatalf("%v %+v", err, r.st.failures)
	}
	if s := r.st.failures[0].source; s == nil || store.IngestGeneration(store.Asset{IngestedAt: s}) != r.gen {
		t.Errorf("the failure names ingest %v, want generation %d", s, r.gen)
	}
	r.st.statusErr = store.ErrNotFound // deleted or ingested again since
	if err := p.Fail(context.Background(), r.ws, r.id, r.gen); err != nil {
		t.Fatal(err)
	}
}

func TestNewChecksItsConfig(t *testing.T) {
	r := newRig(t)
	for name, cfg := range map[string]Config{
		"nothing":       {},
		"temp dir":      {Store: r.st, Objects: r.obj, Provider: r.prov, Detector: r.det, TempDir: "relative/dir"},
		"no provider":   {Store: r.st, Objects: r.obj, Detector: r.det, TempDir: t.TempDir()},
		"no store":      {Objects: r.obj, Provider: r.prov, Detector: r.det, TempDir: t.TempDir()},
		"no detector":   {Store: r.st, Objects: r.obj, Provider: r.prov, TempDir: t.TempDir()},
		"no objects":    {Store: r.st, Provider: r.prov, Detector: r.det, TempDir: t.TempDir()},
		"unclean path":  {Store: r.st, Objects: r.obj, Provider: r.prov, Detector: r.det, TempDir: t.TempDir() + "/../x"},
		"empty tempdir": {Store: r.st, Objects: r.obj, Provider: r.prov, Detector: r.det},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// A transcription that does not fit under the platform cap while the breaker is not tripped is busy: the job waits a short while
// (ErrPlatformBusy), nothing is sent and the transcript is not failed.
func TestRunWaitsABriefWhileWhenThePlatformCapIsOnlyBusy(t *testing.T) {
	r := newRig(t)
	r.prov.pricePerMinute = 6_000_000 // a 4 s clip reserves $0.40
	r.platCap = 1_000_000
	other, err := r.gate().Reserve(context.Background(), spend.Request{Workspace: ids.WorkspaceID{UUID: uuid.New()}, BilledTo: store.BilledToPlatform, Kind: store.KindLLM, Micros: 700_000})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Release(context.Background()) }()
	p, err := New(Config{Store: r.st, Objects: r.obj, Provider: r.prov, Detector: r.det, Gate: r.gate(), BilledTo: store.BilledToPlatform, TempDir: t.TempDir(), now: func() time.Time { return testNow }})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Run(context.Background(), r.ws, r.id, r.gen)
	if !errors.Is(err, ErrPlatformBusy) || !errors.Is(err, spend.ErrSpendCapBusy) || errors.Is(err, ErrPlatformPaused) || r.prov.runs != 0 || len(r.st.failures) != 0 {
		t.Fatalf("err %v, provider runs %d, failures %+v", err, r.prov.runs, r.st.failures)
	}
}
