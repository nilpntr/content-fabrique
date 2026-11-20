package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/research"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// researchSuite is part of repositorySuite (superuser, then the app role): the creator research tables work with DML rights only, and
// the compliance rules hold twice, once in Go and once as CHECK constraints (the raw inserts go around the Go checks on purpose).
func researchSuite(t *testing.T, st *store.Store, suffix string) {
	ctx := context.Background()
	tag := strings.ReplaceAll(suffix, "-", "")[:12]
	now := time.Now().UTC().Truncate(time.Microsecond)

	newCreator := func(t *testing.T, platform research.Platform, name string) store.ResearchCreator {
		t.Helper()
		c, err := st.UpsertResearchCreator(ctx, store.NewResearchCreator{Platform: platform, Handle: name + "_" + tag, WhySimilar: "long takes, one title", Similarity: 0.7})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	tiktokPost := func(t *testing.T, c store.ResearchCreator, n int) store.ResearchPost {
		t.Helper()
		link := "https://www.tiktok.com/@" + c.Handle + "/video/" + uuidDigits(tag, n)
		p, err := st.UpsertResearchPost(ctx, store.NewResearchPost{URL: link, CreatorID: c.ID})
		if err != nil {
			t.Fatalf("post %s: %v", link, err)
		}
		return p
	}

	t.Run("creators: candidate, approve needs a person, refresh keeps the status", func(t *testing.T) {
		c := newCreator(t, research.TikTok, "gym_a")
		if c.Status != store.ResearchCandidate || c.ProfileUrl != "https://www.tiktok.com/@gym_a_"+tag {
			t.Fatalf("new creator: %+v", c)
		}
		if _, err := st.SetResearchCreatorStatus(ctx, c.ID, store.ResearchApproved, " "); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("approved by nobody: %v", err)
		}
		a, err := st.SetResearchCreatorStatus(ctx, c.ID, store.ResearchApproved, "sam")
		if err != nil || a.Status != "approved" || a.ApprovedBy == nil || *a.ApprovedBy != "sam" || a.ApprovedAt == nil {
			t.Fatalf("approve: %+v %v", a, err)
		}
		again, err := st.UpsertResearchCreator(ctx, store.NewResearchCreator{Platform: research.TikTok, Handle: "@GYM_A_" + tag, WhySimilar: "new words", Similarity: 0.9})
		if err != nil || again.ID != c.ID || again.Status != "approved" || again.WhySimilar != "new words" {
			t.Fatalf("refresh: %+v %v", again, err)
		}
		if _, err := st.SetResearchCreatorStatus(ctx, c.ID, "banned", ""); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("unknown status: %v", err)
		}
		d, err := st.SetResearchCreatorStatus(ctx, c.ID, store.ResearchDropped, "")
		if err != nil || d.ApprovedBy != nil {
			t.Fatalf("drop: %+v %v", d, err)
		}
		if _, err := st.UpsertResearchCreator(ctx, store.NewResearchCreator{Platform: "myspace", Handle: "x"}); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("unknown platform: %v", err)
		}
		if _, err := st.UpsertResearchCreator(ctx, store.NewResearchCreator{Platform: research.TikTok, Handle: "has space"}); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("bad handle: %v", err)
		}
		if _, err := st.UpsertResearchCreator(ctx, store.NewResearchCreator{Platform: research.TikTok, Handle: "ok_" + tag, Similarity: 1.5}); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("similarity out of range: %v", err)
		}
	})

	t.Run("posts: only post pages, never media", func(t *testing.T) {
		c := newCreator(t, research.TikTok, "gym_b")
		posted := now.Add(-48 * time.Hour)
		p, err := st.UpsertResearchPost(ctx, store.NewResearchPost{
			URL: "https://www.tiktok.com/@" + c.Handle + "/video/7300000000000000001?utm=x", CreatorID: c.ID,
			PostedAt: &posted, PostedAtSource: "id",
		})
		if err != nil || p.ExternalID != "7300000000000000001" || p.Url != "https://www.tiktok.com/@"+c.Handle+"/video/7300000000000000001" {
			t.Fatalf("post: %+v %v", p, err)
		}
		// The same post again refreshes it (and does not lose the time).
		dur := 31.5
		p2, err := st.UpsertResearchPost(ctx, store.NewResearchPost{URL: p.Url, CreatorID: c.ID, DurationS: &dur})
		if err != nil || p2.ID != p.ID || p2.PostedAt == nil || p2.DurationS == nil || *p2.DurationS != 31.5 {
			t.Fatalf("refresh: %+v %v", p2, err)
		}
		got, err := st.GetResearchPostByURL(ctx, p.Url+"?x=1")
		if err != nil || got.ID != p.ID {
			t.Fatalf("by url: %+v %v", got, err)
		}
		other := newCreator(t, research.TikTok, "gym_c")
		if _, err := st.UpsertResearchPost(ctx, store.NewResearchPost{URL: p.Url, CreatorID: other.ID}); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("a post of another creator: %v", err)
		}
		for _, bad := range []string{
			"https://www.tiktok.com/@" + c.Handle,
			"https://v16.tiktokcdn.com/abc/video.mp4",
			"https://cdn.example.com/clip.mp4",
			"https://www.instagram.com/" + c.Handle + "/",
			"javascript:alert(1)",
		} {
			if _, err := st.UpsertResearchPost(ctx, store.NewResearchPost{URL: bad, CreatorID: c.ID}); !errors.Is(err, store.ErrInvalid) {
				t.Errorf("%q: err = %v, want ErrInvalid", bad, err)
			}
		}
		future := now.Add(30 * 24 * time.Hour)
		if _, err := st.UpsertResearchPost(ctx, store.NewResearchPost{URL: p.Url, CreatorID: c.ID, PostedAt: &future, PostedAtSource: "manual"}); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("a post time in the future: %v", err)
		}
		if _, err := st.UpsertResearchPost(ctx, store.NewResearchPost{URL: p.Url, CreatorID: c.ID, PostedAt: &posted}); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("a post time without its source: %v", err)
		}
		// There is no column for media at all.
		var media int
		if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_name LIKE 'research\_%'
			AND (column_name ~ '(media|video_|audio|thumb|transcript|blob|(^|_)file|bytes|stream|download)')`).Scan(&media); err != nil || media != 0 {
			t.Fatalf("columns that could hold media: %d (%v)", media, err)
		}
	})

	t.Run("evidence: allow-list, method, metrics, retention", func(t *testing.T) {
		c := newCreator(t, research.TikTok, "gym_d")
		p := tiktokPost(t, c, 1)
		hash := strings.Repeat("ab", 32)

		ev, err := st.AddResearchEvidence(ctx, store.NewResearchEvidence{
			PostID: p.ID, Source: research.SourceWatchSheet, Method: research.MethodManual, ObservedAt: now, Observer: "sam",
			Metrics: map[string]float64{"views": 1200, "likes": 80, "saves": 30, "shares": 12}, RawRef: hash,
		})
		if err != nil {
			t.Fatal(err)
		}
		if want := now.Add(365 * research.Day); !ev.RetainUntil.Equal(want) {
			t.Fatalf("retain_until = %v, want %v (12 months)", ev.RetainUntil, want)
		}
		short, err := st.AddResearchEvidence(ctx, store.NewResearchEvidence{
			PostID: p.ID, Source: research.SourceWeb, Method: research.MethodAPI, ObservedAt: now, Observer: "run:test",
			Retention: research.Retention{Default: 10 * research.Day, YouTube: 5 * research.Day},
		})
		if err != nil || !short.RetainUntil.Equal(now.Add(10*research.Day)) {
			t.Fatalf("a configured retention: %+v %v", short, err)
		}
		oe, err := st.AddResearchEvidence(ctx, store.NewResearchEvidence{PostID: p.ID, Source: research.SourceOEmbed, Method: research.MethodAPI, Observer: "run:test"})
		if err != nil || string(oe.Metrics) != "{}" {
			t.Fatalf("oembed: %+v %v", oe, err)
		}
		list, err := st.ListResearchEvidenceForPost(ctx, p.ID)
		if err != nil || len(list) != 3 {
			t.Fatalf("list: %d %v", len(list), err)
		}

		refused := map[string]store.NewResearchEvidence{
			"unknown source":      {PostID: p.ID, Source: "tikapi", Method: research.MethodAPI, Observer: "x"},
			"no target":           {Source: research.SourceWatchSheet, Method: research.MethodManual, Observer: "x"},
			"both targets":        {PostID: p.ID, CreatorID: c.ID, Source: research.SourceWatchSheet, Method: research.MethodManual, Observer: "x"},
			"wrong method":        {PostID: p.ID, Source: research.SourceWatchSheet, Method: research.MethodAPI, Observer: "x"},
			"youtube on tiktok":   {PostID: p.ID, Source: research.SourceYouTubeAPI, Method: research.MethodAPI, Observer: "x"},
			"counts from oembed":  {PostID: p.ID, Source: research.SourceOEmbed, Method: research.MethodAPI, Observer: "x", Metrics: map[string]float64{"views": 5}},
			"media in metrics":    {PostID: p.ID, Source: research.SourceWatchSheet, Method: research.MethodManual, Observer: "x", Metrics: map[string]float64{"media_url": 1}},
			"no observer":         {PostID: p.ID, Source: research.SourceWatchSheet, Method: research.MethodManual},
			"raw response as ref": {PostID: p.ID, Source: research.SourceWatchSheet, Method: research.MethodManual, Observer: "x", RawRef: `{"title":"the whole response"}`},
			"retention too long":  {PostID: p.ID, Source: research.SourceWeb, Method: research.MethodAPI, Observer: "x", Retention: research.Retention{Default: 900 * research.Day, YouTube: research.Day}},
		}
		for name, in := range refused {
			if _, err := st.AddResearchEvidence(ctx, in); !errors.Is(err, store.ErrInvalid) {
				t.Errorf("%s: err = %v, want ErrInvalid", name, err)
			}
		}
		if _, err := st.AddResearchEvidence(ctx, store.NewResearchEvidence{PostID: uuid.New(), Source: research.SourceWatchSheet, Method: research.MethodManual, Observer: "x"}); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("a post that does not exist: %v", err)
		}

		// The database says no even when the Go checks are bypassed.
		raw := func(name, sql string, args ...any) {
			t.Helper()
			if _, err := st.Pool().Exec(ctx, sql, args...); err == nil {
				t.Errorf("%s: the database accepted it", name)
			}
		}
		ins := `INSERT INTO research_evidence (post_id, source, method, observed_at, observer, metrics, raw_ref, retain_until) VALUES ($1, $2, $3, now(), 'x', $4::jsonb, $5, now() + $6::interval)`
		raw("unknown source", ins, p.ID, "apify", "api", `{}`, nil, "1 day")
		raw("unknown method", ins, p.ID, "web", "scrape", `{}`, nil, "1 day")
		raw("watch sheet by api", ins, p.ID, "watch_sheet", "api", `{}`, nil, "1 day")
		raw("media key in metrics", ins, p.ID, "web", "api", `{"video_url":1}`, nil, "1 day")
		raw("text in metrics", ins, p.ID, "web", "api", `{"views":"https://x.example/a.mp4"}`, nil, "1 day")
		raw("negative metric", ins, p.ID, "web", "api", `{"views":-1}`, nil, "1 day")
		raw("raw response as ref", ins, p.ID, "web", "api", `{}`, `{"a":1}`, "1 day")
		raw("youtube kept 31 days", ins, p.ID, "youtube_api", "api", `{}`, nil, "31 days")
		raw("retain before observed", ins, p.ID, "web", "api", `{}`, nil, "-1 day")
		raw("a media link with a query", `INSERT INTO research_posts (platform, url, external_id, creator_id) VALUES ('tiktok', 'https://cdn.example.com/a.mp4?x=1', 'abc', $1)`, c.ID)
		raw("a post url with a query", `INSERT INTO research_posts (platform, url, external_id, creator_id) VALUES ('tiktok', 'https://www.tiktok.com/@a/video/1?x=1', 'q1', $1)`, c.ID)
	})

	t.Run("annotations are checked against the formats and the vocabulary", func(t *testing.T) {
		c := newCreator(t, research.TikTok, "gym_e")
		p := tiktokPost(t, c, 2)
		cuts := 4
		a, err := st.SaveResearchAnnotation(ctx, p.ID, research.Annotation{
			FormatID: "tutorial_101", HookKind: "title_first", HookTextPattern: "<TOPIC> 101", TextPattern: "single_title", CutsEstimate: &cuts,
			CTA: "save", Topic: "body:back", Annotator: "sam", Confidence: 0.9,
		})
		if err != nil || a.FormatID == nil || *a.FormatID != "tutorial_101" || a.CutsEstimate == nil || *a.CutsEstimate != 4 {
			t.Fatalf("annotation: %+v %v", a, err)
		}
		// One per post and annotator: a second save replaces it.
		a2, err := st.SaveResearchAnnotation(ctx, p.ID, research.Annotation{Annotator: "sam", Confidence: 0.5, TextPattern: "none"})
		if err != nil || a2.ID != a.ID || a2.FormatID != nil || a2.TextPattern == nil || *a2.TextPattern != "none" {
			t.Fatalf("replace: %+v %v", a2, err)
		}
		for name, in := range map[string]research.Annotation{
			"unknown format": {FormatID: "made_up", Annotator: "sam", Confidence: 1},
			"unknown topic":  {Topic: "body:wings", Annotator: "sam", Confidence: 1},
			"creator words":  {HookTextPattern: "Never skip leg day", Annotator: "sam", Confidence: 1},
		} {
			if _, err := st.SaveResearchAnnotation(ctx, p.ID, in); !errors.Is(err, store.ErrInvalid) {
				t.Errorf("%s: err = %v, want ErrInvalid", name, err)
			}
		}
		if _, err := st.Pool().Exec(ctx, `INSERT INTO research_annotations (post_id, hook_text_pattern, annotator) VALUES ($1, 'never skip leg day', 'raw')`, p.ID); err == nil {
			t.Error("the database accepted the creator's words as a title form")
		}
		list, err := st.ListResearchAnnotationsForPost(ctx, p.ID)
		if err != nil || len(list) != 1 {
			t.Fatalf("list: %d %v", len(list), err)
		}
	})

	t.Run("findings need evidence", func(t *testing.T) {
		c := newCreator(t, research.TikTok, "gym_f")
		p := tiktokPost(t, c, 3)
		ev, err := st.AddResearchEvidence(ctx, store.NewResearchEvidence{PostID: p.ID, Source: research.SourceWatchSheet, Method: research.MethodManual, Observer: "sam", Metrics: map[string]float64{"views": 10}})
		if err != nil {
			t.Fatal(err)
		}
		in := store.NewResearchFinding{PlaybookVersion: "t-" + tag, Claim: "Showing the set before the title.", Dimension: "hook", EffectValue: 1.8, EffectUnit: "x median views", NPosts: 34, NCreators: 9, Confidence: "medium", EvidenceIDs: []uuid.UUID{ev.ID}}
		f, err := st.AddResearchFinding(ctx, in)
		if err != nil || len(f.EvidenceIds) != 1 {
			t.Fatalf("finding: %+v %v", f, err)
		}
		none := in
		none.EvidenceIDs = nil
		if _, err := st.AddResearchFinding(ctx, none); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("a claim without evidence: %v", err)
		}
		badDim := in
		badDim.Dimension = "vibes"
		if _, err := st.AddResearchFinding(ctx, badDim); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("unknown dimension: %v", err)
		}
		badN := in
		badN.NCreators = 40
		if _, err := st.AddResearchFinding(ctx, badN); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("more creators than posts: %v", err)
		}
		got, err := st.ListResearchFindings(ctx, "t-"+tag)
		if err != nil || len(got) != 1 {
			t.Fatalf("list: %d %v", len(got), err)
		}
	})

	t.Run("the sweeper deletes a 31-day YouTube row and keeps a 29-day one", func(t *testing.T) {
		c := newCreator(t, research.YouTube, "yt_a")
		p, err := st.UpsertResearchPost(ctx, store.NewResearchPost{URL: "https://www.youtube.com/shorts/" + ytID(tag, "a"), CreatorID: c.ID})
		if err != nil {
			t.Fatal(err)
		}
		add := func(age time.Duration) store.ResearchEvidence {
			t.Helper()
			e, err := st.AddResearchEvidence(ctx, store.NewResearchEvidence{
				PostID: p.ID, Source: research.SourceYouTubeAPI, Method: research.MethodAPI, ObservedAt: now.Add(-age), Observer: "run:test",
				Metrics: map[string]float64{"views": 100},
			})
			if err != nil {
				t.Fatal(err)
			}
			return e
		}
		old, fresh := add(31*research.Day), add(29*research.Day)
		if !old.RetainUntil.Before(now) || !fresh.RetainUntil.After(now) {
			t.Fatalf("retain_until: old %v fresh %v now %v", old.RetainUntil, fresh.RetainUntil, now)
		}
		// A finding that rested on the old row goes with it; one on the fresh row stays.
		fo, err := st.AddResearchFinding(ctx, store.NewResearchFinding{PlaybookVersion: "sweep-" + tag, Claim: "old", Dimension: "length", EffectUnit: "s", Confidence: "low", EvidenceIDs: []uuid.UUID{old.ID}})
		if err != nil {
			t.Fatal(err)
		}
		ff, err := st.AddResearchFinding(ctx, store.NewResearchFinding{PlaybookVersion: "sweep-" + tag, Claim: "fresh", Dimension: "length", EffectUnit: "s", Confidence: "low", EvidenceIDs: []uuid.UUID{fresh.ID}})
		if err != nil {
			t.Fatal(err)
		}

		// The sweep runs for the whole database, so other tests' rows may go too; this test asserts on its own rows.
		res, err := st.SweepResearch(ctx, now, 365*research.Day)
		if err != nil || res.Evidence < 1 || res.Findings < 1 {
			t.Fatalf("sweep: %+v %v", res, err)
		}
		left, err := st.ListResearchEvidenceForPost(ctx, p.ID)
		if err != nil || len(left) != 1 || left[0].ID != fresh.ID {
			t.Fatalf("after the sweep: %+v %v", left, err)
		}
		fs, err := st.ListResearchFindings(ctx, "sweep-"+tag)
		if err != nil || len(fs) != 1 || fs[0].ID != ff.ID || fs[0].ID == fo.ID {
			t.Fatalf("findings after the sweep: %+v %v", fs, err)
		}
		// Twice is fine, and a month later the rest goes too, with the post that has nothing left.
		if _, err := st.SweepResearch(ctx, now, 365*research.Day); err != nil {
			t.Fatal(err)
		}
		if _, err := st.GetResearchPostByURL(ctx, p.Url); err != nil {
			t.Fatalf("the post still has evidence: %v", err)
		}
		// Two days on, the 29-day row has expired too, and the post with nothing left goes (it is older than the orphan grace).
		if _, err := st.SweepResearch(ctx, now.Add(2*research.Day), 365*research.Day); err != nil {
			t.Fatal(err)
		}
		if _, err := st.GetResearchPostByURL(ctx, p.Url); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("the post with no evidence left: %v", err)
		}
		// An approved creator is kept; the others are only handles.
		if _, err := st.GetResearchCreator(ctx, research.YouTube, c.Handle); err != nil {
			t.Fatalf("candidate inside the idle time: %v", err)
		}
	})

	t.Run("an approved creator survives the sweep; an idle candidate does not", func(t *testing.T) {
		keep := newCreator(t, research.TikTok, "keep")
		if _, err := st.SetResearchCreatorStatus(ctx, keep.ID, store.ResearchApproved, "sam"); err != nil {
			t.Fatal(err)
		}
		idle := newCreator(t, research.TikTok, "idle")
		far := now.Add(2 * 365 * research.Day)
		if _, err := st.SweepResearch(ctx, far, 365*research.Day); err != nil {
			t.Fatal(err)
		}
		if _, err := st.GetResearchCreator(ctx, research.TikTok, keep.Handle); err != nil {
			t.Fatalf("approved creator: %v", err)
		}
		if _, err := st.GetResearchCreator(ctx, research.TikTok, idle.Handle); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("idle candidate: %v", err)
		}
	})

	t.Run("forget removes a creator and everything about them", func(t *testing.T) {
		c := newCreator(t, research.TikTok, "gone")
		other := newCreator(t, research.TikTok, "stays")
		p := tiktokPost(t, c, 4)
		op := tiktokPost(t, other, 5)
		ev, err := st.AddResearchEvidence(ctx, store.NewResearchEvidence{PostID: p.ID, Source: research.SourceWatchSheet, Method: research.MethodManual, Observer: "sam", Metrics: map[string]float64{"views": 9}})
		if err != nil {
			t.Fatal(err)
		}
		cev, err := st.AddResearchEvidence(ctx, store.NewResearchEvidence{CreatorID: c.ID, Source: research.SourceWeb, Method: research.MethodManual, Observer: "sam"})
		if err != nil {
			t.Fatal(err)
		}
		oev, err := st.AddResearchEvidence(ctx, store.NewResearchEvidence{PostID: op.ID, Source: research.SourceWatchSheet, Method: research.MethodManual, Observer: "sam"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.SaveResearchAnnotation(ctx, p.ID, research.Annotation{Annotator: "sam", Confidence: 1, CTA: "none"}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.AddResearchFinding(ctx, store.NewResearchFinding{PlaybookVersion: "forget-" + tag, Claim: "rests on the creator", Dimension: "hook", EffectUnit: "x", Confidence: "low", EvidenceIDs: []uuid.UUID{ev.ID, oev.ID}}); err != nil {
			t.Fatal(err)
		}
		keepF, err := st.AddResearchFinding(ctx, store.NewResearchFinding{PlaybookVersion: "forget-" + tag, Claim: "rests on someone else", Dimension: "hook", EffectUnit: "x", Confidence: "low", EvidenceIDs: []uuid.UUID{oev.ID}})
		if err != nil {
			t.Fatal(err)
		}

		if _, err := st.ForgetResearchCreator(ctx, research.TikTok, "nobody_"+tag); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("an unknown handle: %v", err)
		}
		if _, err := st.ForgetResearchCreator(ctx, research.Instagram, c.Handle); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("the handle on another platform: %v", err)
		}
		if _, err := st.ForgetResearchCreator(ctx, "", "not a handle"); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("a bad handle: %v", err)
		}
		res, err := st.ForgetResearchCreator(ctx, "", "@"+strings.ToUpper(c.Handle))
		if err != nil || res.Creators != 1 || res.Posts != 1 || res.Annotations != 1 || res.Evidence != 2 || res.Findings != 1 {
			t.Fatalf("forget: %+v %v", res, err)
		}
		if _, err := st.GetResearchCreator(ctx, research.TikTok, c.Handle); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("the creator is still there: %v", err)
		}
		if _, err := st.GetResearchPostByURL(ctx, p.Url); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("the post is still there: %v", err)
		}
		var n int
		if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM research_evidence WHERE id = ANY($1)`, []uuid.UUID{ev.ID, cev.ID}).Scan(&n); err != nil || n != 0 {
			t.Fatalf("evidence left: %d %v", n, err)
		}
		// Somebody else's data is untouched, and so is the finding that did not rest on the creator.
		if _, err := st.GetResearchCreator(ctx, research.TikTok, other.Handle); err != nil {
			t.Fatal(err)
		}
		if l, err := st.ListResearchEvidenceForPost(ctx, op.ID); err != nil || len(l) != 1 {
			t.Fatalf("other evidence: %d %v", len(l), err)
		}
		fs, err := st.ListResearchFindings(ctx, "forget-"+tag)
		if err != nil || len(fs) != 1 || fs[0].ID != keepF.ID {
			t.Fatalf("findings after forget: %+v %v", fs, err)
		}
	})
}

// uuidDigits makes a unique run of digits for a TikTok post id out of the suite's tag and a counter.
func uuidDigits(tag string, n int) string {
	var b strings.Builder
	b.WriteString("73")
	for _, r := range tag {
		b.WriteByte(byte('0' + int(r)%10))
	}
	b.WriteByte(byte('0' + n%10))
	return b.String()
}

// ytID makes a unique 11-character YouTube id out of the suite's tag.
func ytID(tag, which string) string {
	return (tag + which + "xxxxxxxxxxx")[:11]
}
