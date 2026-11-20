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
		// The link names its creator: another creator's row is refused (compliance: a post is never filed under the wrong person).
		if _, err := st.UpsertResearchPost(ctx, store.NewResearchPost{URL: p.Url, CreatorID: other.ID}); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("a TikTok post under another creator: %v", err)
		}
		// The platform of the post must be the creator's platform.
		ig := newCreator(t, research.Instagram, "gym_ig")
		if _, err := st.UpsertResearchPost(ctx, store.NewResearchPost{URL: p.Url, CreatorID: ig.ID}); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("a TikTok post under an Instagram creator: %v", err)
		}
		if _, err := st.UpsertResearchPost(ctx, store.NewResearchPost{URL: "https://www.instagram.com/reel/Cabc12345/", CreatorID: c.ID}); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("an Instagram post under a TikTok creator: %v", err)
		}
		if _, err := st.UpsertResearchPost(ctx, store.NewResearchPost{URL: p.Url, CreatorID: uuid.New()}); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("a creator that does not exist: %v", err)
		}
		// Where the link has no handle (YouTube), the same post under a second creator is a conflict.
		y1, y2 := newCreator(t, research.YouTube, "yt_one"), newCreator(t, research.YouTube, "yt_two")
		yl := "https://www.youtube.com/shorts/" + ytID(tag, "c")
		if _, err := st.UpsertResearchPost(ctx, store.NewResearchPost{URL: yl, CreatorID: y1.ID}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.UpsertResearchPost(ctx, store.NewResearchPost{URL: yl, CreatorID: y2.ID}); !errors.Is(err, store.ErrConflict) {
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

	t.Run("every YouTube link form can be stored, with a hyphenated handle", func(t *testing.T) {
		c, err := st.UpsertResearchCreator(ctx, store.NewResearchCreator{Platform: research.YouTube, Handle: "@My-Channel-" + tag})
		if err != nil || c.Handle != "my-channel-"+tag {
			t.Fatalf("hyphenated handle: %+v %v", c, err)
		}
		id1, id2 := ytID(tag, "w"), ytID(tag, "u")
		for _, link := range []string{"https://www.youtube.com/watch?v=" + id1 + "&list=x", "https://youtu.be/" + id2 + "?t=3", "https://www.youtube.com/shorts/" + ytID(tag, "s")} {
			p, err := st.UpsertResearchPost(ctx, store.NewResearchPost{URL: link, CreatorID: c.ID})
			if err != nil {
				t.Fatalf("%s: %v", link, err)
			}
			if !strings.HasPrefix(p.Url, "https://www.youtube.com/") {
				t.Fatalf("canonical url = %s", p.Url)
			}
			if _, err := st.AddResearchEvidence(ctx, store.NewResearchEvidence{PostID: p.ID, Source: research.SourceYouTubeAPI, Method: research.MethodAPI, Observer: "run:test", Metrics: map[string]float64{"views": 3}}); err != nil {
				t.Fatalf("evidence for %s: %v", link, err)
			}
			got, err := st.GetResearchPostByURL(ctx, link)
			if err != nil || got.ID != p.ID {
				t.Fatalf("lookup %s: %+v %v", link, got, err)
			}
		}
		// The database accepts ?v=ID as the only query, and nothing else.
		okIns := `INSERT INTO research_posts (platform, url, external_id, creator_id) VALUES ('youtube', $1, $2, $3)`
		if _, err := st.Pool().Exec(ctx, okIns, "https://www.youtube.com/watch?v=abcDEF12345", ytID(tag, "r1"), c.ID); err != nil {
			t.Errorf("a watch link was refused by the database: %v", err)
		}
		for _, bad := range []string{"https://www.youtube.com/watch?x=abcDEF12345", "https://www.youtube.com/watch?v=short", "https://www.youtube.com/watch?v=abcDEF12345&list=1", "https://www.youtube.com/a.mp4?v=abcDEF12345"} {
			if bad == "https://www.youtube.com/a.mp4?v=abcDEF12345" {
				continue // an extension is judged in Go (ParsePostURL), the column only shapes the query
			}
			if _, err := st.Pool().Exec(ctx, okIns, bad, ytID(tag, "r2"), c.ID); err == nil {
				t.Errorf("the database accepted %q", bad)
			}
		}
		// A creator with a hyphen can be forgotten.
		res, err := st.ForgetResearchCreator(ctx, research.YouTube, "@MY-CHANNEL-"+tag)
		if err != nil || res.Creators != 1 || res.Posts != 3+1 {
			t.Fatalf("forget a hyphenated handle: %+v %v", res, err)
		}
		if _, err := st.UpsertResearchCreator(ctx, store.NewResearchCreator{Platform: research.YouTube, Handle: "my-channel-" + tag}); !errors.Is(err, store.ErrForgotten) {
			t.Fatalf("re-ingest after forget: %v", err)
		}
	})

	t.Run("a YouTube observation may be kept 30 days in any time zone", func(t *testing.T) {
		c := newCreator(t, research.YouTube, "yt_tz")
		p, err := st.UpsertResearchPost(ctx, store.NewResearchPost{URL: "https://www.youtube.com/shorts/" + ytID(tag, "z"), CreatorID: c.ID})
		if err != nil {
			t.Fatal(err)
		}
		// Observed just before the US clocks went forward: 30 calendar days later is only 719 hours in New York, but the rule is 30x24h.
		obs := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
		ins := `INSERT INTO research_evidence (post_id, source, method, observed_at, observer, retain_until) VALUES ($1, 'youtube_api', 'api', $2, 'x', $3)`
		for _, zone := range []string{"UTC", "America/New_York", "Pacific/Auckland", "Europe/Amsterdam"} {
			for _, c := range []struct {
				hours int
				ok    bool
			}{{720, true}, {721, false}} {
				tx, err := st.Pool().Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := tx.Exec(ctx, `SELECT set_config('TimeZone', $1, true)`, zone); err != nil {
					t.Fatal(err)
				}
				_, err = tx.Exec(ctx, ins, p.ID, obs, obs.Add(time.Duration(c.hours)*time.Hour))
				_ = tx.Rollback(ctx)
				if (err == nil) != c.ok {
					t.Errorf("zone %s, %dh: err = %v, want ok = %v", zone, c.hours, err, c.ok)
				}
			}
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

	t.Run("a watch-sheet note is short, one line and has no link (R2)", func(t *testing.T) {
		c := newCreator(t, research.TikTok, "gym_n")
		p := tiktokPost(t, c, 8)
		a, err := st.SaveResearchAnnotation(ctx, p.ID, research.Annotation{Notes: "long take, title over the set", Annotator: "sam", Confidence: 1})
		if err != nil || a.Notes == nil || *a.Notes != "long take, title over the set" {
			t.Fatalf("note: %+v %v", a, err)
		}
		for name, note := range map[string]string{
			"link":      "see https://example.com/x",
			"too long":  strings.Repeat("a", research.MaxNotes+1),
			"two lines": "one\ntwo",
		} {
			if _, err := st.SaveResearchAnnotation(ctx, p.ID, research.Annotation{Notes: note, Annotator: "sam", Confidence: 1}); !errors.Is(err, store.ErrInvalid) {
				t.Errorf("%s: err = %v, want ErrInvalid", name, err)
			}
			if _, err := st.Pool().Exec(ctx, `INSERT INTO research_annotations (post_id, notes, annotator) VALUES ($1, $2, 'raw')`, p.ID, note); err == nil {
				t.Errorf("the database accepted a note with a %s", name)
			}
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
		ghost := in
		ghost.EvidenceIDs = []uuid.UUID{ev.ID, uuid.New()}
		if _, err := st.AddResearchFinding(ctx, ghost); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("evidence that does not exist: %v", err)
		}
		dup := in
		dup.EvidenceIDs = []uuid.UUID{ev.ID, ev.ID}
		if _, err := st.AddResearchFinding(ctx, dup); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("a duplicate evidence id: %v", err)
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
		res, err := st.SweepResearch(ctx, now, research.DefaultRetention())
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
		if _, err := st.SweepResearch(ctx, now, research.DefaultRetention()); err != nil {
			t.Fatal(err)
		}
		if _, err := st.GetResearchPostByURL(ctx, p.Url); err != nil {
			t.Fatalf("the post still has evidence: %v", err)
		}
		// Two days on, the 29-day row has expired too, and the post with nothing left goes (it is older than the orphan grace).
		if _, err := st.SweepResearch(ctx, now.Add(2*research.Day), research.DefaultRetention()); err != nil {
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

	t.Run("the sweep enforces the current retention, orphan findings and old findings", func(t *testing.T) {
		c := newCreator(t, research.TikTok, "gym_sw")
		if _, err := st.SetResearchCreatorStatus(ctx, c.ID, store.ResearchApproved, "sam"); err != nil {
			t.Fatal(err)
		}
		p := tiktokPost(t, c, 6)
		// Written under the default (12 months), seen 40 days ago.
		old, err := st.AddResearchEvidence(ctx, store.NewResearchEvidence{PostID: p.ID, Source: research.SourceWatchSheet, Method: research.MethodManual, ObservedAt: now.Add(-40 * research.Day), Observer: "sam"})
		if err != nil {
			t.Fatal(err)
		}
		fresh, err := st.AddResearchEvidence(ctx, store.NewResearchEvidence{PostID: p.ID, Source: research.SourceWatchSheet, Method: research.MethodManual, ObservedAt: now.Add(-5 * research.Day), Observer: "sam"})
		if err != nil {
			t.Fatal(err)
		}
		if !old.RetainUntil.After(now) {
			t.Fatalf("setup: retain_until %v should still be in the future", old.RetainUntil)
		}
		mk := func(claim string, ids ...uuid.UUID) store.ResearchFinding {
			t.Helper()
			f, err := st.AddResearchFinding(ctx, store.NewResearchFinding{PlaybookVersion: "cur-" + tag, Claim: claim, Dimension: "length", EffectUnit: "s", Confidence: "low", EvidenceIDs: ids})
			if err != nil {
				t.Fatal(err)
			}
			return f
		}
		onFresh := mk("on fresh", fresh.ID)
		// A finding whose evidence vanished by other means (a cascade) is an orphan.
		other := newCreator(t, research.TikTok, "gym_sw2")
		op := tiktokPost(t, other, 7)
		oev, err := st.AddResearchEvidence(ctx, store.NewResearchEvidence{PostID: op.ID, Source: research.SourceWatchSheet, Method: research.MethodManual, Observer: "sam"})
		if err != nil {
			t.Fatal(err)
		}
		orphan := mk("orphan", oev.ID, fresh.ID)
		if _, err := st.Pool().Exec(ctx, `DELETE FROM research_posts WHERE id = $1`, op.ID); err != nil {
			t.Fatal(err)
		}
		// An old finding on live evidence is still too old.
		aged := mk("aged", fresh.ID)
		if _, err := st.Pool().Exec(ctx, `UPDATE research_findings SET created_at = $2 WHERE id = $1`, aged.ID, now.Add(-400*research.Day)); err != nil {
			t.Fatal(err)
		}

		// Retention lowered to 30 days: the 40-day-old row goes though its stored retain_until is months away.
		res, err := st.SweepResearch(ctx, now, research.Retention{Default: 30 * research.Day, YouTube: 30 * research.Day})
		if err != nil || res.Evidence < 1 || res.Findings < 2 {
			t.Fatalf("sweep: %+v %v", res, err)
		}
		left, err := st.ListResearchEvidenceForPost(ctx, p.ID)
		if err != nil || len(left) != 1 || left[0].ID != fresh.ID {
			t.Fatalf("evidence after the sweep: %+v %v", left, err)
		}
		fs, err := st.ListResearchFindings(ctx, "cur-"+tag)
		if err != nil || len(fs) != 1 || fs[0].ID != onFresh.ID {
			t.Fatalf("findings after the sweep (orphan %s, aged %s): %+v %v", orphan.ID, aged.ID, fs, err)
		}
		// With the default retention the 12-month-old finding would have gone too; the fresh one stays.
		if _, err := st.SweepResearch(ctx, now, research.Retention{}); err != nil {
			t.Fatal(err)
		}
		if fs, err = st.ListResearchFindings(ctx, "cur-"+tag); err != nil || len(fs) != 1 {
			t.Fatalf("findings after a default sweep: %+v %v", fs, err)
		}
	})

	t.Run("an approved creator survives the sweep; an idle candidate does not", func(t *testing.T) {
		keep := newCreator(t, research.TikTok, "keep")
		if _, err := st.SetResearchCreatorStatus(ctx, keep.ID, store.ResearchApproved, "sam"); err != nil {
			t.Fatal(err)
		}
		idle := newCreator(t, research.TikTok, "idle")
		far := now.Add(2 * 365 * research.Day)
		if _, err := st.SweepResearch(ctx, far, research.DefaultRetention()); err != nil {
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

		// Forgetting a handle we hold nothing on still blocks it (the request came before we met the person).
		if res, err := st.ForgetResearchCreator(ctx, research.TikTok, "nobody_"+tag); err != nil || res.Creators != 0 {
			t.Fatalf("an unknown handle: %+v %v", res, err)
		}
		if _, err := st.UpsertResearchCreator(ctx, store.NewResearchCreator{Platform: research.TikTok, Handle: "nobody_" + tag}); !errors.Is(err, store.ErrForgotten) {
			t.Fatalf("an unknown handle that was forgotten: %v", err)
		}
		// A tombstone is for one platform when one was named, and the other platforms are free.
		if _, err := st.UpsertResearchCreator(ctx, store.NewResearchCreator{Platform: research.Instagram, Handle: "nobody_" + tag}); err != nil {
			t.Fatalf("the same handle on another platform: %v", err)
		}
		if _, err := st.ForgetResearchCreator(ctx, research.Instagram, c.Handle); err != nil {
			t.Fatalf("the handle on another platform: %v", err)
		}
		if _, err := st.GetResearchCreator(ctx, research.TikTok, c.Handle); err != nil {
			t.Fatalf("forgetting on Instagram must not touch TikTok: %v", err)
		}
		if n, err := st.UnforgetResearchCreator(ctx, research.Instagram, c.Handle); err != nil || n != 1 {
			t.Fatalf("undo: %d %v", n, err)
		}
		if _, err := st.ForgetResearchCreator(ctx, "", "-not a handle"); !errors.Is(err, store.ErrInvalid) {
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
		// The tombstone refuses the creator and their posts, and holds no handle in the clear.
		if _, err := st.UpsertResearchCreator(ctx, store.NewResearchCreator{Platform: research.TikTok, Handle: c.Handle}); !errors.Is(err, store.ErrForgotten) {
			t.Fatalf("a forgotten creator comes back: %v", err)
		}
		var leaked int
		if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM research_forgotten WHERE handle_hash LIKE '%' || $1 || '%'`, tag).Scan(&leaked); err != nil || leaked != 0 {
			t.Fatalf("the tombstone holds the handle in the clear: %d %v", leaked, err)
		}
		if _, err := st.GetResearchCreator(ctx, research.TikTok, other.Handle); err != nil {
			t.Fatal(err)
		}
		// A post of a creator whose tombstone appeared later is refused too (the creator row exists, e.g. a race).
		hash, err := research.HandleHash(research.TikTok, other.Handle)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.Pool().Exec(ctx, `INSERT INTO research_forgotten (platform, handle_hash) VALUES ('tiktok', $1)`, hash); err != nil {
			t.Fatal(err)
		}
		if _, err := st.UpsertResearchPost(ctx, store.NewResearchPost{URL: "https://www.tiktok.com/@" + other.Handle + "/video/7399999999999999" + uuidDigits(tag, 9)[:3], CreatorID: other.ID}); !errors.Is(err, store.ErrForgotten) {
			t.Fatalf("a post of a forgotten creator: %v", err)
		}
		if _, err := st.Pool().Exec(ctx, `DELETE FROM research_forgotten WHERE handle_hash = $1`, hash); err != nil {
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
	return (tag[:8] + which + "xxxxxxxxxxx")[:11]
}
