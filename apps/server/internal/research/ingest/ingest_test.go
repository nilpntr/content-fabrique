package ingest_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/research"
	"github.com/nilpntr/content-fabrique/internal/research/ingest"
	"github.com/nilpntr/content-fabrique/internal/research/oembed"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// fakeDB keeps the research rows in memory and applies the same package research checks the store does (the store's own tests cover
// the database side). No Docker, no network.
type fakeDB struct {
	creators    map[string]store.ResearchCreator
	forgotten   map[string]bool
	posts       map[string]store.ResearchPost
	evidence    map[uuid.UUID][]store.ResearchEvidence
	annotations map[string]research.Annotation
	failTx      bool
}

func newFakeDB() *fakeDB {
	return &fakeDB{
		creators: map[string]store.ResearchCreator{}, forgotten: map[string]bool{}, posts: map[string]store.ResearchPost{},
		evidence: map[uuid.UUID][]store.ResearchEvidence{}, annotations: map[string]research.Annotation{},
	}
}

func (f *fakeDB) InTx(_ context.Context, fn func(ingest.Writer) error) error {
	if f.failTx {
		return errors.New("tx failed")
	}
	return fn(f)
}

func (f *fakeDB) GetResearchCreator(_ context.Context, p research.Platform, handle string) (store.ResearchCreator, error) {
	c, ok := f.creators[string(p)+":"+handle]
	if !ok {
		return c, store.ErrNotFound
	}
	return c, nil
}

func (f *fakeDB) UpsertResearchCreator(_ context.Context, in store.NewResearchCreator) (store.ResearchCreator, error) {
	key := string(in.Platform) + ":" + in.Handle
	if f.forgotten[key] {
		return store.ResearchCreator{}, store.ErrForgotten
	}
	c := store.ResearchCreator{ID: uuid.New(), Platform: string(in.Platform), Handle: in.Handle, Status: store.ResearchCandidate}
	f.creators[key] = c
	return c, nil
}

func (f *fakeDB) UpsertResearchPost(_ context.Context, in store.NewResearchPost) (store.ResearchPost, error) {
	ref, err := research.ParsePostURL(in.URL)
	if err != nil {
		return store.ResearchPost{}, err
	}
	if err := research.CheckPostTime(in.PostedAt, in.PostedAtSource, time.Now()); err != nil {
		return store.ResearchPost{}, err
	}
	p, ok := f.posts[ref.URL]
	if !ok {
		p = store.ResearchPost{ID: uuid.New(), Platform: string(ref.Platform), Url: ref.URL, ExternalID: ref.ExternalID, CreatorID: in.CreatorID}
	}
	if in.PostedAt != nil {
		src := in.PostedAtSource
		p.PostedAt, p.PostedAtSource = in.PostedAt, &src
	}
	if in.DurationS != nil {
		p.DurationS = in.DurationS
	}
	f.posts[ref.URL] = p
	return p, nil
}

func (f *fakeDB) ListResearchEvidenceForPost(_ context.Context, postID uuid.UUID) ([]store.ResearchEvidence, error) {
	return f.evidence[postID], nil
}

func (f *fakeDB) AddResearchEvidence(_ context.Context, in store.NewResearchEvidence) (store.ResearchEvidence, error) {
	var platform research.Platform
	for _, p := range f.posts {
		if p.ID == in.PostID {
			platform = research.Platform(p.Platform)
		}
	}
	if err := research.CheckObservation(in.Source, in.Method, platform, in.Metrics); err != nil {
		return store.ResearchEvidence{}, err
	}
	m, _ := json.Marshal(in.Metrics)
	ref := in.RawRef
	e := store.ResearchEvidence{ID: uuid.New(), Source: string(in.Source), Method: string(in.Method), ObservedAt: in.ObservedAt,
		Observer: in.Observer, Metrics: m, RawRef: &ref}
	f.evidence[in.PostID] = append(f.evidence[in.PostID], e)
	return e, nil
}

func (f *fakeDB) SaveResearchAnnotation(_ context.Context, postID uuid.UUID, a research.Annotation) (store.ResearchAnnotation, error) {
	if err := research.DefaultVocabulary().CheckAnnotation(a); err != nil {
		return store.ResearchAnnotation{}, err
	}
	f.annotations[postID.String()+":"+a.Annotator] = a
	return store.ResearchAnnotation{}, nil
}

func (f *fakeDB) observations() int {
	n := 0
	for _, list := range f.evidence {
		n += len(list)
	}
	return n
}

// countingResolver counts the oEmbed calls.
type countingResolver struct {
	inner *oembed.Client
	calls int
	err   error
}

func (r *countingResolver) Resolve(ctx context.Context, link string) (oembed.Result, error) {
	r.calls++
	if r.err != nil {
		return oembed.Result{}, r.err
	}
	return r.inner.Resolve(ctx, link)
}

func recordedSheet(t *testing.T) []research.SheetRow {
	t.Helper()
	raw, err := os.ReadFile("../testdata/r2/watch-sheet.csv")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := research.ParseWatchSheet("watch-sheet.csv", raw, research.SheetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func replayResolver(t *testing.T) *countingResolver {
	t.Helper()
	c, err := oembed.New(oembed.Config{Mode: oembed.Replay, Dir: "../testdata/r2/oembed"})
	if err != nil {
		t.Fatal(err)
	}
	return &countingResolver{inner: c}
}

// The R2 acceptance on recorded fixtures: 30 posts from 5 creators entered and resolved, the post times from the ids agree with the
// shown dates on the 10 rows that carry one, and a second import stores nothing twice.
func TestImportTheRecordedSheet(t *testing.T) {
	ctx := context.Background()
	db := newFakeDB()
	rows := recordedSheet(t)
	res := replayResolver(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	rep, err := ingest.Import(ctx, db, rows, ingest.Options{Resolver: res, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Failed) != 0 {
		t.Fatalf("failed rows: %v", rep.Failed)
	}
	if rep.Rows != 30 || rep.Imported != 30 || rep.NewCreators != 5 || rep.Resolved != 30 || len(db.creators) != 5 || len(db.posts) != 30 {
		t.Fatalf("report %+v; %d creators, %d posts", rep, len(db.creators), len(db.posts))
	}
	if rep.TimeChecks != 10 || rep.TimeMatches != 10 || len(rep.Warnings) != 0 {
		t.Fatalf("post times: %d of %d match; %v", rep.TimeMatches, rep.TimeChecks, rep.Warnings)
	}
	// One watch-sheet and one oEmbed observation per post, and the person's and the caption's annotation.
	if rep.Observations != 60 || db.observations() != 60 || rep.Annotations != 60 || len(db.annotations) != 60 {
		t.Fatalf("observations %d (%d stored), annotations %d (%d stored)", rep.Observations, db.observations(), rep.Annotations, len(db.annotations))
	}
	for _, row := range rows {
		p := db.posts[row.Post.URL]
		want, _ := research.TikTokPostTime(row.Post.ExternalID)
		if p.PostedAt == nil || !p.PostedAt.Equal(want) || *p.PostedAtSource != "id" || p.DurationS == nil {
			t.Fatalf("post %s: %+v", row.Post.URL, p)
		}
		for _, e := range db.evidence[p.ID] {
			switch e.Source {
			case "watch_sheet":
				if e.Method != "manual" || e.Observer != "sam" || !strings.Contains(string(e.Metrics), "saves") || e.ObservedAt.Equal(now) {
					t.Fatalf("sheet observation: %+v", e)
				}
			case "oembed":
				if e.Method != "api" || string(e.Metrics) != "null" && string(e.Metrics) != "{}" || len(*e.RawRef) != 64 {
					t.Fatalf("oembed observation: %+v %s", e, e.Metrics)
				}
			default:
				t.Fatalf("source %s", e.Source)
			}
		}
		capt := db.annotations[p.ID.String()+":oembed"]
		if capt.CaptionLen == nil || *capt.CaptionLen == 0 || capt.HashtagsN == nil || *capt.HashtagsN == 0 {
			t.Fatalf("caption stats: %+v", capt)
		}
	}

	again, err := ingest.Import(ctx, db, rows, ingest.Options{Resolver: res, Now: func() time.Time { return now.Add(time.Hour) }})
	if err != nil || again.Observations != 0 || again.Duplicates != 60 || again.NewCreators != 0 || db.observations() != 60 {
		t.Fatalf("second import: %+v %v", again, err)
	}
}

func TestImportRespectsTombstonesAndStopsOn429(t *testing.T) {
	ctx := context.Background()
	rows := recordedSheet(t)

	// A forgotten creator is refused before TikTok is asked anything; the other creators go on.
	db := newFakeDB()
	db.forgotten["tiktok:synthetic_lifter_b"] = true
	res := replayResolver(t)
	rep, err := ingest.Import(ctx, db, rows, ingest.Options{Resolver: res})
	if err != nil || len(rep.Failed) != 6 || rep.Imported != 24 || res.calls != 24 {
		t.Fatalf("forgotten: %+v %v, %d oEmbed calls", rep, err, res.calls)
	}
	for _, f := range rep.Failed {
		if !errors.Is(f.Err, store.ErrForgotten) {
			t.Fatalf("failure: %v", f)
		}
	}

	// A dropped creator is not sampled.
	db = newFakeDB()
	db.creators["tiktok:synthetic_lifter_a"] = store.ResearchCreator{ID: uuid.New(), Platform: "tiktok", Handle: "synthetic_lifter_a", Status: store.ResearchDropped}
	rep, err = ingest.Import(ctx, db, rows[:6], ingest.Options{Resolver: replayResolver(t)})
	if err != nil || len(rep.Failed) != 6 || !strings.Contains(rep.Failed[0].Error(), "dropped") {
		t.Fatalf("dropped: %+v %v", rep, err)
	}

	// TikTok's 429 stops the import at once.
	db = newFakeDB()
	limited := &countingResolver{err: oembed.ErrRateLimited}
	rep, err = ingest.Import(ctx, db, rows, ingest.Options{Resolver: limited})
	if !errors.Is(err, oembed.ErrRateLimited) || limited.calls != 1 || rep.Imported != 0 || len(db.posts) != 0 {
		t.Fatalf("429: %+v %v, %d calls", rep, err, limited.calls)
	}

	// The reserved annotator name.
	row := rows[0]
	row.Observer = ingest.OEmbedAnnotator
	rep, _ = ingest.Import(ctx, newFakeDB(), []research.SheetRow{row}, ingest.Options{})
	if len(rep.Failed) != 1 {
		t.Fatalf("observer oembed: %+v", rep)
	}
}

func TestImportWithoutOEmbedAndOtherPlatforms(t *testing.T) {
	sheet := "url,handle,observer,posted_at,views,likes,saves\n" +
		"https://www.instagram.com/reel/AbCdEf123/,lifter_y,sam,2026-11-01,12K,800,95\n" +
		"https://www.tiktok.com/@synthetic_lifter_a/video/7657610837863983488,,sam,2026-03-01,5000,,\n"
	rows, err := research.ParseWatchSheet("s.csv", []byte(sheet), research.SheetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	db := newFakeDB()
	rep, err := ingest.Import(context.Background(), db, rows, ingest.Options{})
	if err != nil || rep.Imported != 2 || rep.Resolved != 0 || rep.Observations != 2 || rep.Annotations != 0 {
		t.Fatalf("report %+v %v", rep, err)
	}
	ig := db.posts["https://www.instagram.com/reel/AbCdEf123/"]
	if ig.PostedAt == nil || *ig.PostedAtSource != "manual" {
		t.Fatalf("instagram post: %+v", ig)
	}
	// The TikTok row's shown date disagrees with its id: a warning, and the id's time is kept.
	if rep.TimeChecks != 1 || rep.TimeMatches != 0 || len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "2026-07-01") {
		t.Fatalf("time check: %+v", rep)
	}
}

func TestCaptionStats(t *testing.T) {
	n, tags := ingest.CaptionStats("Rücken 101 #gym #rückentraining #fit_tok, no tag here #")
	if n != 55 || tags != 3 {
		t.Fatalf("CaptionStats = %d, %d", n, tags)
	}
}
