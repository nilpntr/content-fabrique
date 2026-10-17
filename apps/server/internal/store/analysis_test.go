package store_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// analysisSuite is part of repositorySuite (superuser and app role): the shots, moments and signals of an analysis are stored as
// a whole, replaced by a re-run, ranked for the editor, and never cross a workspace.
func analysisSuite(t *testing.T, st *store.Store, suffix string) {
	ctx := context.Background()
	mk := func(name string) (store.Workspace, ids.UserID) {
		t.Helper()
		w, err := st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_analysis_" + name + "_" + suffix, Name: name})
		if err != nil {
			t.Fatal(err)
		}
		u, err := st.UpsertUserByClerkID(ctx, "user_analysis_"+name+"_"+suffix, name+"-analysis@example.com")
		if err != nil {
			t.Fatal(err)
		}
		return w, u.ID
	}
	ready := func(w store.Workspace, user ids.UserID, name string) store.Asset {
		t.Helper()
		id, err := st.NewAssetID(ctx)
		if err != nil {
			t.Fatal(err)
		}
		a, _, err := st.CreateAssetWithUpload(ctx, w.ID, store.NewAsset{
			ID: id, Kind: store.AssetKindVideo, Filename: name + ".mp4", ContentType: "video/mp4", SizeBytes: 40 << 20,
			Title: name, R2Key: "ws/" + w.ID.String() + "/assets/" + id.String() + "/original", CreatedBy: user,
			R2UploadID: "r2-" + id.String(), PartSize: 16 << 20, PartCount: 3, ExpiresAt: time.Now().Add(time.Hour),
		}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.InTx(ctx, func(tx *store.Store) error { _, err := tx.CompleteUpload(ctx, w.ID, a.ID); return err }); err != nil {
			t.Fatal(err)
		}
		if _, err := st.ClaimAssetForIngest(ctx, w.ID, a.ID); err != nil {
			t.Fatal(err)
		}
		done, err := st.FinishAssetIngest(ctx, w.ID, a.ID, store.IngestResult{DurationMs: 60000, Width: 720, Height: 1280})
		if err != nil {
			t.Fatal(err)
		}
		return done
	}
	wa, ua := mk("a")
	wb, ub := mk("b")

	result := func(a store.Asset, reps int) store.AnalysisResult {
		r := store.AnalysisResult{
			Version: 1, SourceIngestedAt: a.IngestedAt, StepMs: 100, DurationMs: 60000, Series: store.PackedSeries{Scales: []byte(`{"scale":{}}`), Motion: []byte{1, 0, 2, 0, 3, 0}, Luma: []byte{1, 2, 3}, Contrast: []byte{1, 2, 3}, High: []byte{1, 2, 3}, Sharp: []byte{1, 0, 2, 0, 3, 0}, Scene: []byte{0, 0, 0, 0, 0, 0}}, Summary: []byte(`{"reps":3}`),
			Shots: []store.NewShot{{Idx: 0, StartMs: 0, EndMs: 60000, Signals: []byte(`{"exposure":{"level":"dark"}}`)}},
			Moments: []store.NewMoment{
				{Kind: store.MomentShot, StartMs: 0, EndMs: 60000, Score: 0.5, Attrs: []byte(`{}`), Description: "shot 1"},
				{Kind: store.MomentDead, StartMs: 0, EndMs: 5000, Score: 0, Attrs: []byte(`{"reason":"rest"}`), Description: "dead time"},
				{Kind: store.MomentHold, StartMs: 50000, EndMs: 58000, Score: 0.6, Labels: []string{"pose"}, Description: "hold"},
			},
		}
		for i := 0; i < reps; i++ {
			r.Moments = append(r.Moments, store.NewMoment{
				Kind: store.MomentRep, StartMs: 6000 + i*3000, EndMs: 9000 + i*3000, Score: 0.5 + float64(i)/10,
				Labels: []string{"row"}, Attrs: []byte(`{"rep":1}`), Description: "a rep",
			})
		}
		return r
	}

	t.Run("save, read, replace", func(t *testing.T) {
		a := ready(wa, ua, "analysis-clip")
		if err := st.SaveAnalysis(ctx, wa.ID, a.ID, result(a, 3)); err != nil {
			t.Fatal(err)
		}
		sig, err := st.GetAssetSignals(ctx, wa.ID, a.ID)
		if err != nil || sig.Version != 1 || sig.StepMs != 100 || sig.DurationMs != 60000 {
			t.Fatalf("signals %+v %v", sig, err)
		}
		shots, _ := st.ListAssetShots(ctx, wa.ID, a.ID)
		moments, _ := st.ListAssetMoments(ctx, wa.ID, a.ID)
		if len(shots) != 1 || len(moments) != 6 {
			t.Fatalf("%d shots, %d moments", len(shots), len(moments))
		}
		sums, err := st.ListAssetAnalysisSummaries(ctx, wa.ID, []ids.AssetID{a.ID})
		if err != nil || len(sums) != 1 || string(sums[0].Summary) != `{"reps": 3}` {
			t.Fatalf("summaries %+v %v", sums, err)
		}

		// ranked: reps and holds by default, best first; kinds, min score and limit filter
		top, err := st.ListMoments(ctx, wa.ID, store.MomentFilter{Assets: []ids.AssetID{a.ID}, Limit: 10})
		if err != nil || len(top) != 4 || top[0].Score < top[1].Score || top[0].Kind == store.MomentDead || top[0].Kind == store.MomentShot {
			t.Fatalf("top %+v %v", top, err)
		}
		reps, _ := st.ListMoments(ctx, wa.ID, store.MomentFilter{Assets: []ids.AssetID{a.ID}, Kinds: []string{store.MomentRep}, MinScore: 0.65, Limit: 1})
		if len(reps) != 1 || reps[0].Kind != store.MomentRep || reps[0].Score < 0.65 {
			t.Fatalf("reps %+v", reps)
		}
		if _, err := st.ListMoments(ctx, wa.ID, store.MomentFilter{Assets: []ids.AssetID{a.ID}, Limit: 0}); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("limit 0: %v", err)
		}

		// a re-run replaces the shots and the moments and keeps one signals row
		if err := st.SaveAnalysis(ctx, wa.ID, a.ID, result(a, 1)); err != nil {
			t.Fatal(err)
		}
		if shots, _ = st.ListAssetShots(ctx, wa.ID, a.ID); len(shots) != 1 {
			t.Fatalf("%d shots after a re-run", len(shots))
		}
		if moments, _ = st.ListAssetMoments(ctx, wa.ID, a.ID); len(moments) != 4 {
			t.Fatalf("%d moments after a re-run, want 4", len(moments))
		}
	})

	t.Run("tenancy", func(t *testing.T) {
		a := ready(wa, ua, "analysis-tenancy")
		if err := st.SaveAnalysis(ctx, wb.ID, a.ID, result(a, 2)); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("workspace B saved an analysis on A's asset: %v", err)
		}
		if err := st.SaveAnalysis(ctx, wa.ID, a.ID, result(a, 2)); err != nil {
			t.Fatal(err)
		}
		if _, err := st.GetAssetSignals(ctx, wb.ID, a.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("B read A's signals: %v", err)
		}
		if m, _ := st.ListAssetMoments(ctx, wb.ID, a.ID); len(m) != 0 {
			t.Fatalf("B read A's moments: %v", m)
		}
		if m, _ := st.ListMoments(ctx, wb.ID, store.MomentFilter{Assets: []ids.AssetID{a.ID}, Limit: 10}); len(m) != 0 {
			t.Fatalf("B searched A's moments: %v", m)
		}
		if s, _ := st.ListAssetAnalysisSummaries(ctx, wb.ID, []ids.AssetID{a.ID}); len(s) != 0 {
			t.Fatalf("B read A's summary: %v", s)
		}
		// B's own asset stays empty, and a deleted asset takes no analysis
		b := ready(wb, ub, "analysis-b")
		if m, _ := st.ListMoments(ctx, wa.ID, store.MomentFilter{Assets: []ids.AssetID{b.ID}, Limit: 10}); len(m) != 0 {
			t.Fatalf("A searched B's moments: %v", m)
		}
		if _, err := st.SoftDeleteAsset(ctx, wa.ID, a.ID); err != nil {
			t.Fatal(err)
		}
		if err := st.SaveAnalysis(ctx, wa.ID, a.ID, result(a, 1)); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("an analysis was saved on a deleted asset: %v", err)
		}
	})

	t.Run("the subject boxes of the quick look are read, by workspace, for the assets that have them", func(t *testing.T) {
		with, without := ready(wa, ua, "subjects-clip"), ready(wa, ua, "no-subjects-clip")
		detail := []byte(`{"prompt_version":"quicklook.v2","subjects":{"aspect":0.5625,"samples":[{"at_ms":1500,"x":0.2,"y":0.2,"w":0.6,"h":0.7,"head_top_y":0.22}]}}`)
		if _, err := st.ApplyQuickLook(ctx, wa.ID, with.ID, store.QuickLookResult{Title: "Row", Description: "A row.", Tags: []string{"row"}, Detail: detail}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.ApplyQuickLook(ctx, wa.ID, without.ID, store.QuickLookResult{Title: "Row 2", Description: "A row.", Tags: []string{"row"}, Detail: []byte(`{"prompt_version":"quicklook.v1"}`)}); err != nil {
			t.Fatal(err)
		}
		rows, err := st.ListAssetSubjects(ctx, wa.ID, []ids.AssetID{with.ID, without.ID})
		if err != nil || len(rows) != 1 || rows[0].AssetID != with.ID || !strings.Contains(string(rows[0].Subjects), `"head_top_y": 0.22`) && !strings.Contains(string(rows[0].Subjects), `"head_top_y":0.22`) {
			t.Fatalf("subjects of workspace A: %+v %v", rows, err)
		}
		// Another workspace reads nothing of it; nothing asked is nothing read.
		if rows, err := st.ListAssetSubjects(ctx, wb.ID, []ids.AssetID{with.ID}); err != nil || len(rows) != 0 {
			t.Fatalf("workspace B read A's subjects: %+v %v", rows, err)
		}
		if rows, err := st.ListAssetSubjects(ctx, wa.ID, nil); err != nil || rows != nil {
			t.Fatalf("no assets: %+v %v", rows, err)
		}
		// A quick look that failed has no boxes to give, even if an earlier one had.
		if _, err := st.FailQuickLook(ctx, wa.ID, with.ID, []byte(`{"error":"spend_cap"}`)); err != nil {
			t.Fatal(err)
		}
		if rows, _ := st.ListAssetSubjects(ctx, wa.ID, []ids.AssetID{with.ID}); len(rows) != 0 {
			t.Fatalf("a failed quick look's boxes were read: %+v", rows)
		}
	})

	t.Run("labels follow the quick look, dead spans are limited per asset, candidates rank below", func(t *testing.T) {
		a := ready(wa, ua, "labels-clip")
		r := result(a, 2)
		for i := 0; i < 8; i++ { // eight dead spans of different lengths
			r.Moments = append(r.Moments, store.NewMoment{Kind: store.MomentDead, StartMs: 1000 + i*5000, EndMs: 1000 + i*5000 + 1000 + i*100, Attrs: []byte(`{}`), Description: "dead"})
		}
		r.Moments = append(r.Moments, store.NewMoment{Kind: store.MomentRepCandidate, StartMs: 52000, EndMs: 58000, Score: 0.3, Attrs: []byte(`{}`), Description: "slow"})
		for i := range r.Moments {
			r.Moments[i].Labels = nil
		}
		if err := st.SaveAnalysis(ctx, wa.ID, a.ID, r); err != nil {
			t.Fatal(err)
		}
		// before the quick look the moments have no labels; when it lands they get its tags
		if ms, _ := st.ListAssetMoments(ctx, wa.ID, a.ID); ms[0].Labels == nil || len(ms[0].Labels) != 0 {
			t.Fatalf("labels before the quick look: %v", ms[0].Labels)
		}
		if _, err := st.ApplyQuickLook(ctx, wa.ID, a.ID, store.QuickLookResult{Title: "Row", Description: "A row.", Tags: []string{"row", "back"}, Detail: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
		ms, _ := st.ListAssetMoments(ctx, wa.ID, a.ID)
		for _, m := range ms {
			if len(m.Labels) != 2 || m.Labels[0] != "row" {
				t.Fatalf("a moment was not labelled by the quick look: %+v", m)
			}
		}
		if _, err := st.ApplyQuickLook(ctx, wb.ID, a.ID, store.QuickLookResult{Tags: []string{"stolen"}}); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("workspace B applied a quick look: %v", err)
		}
		if ms, _ = st.ListAssetMoments(ctx, wa.ID, a.ID); ms[0].Labels[0] != "row" {
			t.Fatalf("labels after B's attempt: %v", ms[0].Labels)
		}
		// a later analysis of an asset that has tags labels its moments with them
		if err := st.SaveAnalysis(ctx, wa.ID, a.ID, r); err != nil {
			t.Fatal(err)
		}
		if ms, _ = st.ListAssetMoments(ctx, wa.ID, a.ID); ms[0].Labels[0] != "row" {
			t.Fatalf("a re-run lost the labels: %v", ms[0].Labels)
		}

		spans, err := st.ListDeadSpans(ctx, wa.ID, []ids.AssetID{a.ID}, 3)
		if err != nil || len(spans) != 3 {
			t.Fatalf("spans %v %v", spans, err)
		}
		for i, sp := range spans {
			if sp.Total != 9 || (i > 0 && sp.StartMs < spans[i-1].StartMs) {
				t.Fatalf("span %d: %+v (a clip has 9 in all: 8 and the one of result)", i, sp)
			}
		}
		if spans[2].EndMs-spans[2].StartMs != 1700 { // the longest of the eight, in time order last
			t.Errorf("the longest spans were not the ones kept: %+v", spans)
		}
		if other, _ := st.ListDeadSpans(ctx, wb.ID, []ids.AssetID{a.ID}, 3); len(other) != 0 {
			t.Fatalf("B read A's dead spans: %v", other)
		}
		// by default candidates are searched with the reps and hold, below them
		top, _ := st.ListMoments(ctx, wa.ID, store.MomentFilter{Assets: []ids.AssetID{a.ID}, Limit: 20})
		if top[len(top)-1].Kind != store.MomentRepCandidate {
			t.Fatalf("the rep candidate should rank last: %+v", top)
		}
		meta, err := st.GetAssetSignalsMeta(ctx, wa.ID, a.ID)
		if err != nil || meta.Version != 1 {
			t.Fatalf("meta %+v %v", meta, err)
		}
		if _, err := st.GetAssetSignalsMeta(ctx, wb.ID, a.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("B read A's meta: %v", err)
		}
	})

	t.Run("a stale or not ready analysis is refused and changes nothing", func(t *testing.T) {
		a := ready(wa, ua, "stale-clip")
		if err := st.SaveAnalysis(ctx, wa.ID, a.ID, result(a, 3)); err != nil {
			t.Fatal(err)
		}
		before, _ := st.ListAssetMoments(ctx, wa.ID, a.ID)
		// an analysis made from another proxy (an older ingest) is refused
		old := result(a, 1)
		older := a.IngestedAt.Add(-time.Minute)
		old.SourceIngestedAt = &older
		old.Moments[0].Description = "from the old proxy"
		if err := st.SaveAnalysis(ctx, wa.ID, a.ID, old); !errors.Is(err, store.ErrStaleAnalysis) {
			t.Fatalf("a stale save = %v", err)
		}
		after, _ := st.ListAssetMoments(ctx, wa.ID, a.ID)
		if len(after) != len(before) || after[0].Description != before[0].Description {
			t.Fatalf("a stale save changed the moments: %d -> %d", len(before), len(after))
		}
		meta, _ := st.GetAssetSignalsMeta(ctx, wa.ID, a.ID)
		if meta.SourceIngestedAt == nil || !meta.SourceIngestedAt.Equal(*a.IngestedAt) {
			t.Fatalf("a stale save changed the signals: %v", meta.SourceIngestedAt)
		}
		// an asset that is processing again (a re-ingest under way) takes no analysis either
		if _, err := st.ClaimAssetForIngest(ctx, wa.ID, a.ID); err != nil {
			t.Fatal(err)
		}
		if err := st.SaveAnalysis(ctx, wa.ID, a.ID, result(a, 3)); !errors.Is(err, store.ErrStaleAnalysis) {
			t.Fatalf("a save while processing = %v", err)
		}
	})

	t.Run("two analyses of one asset: the one from the old proxy loses, whichever saves first", func(t *testing.T) {
		a := ready(wa, ua, "race-clip")
		// the asset is ingested again: a new generation
		if _, err := st.ClaimAssetForIngest(ctx, wa.ID, a.ID); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
		b, err := st.FinishAssetIngest(ctx, wa.ID, a.ID, store.IngestResult{DurationMs: 60000, Width: 720, Height: 1280})
		if err != nil {
			t.Fatal(err)
		}
		stale, fresh := result(a, 2), result(b, 5)
		stale.Moments[0].Description = "stale"
		fresh.Moments[0].Description = "fresh"
		for round := 0; round < 4; round++ {
			var wg sync.WaitGroup
			errs := make([]error, 2)
			for i, r := range []store.AnalysisResult{stale, fresh} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					errs[i] = st.SaveAnalysis(ctx, wa.ID, a.ID, r)
				}()
			}
			wg.Wait()
			if !errors.Is(errs[0], store.ErrStaleAnalysis) || errs[1] != nil {
				t.Fatalf("round %d: stale save = %v, fresh save = %v", round, errs[0], errs[1])
			}
			ms, _ := st.ListAssetMoments(ctx, wa.ID, a.ID)
			if len(ms) != 8 || ms[0].Description == "stale" {
				t.Fatalf("round %d: the stale analysis won: %d moments", round, len(ms))
			}
		}
	})

	t.Run("tags are the ones the locked row holds", func(t *testing.T) {
		a := ready(wa, ua, "tags-clip")
		if _, err := st.ApplyQuickLook(ctx, wa.ID, a.ID, store.QuickLookResult{Title: "T", Description: "d", Tags: []string{"lat", "pulldown"}, Detail: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
		// the caller's copy of the asset predates the quick look; the moments still get the tags of the row
		r := result(a, 1)
		for i := range r.Moments {
			r.Moments[i].Labels = nil
		}
		if err := st.SaveAnalysis(ctx, wa.ID, a.ID, r); err != nil {
			t.Fatal(err)
		}
		ms, _ := st.ListAssetMoments(ctx, wa.ID, a.ID)
		for _, m := range ms {
			if len(m.Labels) != 2 || m.Labels[0] != "lat" {
				t.Fatalf("labels %v", m.Labels)
			}
		}
	})

	t.Run("backfill list", func(t *testing.T) {
		wc, uc := mk("c")
		plain := ready(wc, uc, "needs-analysis")
		done := ready(wc, uc, "analysed")
		if err := st.SaveAnalysis(ctx, wc.ID, done.ID, result(done, 1)); err != nil {
			t.Fatal(err)
		}
		todo, err := st.ListAssetsForAnalysis(ctx, wc.ID, 1, false, 10)
		if err != nil || len(todo) != 1 || todo[0].ID != plain.ID {
			t.Fatalf("todo %v %v", todo, err)
		}
		if todo, _ = st.ListAssetsForAnalysis(ctx, wc.ID, 2, false, 10); len(todo) != 2 {
			t.Fatalf("a newer version must list both: %d", len(todo))
		}
		if todo, _ = st.ListAssetsForAnalysis(ctx, wc.ID, 1, true, 10); len(todo) != 2 {
			t.Fatalf("include_done must list both: %d", len(todo))
		}
		// an asset that was ingested again after its analysis (a different proxy) is listed even at the current version
		if _, err := st.ClaimAssetForIngest(ctx, wc.ID, done.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := st.FinishAssetIngest(ctx, wc.ID, done.ID, store.IngestResult{DurationMs: 60000, Width: 720, Height: 1280}); err != nil {
			t.Fatal(err)
		}
		if todo, _ = st.ListAssetsForAnalysis(ctx, wc.ID, 1, false, 10); len(todo) != 2 {
			t.Fatalf("an asset analysed from an older proxy must be listed: %d", len(todo))
		}
	})

	t.Run("hook runs in the ingest transaction", func(t *testing.T) {
		var seen []ids.AssetID
		st.SetAnalysisHook(func(_ context.Context, tx *store.Store, a store.Asset) error {
			if _, err := tx.Tx(); err != nil {
				return err
			}
			seen = append(seen, a.ID)
			return nil
		})
		defer st.SetAnalysisHook(nil)
		a := ready(wa, ua, "hooked")
		if len(seen) != 1 || seen[0] != a.ID {
			t.Fatalf("hook saw %v", seen)
		}
		st.SetAnalysisHook(func(context.Context, *store.Store, store.Asset) error { return errors.New("queue down") })
		id, _ := st.NewAssetID(ctx)
		b, _, err := st.CreateAssetWithUpload(ctx, wa.ID, store.NewAsset{
			ID: id, Kind: store.AssetKindVideo, Filename: "x.mp4", ContentType: "video/mp4", SizeBytes: 1 << 20, Title: "x",
			R2Key: "ws/" + wa.ID.String() + "/assets/" + id.String() + "/original", CreatedBy: ua, R2UploadID: "r2-" + id.String(),
			PartSize: 16 << 20, PartCount: 1, ExpiresAt: time.Now().Add(time.Hour),
		}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.InTx(ctx, func(tx *store.Store) error { _, err := tx.CompleteUpload(ctx, wa.ID, b.ID); return err }); err != nil {
			t.Fatal(err)
		}
		if _, err := st.ClaimAssetForIngest(ctx, wa.ID, b.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := st.FinishAssetIngest(ctx, wa.ID, b.ID, store.IngestResult{DurationMs: 1, Width: 2, Height: 2}); err == nil {
			t.Fatal("a failing hook did not fail the finish")
		}
		if got, _ := st.GetAsset(ctx, wa.ID, b.ID); got.Status != store.AssetProcessing {
			t.Fatalf("a failed hook must roll the ingest back, status %s", got.Status)
		}
	})
}
