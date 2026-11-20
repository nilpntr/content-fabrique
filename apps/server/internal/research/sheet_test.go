package research_test

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/research"
)

var sheetNow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

// The recorded synthetic sheet: 30 posts by 5 creators, 10 of them with the date the post shows.
func TestParseTheRecordedWatchSheet(t *testing.T) {
	raw, err := os.ReadFile("testdata/r2/watch-sheet.csv")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := research.ParseWatchSheet("watch-sheet.csv", raw, research.SheetOptions{Now: sheetNow})
	if err != nil {
		t.Fatal(err)
	}
	handles := map[string]int{}
	shown := 0
	for _, r := range rows {
		handles[r.Handle]++
		if r.PostedAt != nil {
			shown++
			if !r.PostedDateOnly {
				t.Errorf("line %d: a shown date is a date", r.Line)
			}
		}
		if r.Post.Platform != research.TikTok || r.Observer != "sam" || r.DurationS == nil || r.Metrics[research.MetricSaves] == 0 ||
			r.Annotation.Annotator != "sam" || !r.HasAnnotation() || len(r.RawRef) != 64 {
			t.Errorf("line %d: %+v", r.Line, r)
		}
	}
	if len(rows) != 30 || len(handles) != 5 || shown != 10 {
		t.Fatalf("rows %d, creators %d, shown dates %d; want 30, 5, 10", len(rows), len(handles), shown)
	}
	// A shown count is read as a viewer sees it.
	if got := rows[0].Metrics[research.MetricViews]; got != 177100 {
		t.Errorf("177.1K = %v", got)
	}
	if rows[0].Annotation.Notes != "long take, title over the set" || *rows[0].DurationS != 36 {
		t.Errorf("first row: %+v", rows[0])
	}
	// The same rows parse to the same fingerprints (so a second import stores nothing new).
	again, err := research.ParseWatchSheet("watch-sheet.csv", raw, research.SheetOptions{Now: sheetNow})
	if err != nil || again[7].RawRef != rows[7].RawRef {
		t.Fatalf("fingerprint is not stable: %v", err)
	}
}

func TestTheTemplateIsAnEmptySheet(t *testing.T) {
	tpl := research.WatchSheetTemplate()
	rows, err := research.ParseWatchSheet("template.csv", tpl, research.SheetOptions{Now: sheetNow})
	if err != nil || len(rows) != 0 {
		t.Fatalf("template: %d rows, %v", len(rows), err)
	}
	for _, col := range research.SheetColumns() {
		if !strings.Contains(string(tpl), col) {
			t.Errorf("the template does not explain %q", col)
		}
	}
}

func TestWatchSheetRefusals(t *testing.T) {
	const tt = "https://www.tiktok.com/@lifter_x/video/7680618118675196288"
	cases := map[string]struct {
		sheet string
		want  string
		media bool
	}{
		"a media column":           {"url,video_url\n" + tt + ",x\n", "video_url", true},
		"an unknown column":        {"url,mood\n" + tt + ",happy\n", `unknown column "mood"`, false},
		"no url column":            {"handle\nlifter_x\n", "no url column", false},
		"a profile link":           {"url,observer\nhttps://www.tiktok.com/@lifter_x,sam\n", "not the link", false},
		"the creator's words":      {"url,observer,hook_text\n" + tt + ",sam,never skip leg day\n", "capitals", false},
		"an unknown format":        {"url,observer,format\n" + tt + ",sam,made_up\n", "made_up", false},
		"an unknown topic":         {"url,observer,topic\n" + tt + ",sam,body:wings\n", "body:wings", false},
		"a link in the notes":      {"url,observer,notes\n" + tt + ",sam,see www.example.com\n", "no links", false},
		"a media link in a note":   {"url,observer,notes\n" + tt + ",sam,https://example.com/clip.mp4\n", "media file", true},
		"a long note":              {"url,observer,notes\n" + tt + ",sam," + strings.Repeat("x", 281) + "\n", "280", false},
		"no observer":              {"url,views\n" + tt + ",12\n", "observer", false},
		"a bad count":              {"url,observer,saves\n" + tt + ",sam,lots\n", "saves", false},
		"a fraction without K":     {"url,observer,likes\n" + tt + ",sam,1.5\n", "likes", false},
		"a post in the future":     {"url,observer,posted_at\n" + tt + ",sam,2027-01-01\n", "future", false},
		"a bad length":             {"url,observer,length\n" + tt + ",sam,1:75\n", "length", false},
		"another creator's handle": {"url,observer,handle\n" + tt + ",sam,someone_else\n", "not the handle in the link", false},
		"an Instagram link alone":  {"url,observer\nhttps://www.instagram.com/reel/AbCdEf123/,sam\n", "fill the handle", false},
		"the same post twice":      {"url,observer\n" + tt + ",sam\n" + tt + "?lang=en,sam\n", "same post as line 2", false},
		"a column twice":           {"url,url\n" + tt + "," + tt + "\n", "twice", false},
		"an unknown caption form":  {"url,observer,caption_pattern\n" + tt + ",sam,karaoke\n", "karaoke", false},
	}
	for name, c := range cases {
		_, err := research.ParseWatchSheet("s.csv", []byte(c.sheet), research.SheetOptions{Now: sheetNow})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
		if c.media && !errors.Is(err, research.ErrMedia) {
			t.Errorf("%s: err = %v, want ErrMedia", name, err)
		}
	}
}

func TestWatchSheetJSONAndDefaults(t *testing.T) {
	sheet := `{"rows": [
	  {"url": "https://www.instagram.com/reel/AbCdEf123/", "handle": "@Lifter_Y", "views": 12500, "saves": "1.2K", "length": "42s",
	   "posted_at": "2026-11-01T18:30:00+01:00", "format": "One_Take", "notes": null},
	  {"url": "https://youtu.be/abcDEF12345", "handle": "lifter-z", "observer": "ana", "first_cut_s": "2.5s", "cuts": 3}
	]}`
	rows, err := research.ParseWatchSheet("week.json", []byte(sheet), research.SheetOptions{Observer: "sam", Now: sheetNow})
	if err != nil {
		t.Fatal(err)
	}
	ig, yt := rows[0], rows[1]
	if ig.Handle != "lifter_y" || ig.Observer != "sam" || ig.Metrics["views"] != 12500 || ig.Metrics["saves"] != 1200 || *ig.DurationS != 42 ||
		ig.PostedDateOnly || !ig.PostedAt.Equal(time.Date(2026, 11, 1, 17, 30, 0, 0, time.UTC)) || ig.Annotation.FormatID != "one_take" {
		t.Errorf("instagram row: %+v", ig)
	}
	if yt.Observer != "ana" || yt.Post.URL != "https://www.youtube.com/watch?v=abcDEF12345" || *yt.Annotation.FirstCutS != 2.5 || *yt.Annotation.CutsEstimate != 3 {
		t.Errorf("youtube row: %+v", yt)
	}
	for name, bad := range map[string]string{
		"not a list":    `{"url": "x"}`,
		"a nested type": `[{"url": "https://youtu.be/abcDEF12345", "handle": "x", "observer": "sam", "views": [1]}]`,
		"a media key":   `[{"url": "https://youtu.be/abcDEF12345", "play_addr": "x"}]`,
	} {
		if _, err := research.ParseWatchSheet("s.json", []byte(bad), research.SheetOptions{Now: sheetNow}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestTikTokPostTime(t *testing.T) {
	// The example post of TikTok's embed documentation (@scout2015): its id decodes to 2019-07-27 13:32:33 UTC.
	got, err := research.TikTokPostTime("6718335390845095173")
	if err != nil || !got.Equal(time.Date(2019, 7, 27, 13, 32, 33, 0, time.UTC)) {
		t.Fatalf("scout2015 = %v %v", got, err)
	}
	for _, bad := range []string{"", "abc", "12345", "99999999999999999999"} {
		if _, err := research.TikTokPostTime(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestShownDateMatches(t *testing.T) {
	day := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		posted time.Time
		want   bool
	}{
		{day.Add(10 * time.Hour), true},
		{day.Add(-90 * time.Minute), true},  // 22:30 UTC the day before: already the 27th in Amsterdam
		{day.Add(35 * time.Hour), true},     // 11:00 UTC on the 28th: still the 27th at UTC-12
		{day.Add(-15 * time.Hour), false},   // the 26th everywhere
		{day.Add(36*time.Hour + 1), false},  // the 28th everywhere
		{day.Add(3 * research.Day), false},  // another day
		{day.Add(-3 * research.Day), false}, // another day
		{day.Add(12*time.Hour + 30), true},  // midday
		{day.Add(24*time.Hour - time.Second), true},
	} {
		if got := research.ShownDateMatches(c.posted, day, true); got != c.want {
			t.Errorf("posted %v, shown %v: %v, want %v", c.posted, day, got, c.want)
		}
	}
	at := time.Date(2026, 9, 27, 18, 0, 0, 0, time.UTC)
	if !research.ShownDateMatches(at.Add(30*time.Minute), at, false) || research.ShownDateMatches(at.Add(2*time.Hour), at, false) {
		t.Error("a shown time is matched within an hour")
	}
}

// The recorded replay fixtures pass the media guard: metadata only.
func TestTheRecordedFixturesHoldNoMedia(t *testing.T) {
	if err := research.CheckFixtureDir(os.DirFS("testdata"), "r2"); err != nil {
		t.Fatal(err)
	}
}
