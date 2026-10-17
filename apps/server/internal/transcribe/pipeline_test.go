package transcribe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

type fakeStore struct {
	asset     store.Asset
	assetErr  error
	derivs    []store.Derivative
	statuses  []string
	saved     *store.NewTranscript
	usage     *store.UsageEntry
	saveErr   error
	statusErr error
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

func (f *fakeStore) SaveTranscript(_ context.Context, _ ids.WorkspaceID, _ ids.AssetID, t store.NewTranscript, u *store.UsageEntry) (store.Transcript, error) {
	if f.saveErr != nil {
		return store.Transcript{}, f.saveErr
	}
	f.saved, f.usage = &t, u
	return store.Transcript{}, nil
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
	err   error
	calls int
}

func (p *fakeProvider) Name() string  { return "fake" }
func (p *fakeProvider) Model() string { return "fake-model" }
func (p *fakeProvider) CostMicros(ms int64) (int64, string) {
	return ms / 10, "fake-prices"
}

func (p *fakeProvider) Transcribe(context.Context, Audio, Options) (Transcript, error) {
	p.calls++
	return Transcript{Language: "en", Words: p.words}, p.err
}

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
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{ws: ids.WorkspaceID{UUID: uuid.New()}, id: ids.AssetID{UUID: uuid.New()}, prov: &fakeProvider{}}
	key, err := storage.DerivativeKey(r.ws, r.id, "audio_16k.wav")
	if err != nil {
		t.Fatal(err)
	}
	r.st = &fakeStore{derivs: []store.Derivative{{Kind: store.DerivativeAudio16k, R2Key: key}}}
	// 4 s of audio: speech-like bursts at 0.5-1.0 s and 2.0-2.6 s.
	r.obj = &fakeObjects{body: EncodeWAV(speechLike(4000, [][2]int64{{500, 1000}, {2000, 2600}}))}
	r.det = fakeDetector{segs: []Segment{{StartMs: 500, EndMs: 1000}, {StartMs: 2000, EndMs: 2600}}}
	return r
}

func (r *rig) pipeline(t *testing.T) *Pipeline {
	t.Helper()
	p, err := New(Config{Store: r.st, Objects: r.obj, Provider: r.prov, Detector: r.det, BilledTo: store.BilledToTest, TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunTranscribesSpeechAndMetersIt(t *testing.T) {
	r := newRig(t)
	r.prov.words = []Word{
		{Text: "Hello,", AnchorMs: 900, Confidence: 0.9}, {Text: "um", AnchorMs: 2400},
		{Text: "ghost", AnchorMs: 3500}, // in the silence after the last speech: invented
	}
	res, err := r.pipeline(t).Run(context.Background(), r.ws, r.id)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != store.TranscriptReady || res.Words != 2 || res.SpeechMs != 1100 || res.AudioMs != 4000 {
		t.Fatalf("%+v", res)
	}
	if r.prov.calls != 1 || r.st.saved == nil || !r.st.saved.Speech || r.st.saved.Language != "en" || r.st.saved.Provider != "fake" {
		t.Fatalf("provider calls %d, saved %+v", r.prov.calls, r.st.saved)
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
	u := r.st.usage
	if u == nil || u.Stage != "transcription" || u.Provider != "fake" || u.AudioMs != 4000 || u.ListCostMicros != 400 || u.PriceTableVersion != "fake-prices" || u.BilledTo != store.BilledToTest {
		t.Fatalf("ledger row %+v", u)
	}
	if len(r.obj.keys) != 1 {
		t.Errorf("fetched %v", r.obj.keys)
	}
}

func TestRunSkipsTheProviderWithoutSpeech(t *testing.T) {
	r := newRig(t)
	r.det.segs = []Segment{{StartMs: 100, EndMs: 400}} // 300 ms: a cough
	res, err := r.pipeline(t).Run(context.Background(), r.ws, r.id)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != store.TranscriptNoSpeech || r.prov.calls != 0 {
		t.Fatalf("%+v, %d provider calls", res, r.prov.calls)
	}
	if r.st.saved == nil || r.st.saved.Speech || r.st.usage != nil {
		t.Fatalf("saved %+v, usage %+v: a clip without speech is recorded and not metered", r.st.saved, r.st.usage)
	}
}

func TestRunMetersAProviderThatFindsNoWordsInTheSpeech(t *testing.T) {
	r := newRig(t)
	r.prov.words = nil
	res, err := r.pipeline(t).Run(context.Background(), r.ws, r.id)
	if err != nil || res.Status != store.TranscriptNoSpeech {
		t.Fatalf("%+v %v", res, err)
	}
	if r.st.usage == nil || r.st.saved.Speech {
		t.Fatalf("the call was made and is metered: saved %+v usage %+v", r.st.saved, r.st.usage)
	}
}

func TestRunAnAssetWithoutAudioHasNothingToTranscribe(t *testing.T) {
	r := newRig(t)
	r.st.derivs = nil
	res, err := r.pipeline(t).Run(context.Background(), r.ws, r.id)
	if err != nil || res.Status != store.TranscriptNone || len(r.st.statuses) != 1 || r.st.statuses[0] != store.TranscriptNone {
		t.Fatalf("%+v %v %v", res, err, r.st.statuses)
	}
	if len(r.obj.keys) != 0 || r.prov.calls != 0 {
		t.Error("it fetched or transcribed")
	}
}

func TestRunRefusesAnAudioKeyOfAnotherAsset(t *testing.T) {
	r := newRig(t)
	other := ids.AssetID{UUID: uuid.New()}
	key, _ := storage.DerivativeKey(r.ws, other, "audio_16k.wav")
	r.st.derivs[0].R2Key = key
	if _, err := r.pipeline(t).Run(context.Background(), r.ws, r.id); err == nil || len(r.obj.keys) != 0 {
		t.Fatalf("err %v, fetched %v", err, r.obj.keys)
	}
}

func TestRunErrors(t *testing.T) {
	ctx := context.Background()

	r := newRig(t)
	r.st.assetErr = store.ErrNotFound
	if _, err := r.pipeline(t).Run(ctx, r.ws, r.id); !errors.Is(err, ErrAssetGone) {
		t.Errorf("gone: %v", err)
	}

	r = newRig(t)
	r.obj.body = []byte("not a wav file at all")
	res, err := r.pipeline(t).Run(ctx, r.ws, r.id)
	if err != nil || res.Status != store.TranscriptFailed || r.st.statuses[len(r.st.statuses)-1] != store.TranscriptFailed {
		t.Errorf("unusable audio: %+v %v %v", res, err, r.st.statuses)
	}

	r = newRig(t)
	r.det.err = errors.New("sandbox down")
	if _, err := r.pipeline(t).Run(ctx, r.ws, r.id); err == nil || r.st.saved != nil {
		t.Errorf("detector error: %v", err)
	}

	r = newRig(t)
	r.prov.err = ErrNotConfigured
	if _, err := r.pipeline(t).Run(ctx, r.ws, r.id); !errors.Is(err, ErrNotConfigured) || r.st.saved != nil {
		t.Errorf("not configured: %v", err)
	}

	r = newRig(t)
	r.prov.err = ErrUnusableAudio
	if res, err := r.pipeline(t).Run(ctx, r.ws, r.id); err != nil || res.Status != store.TranscriptFailed {
		t.Errorf("provider refuses the audio: %+v %v", res, err)
	}

	r = newRig(t)
	r.prov.words = []Word{{Text: "hi", AnchorMs: 900}}
	r.st.saveErr = store.ErrNotFound
	if _, err := r.pipeline(t).Run(ctx, r.ws, r.id); !errors.Is(err, ErrAssetGone) {
		t.Errorf("deleted while working: %v", err)
	}
}

func TestFailMarksFailedAndForgivesADeletedAsset(t *testing.T) {
	r := newRig(t)
	p := r.pipeline(t)
	if err := p.Fail(context.Background(), r.ws, r.id); err != nil || r.st.statuses[0] != store.TranscriptFailed {
		t.Fatalf("%v %v", err, r.st.statuses)
	}
	r.st.statusErr = store.ErrNotFound
	if err := p.Fail(context.Background(), r.ws, r.id); err != nil {
		t.Fatal(err)
	}
}

func TestNewChecksItsConfig(t *testing.T) {
	r := newRig(t)
	for name, cfg := range map[string]Config{
		"nothing":  {},
		"temp dir": {Store: r.st, Objects: r.obj, Provider: r.prov, Detector: r.det, TempDir: "relative/dir"},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
