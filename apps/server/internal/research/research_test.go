package research_test

import (
	"errors"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/nilpntr/content-fabrique/internal/research"
)

func TestSourcesAreAClosedList(t *testing.T) {
	want := []string{"oembed", "watch_sheet", "web", "ig_business_discovery", "youtube_api", "creator_oauth"}
	var got []string
	for _, s := range research.Sources() {
		got = append(got, string(s))
		if _, err := research.ParseSource(string(s)); err != nil {
			t.Errorf("%s: %v", s, err)
		}
		if len(s.Methods()) == 0 || len(s.Platforms()) == 0 {
			t.Errorf("%s has no method or platform", s)
		}
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("sources = %v, want %v", got, want)
	}
	for _, bad := range []string{"", "tiktok_research_api", "ensembledata", "apify", "OEMBED", "scraper", "oembed "} {
		if _, err := research.ParseSource(bad); err == nil {
			t.Errorf("ParseSource(%q) accepted a source that is not on the list", bad)
		}
	}
	if err := research.CheckObservation("tikapi", research.MethodAPI, research.TikTok, nil); err == nil {
		t.Fatal("an unknown source was accepted")
	}
}

func TestCheckObservation(t *testing.T) {
	m := func(kv ...any) map[string]float64 {
		out := map[string]float64{}
		for i := 0; i < len(kv); i += 2 {
			out[kv[i].(string)] = float64(kv[i+1].(int))
		}
		return out
	}
	cases := []struct {
		name     string
		source   research.Source
		method   research.Method
		platform research.Platform
		metrics  map[string]float64
		ok       bool
	}{
		{"oembed gives no counts", research.SourceOEmbed, research.MethodAPI, research.TikTok, nil, true},
		{"oembed with views", research.SourceOEmbed, research.MethodAPI, research.TikTok, m("views", 10), false},
		{"oembed is tiktok only", research.SourceOEmbed, research.MethodAPI, research.Instagram, nil, false},
		{"oembed is not manual", research.SourceOEmbed, research.MethodManual, research.TikTok, nil, false},
		{"watch sheet gives all five", research.SourceWatchSheet, research.MethodManual, research.TikTok, m("views", 1, "likes", 2, "comments", 3, "saves", 4, "shares", 5), true},
		{"watch sheet is manual only", research.SourceWatchSheet, research.MethodAPI, research.TikTok, nil, false},
		{"youtube has no saves", research.SourceYouTubeAPI, research.MethodAPI, research.YouTube, m("saves", 1), false},
		{"youtube views", research.SourceYouTubeAPI, research.MethodAPI, research.YouTube, m("views", 100, "likes", 5), true},
		{"youtube api on tiktok", research.SourceYouTubeAPI, research.MethodAPI, research.TikTok, nil, false},
		{"instagram has no shares", research.SourceIGBusinessDiscovery, research.MethodAPI, research.Instagram, m("shares", 1), false},
		{"creator analytics have shares", research.SourceCreatorOAuth, research.MethodAPI, research.TikTok, m("shares", 9), true},
		{"unknown metric", research.SourceWatchSheet, research.MethodManual, research.TikTok, m("media_url", 1), false},
		{"negative", research.SourceWatchSheet, research.MethodManual, research.TikTok, m("views", -1), false},
		{"fraction", research.SourceWatchSheet, research.MethodManual, research.TikTok, map[string]float64{"views": 1.5}, false},
	}
	for _, c := range cases {
		err := research.CheckObservation(c.source, c.method, c.platform, c.metrics)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok = %v", c.name, err, c.ok)
		}
	}
}

func TestRetention(t *testing.T) {
	d := research.DefaultRetention()
	if d.Default != 365*research.Day || d.YouTube != 30*research.Day {
		t.Fatalf("defaults = %+v", d)
	}
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	if got := d.RetainUntil(research.SourceYouTubeAPI, at); !got.Equal(at.AddDate(0, 0, 30)) {
		t.Fatalf("youtube retain_until = %v", got)
	}
	if got := d.RetainUntil(research.SourceWatchSheet, at); !got.Equal(at.AddDate(0, 0, 365)) {
		t.Fatalf("watch sheet retain_until = %v", got)
	}
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	r, err := research.LoadRetention(env(nil))
	if err != nil || r != d {
		t.Fatalf("unset: %+v %v", r, err)
	}
	r, err = research.LoadRetention(env(map[string]string{research.EnvRetentionDays: "90", research.EnvYouTubeRetentionDays: "7"}))
	if err != nil || r.Default != 90*research.Day || r.YouTube != 7*research.Day {
		t.Fatalf("set: %+v %v", r, err)
	}
	// Never a silent clamp: YouTube may be lowered but not raised past its terms.
	for _, bad := range []map[string]string{
		{research.EnvRetentionDays: "0"}, {research.EnvRetentionDays: "-5"}, {research.EnvRetentionDays: "ten"}, {research.EnvRetentionDays: "731"},
		{research.EnvYouTubeRetentionDays: "31"}, {research.EnvYouTubeRetentionDays: "365"},
	} {
		if _, err := research.LoadRetention(env(bad)); err == nil {
			t.Errorf("%v was accepted", bad)
		}
	}
	if err := (research.Retention{Default: 400 * research.Day, YouTube: 31 * research.Day}).Valid(); err == nil {
		t.Fatal("a YouTube retention over 30 days is valid")
	}
}

func TestParsePostURL(t *testing.T) {
	good := map[string]research.PostRef{
		"https://www.tiktok.com/@Some.Creator/video/7301234567890123456?is_from_webapp=1": {Platform: research.TikTok, ExternalID: "7301234567890123456", Handle: "some.creator", URL: "https://www.tiktok.com/@Some.Creator/video/7301234567890123456"},
		"http://tiktok.com/@a_b/video/1234567890/":                                        {Platform: research.TikTok, ExternalID: "1234567890", Handle: "a_b", URL: "https://www.tiktok.com/@a_b/video/1234567890"},
		"https://www.instagram.com/reel/C1a2B3c4D5e/?igsh=x":                              {Platform: research.Instagram, ExternalID: "C1a2B3c4D5e", URL: "https://www.instagram.com/reel/C1a2B3c4D5e/"},
		"https://www.instagram.com/some.one/p/CxYz12345/":                                 {Platform: research.Instagram, ExternalID: "CxYz12345", URL: "https://www.instagram.com/p/CxYz12345/"},
		"https://www.youtube.com/shorts/abcDEF12345":                                      {Platform: research.YouTube, ExternalID: "abcDEF12345", URL: "https://www.youtube.com/shorts/abcDEF12345"},
		"https://youtu.be/abcDEF12345?t=3":                                                {Platform: research.YouTube, ExternalID: "abcDEF12345", URL: "https://www.youtube.com/watch?v=abcDEF12345"},
		"https://www.youtube.com/watch?v=abcDEF12345&list=x":                              {Platform: research.YouTube, ExternalID: "abcDEF12345", URL: "https://www.youtube.com/watch?v=abcDEF12345"},
	}
	for in, want := range good {
		got, err := research.ParsePostURL(in)
		if err != nil || got != want {
			t.Errorf("ParsePostURL(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, bad := range []string{
		"", "not a url", "ftp://www.tiktok.com/@a/video/1234567",
		"https://www.tiktok.com/@creator",                         // a profile
		"https://www.tiktok.com/tag/fitness",                      // a hashtag page
		"https://vm.tiktok.com/ZMabc123/",                         // a short link needs a network call
		"https://www.tiktok.com/@a/video/12",                      // not an id
		"https://evil.example.com/@a/video/1234567",               // another host
		"https://tiktok.com.evil.example/@a/video/1234567",        // look-alike host
		"https://user:pw@www.tiktok.com/@a/video/1234567",         // credentials
		"https://www.tiktok.com:8443/@a/video/1234567",            // port
		"https://www.instagram.com/some.one/",                     // a profile
		"https://www.instagram.com/explore/tags/fitness/",         // a tag page
		"https://www.youtube.com/@channel",                        // a channel
		"https://www.youtube.com/watch?v=short",                   // not an id
		"https://cdn.example.com/video.mp4",                       // a media file
		"https://v16.tiktokcdn.com/abc/video/tos/xyz.mp4",         // a media host
		"https://www.tiktok.com/@a/video/1234567/../../video.mp4", // path tricks
	} {
		if got, err := research.ParsePostURL(bad); err == nil {
			t.Errorf("ParsePostURL(%q) accepted: %+v", bad, got)
		}
	}
}

func TestHandleAndProfileURL(t *testing.T) {
	if h, err := research.NormalizeHandle(" @Some_One.99 "); err != nil || h != "some_one.99" {
		t.Fatalf("NormalizeHandle = %q, %v", h, err)
	}
	for _, bad := range []string{"", "@", "a b", "a/b", "https://x.com/a", strings.Repeat("a", 65), "é"} {
		if _, err := research.NormalizeHandle(bad); err == nil {
			t.Errorf("NormalizeHandle(%q) accepted", bad)
		}
	}
	if u, err := research.ProfileURL(research.TikTok, "@Gino"); err != nil || u != "https://www.tiktok.com/@gino" {
		t.Fatalf("ProfileURL = %q, %v", u, err)
	}
	if _, err := research.ProfileURL("myspace", "gino"); err == nil {
		t.Fatal("an unknown platform has a profile URL")
	}
}

func TestCheckAnnotation(t *testing.T) {
	v := research.DefaultVocabulary()
	n := 3
	ok := research.Annotation{FormatID: "tutorial_101", HookKind: "set_first", HookTextPattern: "<TOPIC> 101", TextPattern: "single_title", CutsEstimate: &n, CTA: "save", Topic: "body:back", Annotator: "sam", Confidence: 0.8}
	if err := v.CheckAnnotation(ok); err != nil {
		t.Fatalf("a good annotation: %v", err)
	}
	bad := map[string]func(a *research.Annotation){
		"unknown format":        func(a *research.Annotation) { a.FormatID = "made_up_format" },
		"unknown topic":         func(a *research.Annotation) { a.Topic = "body:wings" },
		"topic not a ref":       func(a *research.Annotation) { a.Topic = "back" },
		"text pattern":          func(a *research.Annotation) { a.TextPattern = "paragraphs" },
		"cta":                   func(a *research.Annotation) { a.CTA = "buy" },
		"creator words":         func(a *research.Annotation) { a.HookTextPattern = "Never skip leg day again" },
		"words in a pattern":    func(a *research.Annotation) { a.HookTextPattern = "<TOPIC> secrets" },
		"unbalanced slot":       func(a *research.Annotation) { a.HookTextPattern = "<TOPIC 101" },
		"pattern too long":      func(a *research.Annotation) { a.HookTextPattern = strings.Repeat("A", 61) },
		"no annotator":          func(a *research.Annotation) { a.Annotator = " " },
		"confidence out":        func(a *research.Annotation) { a.Confidence = 1.5 },
		"negative cuts":         func(a *research.Annotation) { m := -1; a.CutsEstimate = &m },
		"hook kind with spaces": func(a *research.Annotation) { a.HookKind = "Show the set" },
	}
	for name, mutate := range bad {
		a := ok
		mutate(&a)
		if err := v.CheckAnnotation(a); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestCheckPostTime(t *testing.T) {
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	past := now.AddDate(0, -1, 0)
	future := now.AddDate(0, 0, 10)
	if err := research.CheckPostTime(&past, "id", now); err != nil {
		t.Fatal(err)
	}
	if err := research.CheckPostTime(nil, "", now); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		t   *time.Time
		src string
	}{{&past, ""}, {nil, "id"}, {&past, "guess"}, {&future, "manual"}} {
		if err := research.CheckPostTime(c.t, c.src, now); err == nil {
			t.Errorf("CheckPostTime(%v, %q) accepted", c.t, c.src)
		}
	}
}

func TestCheckFixtureRefusesMedia(t *testing.T) {
	good := `{"title":"A caption #fitness","author_name":"x","author_url":"https://www.tiktok.com/@x","thumbnail_url":"https://p16.example.com/thumb.jpeg?x=1","html":"<blockquote></blockquote>","stats":{"views":10}}`
	if err := research.CheckFixture([]byte(good)); err != nil {
		t.Fatalf("a metadata fixture: %v", err)
	}
	bad := map[string]string{
		"media_url key":        `{"media_url":"https://example.com/a"}`,
		"nested video_url":     `{"items":[{"video_url":"x"}]}`,
		"play address":         `{"video":{"play_addr":{"url_list":[]}}}`,
		"download":             `{"download_addr":"x"}`,
		"bitrate info":         `{"bitrate_info":[]}`,
		"transcript":           `{"transcript":"..."}`,
		"mp4 link":             `{"url":"https://cdn.example.com/a/b/clip.mp4?sig=1"}`,
		"hls link":             `{"url":"https://cdn.example.com/a/index.m3u8"}`,
		"data uri":             `{"thumb":"data:image/png;base64,AAAA"}`,
		"huge string":          `{"blob":"` + strings.Repeat("A", research.MaxFixtureString+1) + `"}`,
		"not json":             `video bytes`,
		"audio link in a list": `{"a":["https://x.example/s.mp3"]}`,
	}
	for name, doc := range bad {
		if err := research.CheckFixture([]byte(doc)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	err := research.CheckFixture([]byte(`{"video_url":"x"}`))
	if !errors.Is(err, research.ErrMedia) || strings.Contains(err.Error(), `"x"`) {
		t.Fatalf("the refusal names the key and not the value: %v", err)
	}
}

func TestCheckFixtureDir(t *testing.T) {
	fsys := fstest.MapFS{
		"fixtures/ok/oembed.json":      {Data: []byte(`{"title":"t"}`)},
		"fixtures/ok/sheet.csv":        {Data: []byte("url,views\n")},
		"fixtures/bad/clip.mp4":        {Data: []byte("x")},
		"fixtures/bad/nested/api.json": {Data: []byte(`{"media_url":"x"}`)},
	}
	if err := research.CheckFixtureDir(fsys, "fixtures/ok"); err != nil {
		t.Fatalf("ok dir: %v", err)
	}
	err := research.CheckFixtureDir(fsys, "fixtures/bad")
	if err == nil || !strings.Contains(err.Error(), "clip.mp4") || !strings.Contains(err.Error(), "api.json") {
		t.Fatalf("bad dir: %v", err)
	}
}
