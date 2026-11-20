package research_test

import (
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/research"
)

func TestWatchSheetLeniency(t *testing.T) {
	const tt = "https://www.tiktok.com/@lifter_x/video/7680618118675196288"
	// A hand-typed row may stop early; a spreadsheet's empty trailing columns are ignored; a Dutch phone's 1,2K is 1200.
	sheet := "url,observer,saves,shares,notes,,\n" + tt + ",sam,\"1,2K\"\n"
	rows, err := research.ParseWatchSheet("s.csv", []byte(sheet), research.SheetOptions{Now: sheetNow})
	if err != nil || len(rows) != 1 || rows[0].Metrics["saves"] != 1200 {
		t.Fatalf("lenient sheet: %+v %v", rows, err)
	}
	for name, c := range map[string]struct{ sheet, want string }{
		"a value under no header":   {"url,observer,\n" + tt + ",sam,x\n", "no name"},
		"an ambiguous 1,2":          {"url,observer,saves\n" + tt + ",sam,\"1,2\"\n", "saves"},
		"the reserved observer":     {"url,observer\n" + tt + ",OEmbed\n", "reserved"},
		"an id without a post time": {"url,observer\nhttps://www.tiktok.com/@lifter_x/video/123456,sam\n", "no post time"},
	} {
		if _, err := research.ParseWatchSheet("s.csv", []byte(c.sheet), research.SheetOptions{Now: sheetNow}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
	// One post spelled with another case of the handle is the same post.
	dup := "url,observer\n" + tt + ",sam\n" + strings.Replace(tt, "lifter_x", "Lifter_X", 1) + ",sam\n"
	if _, err := research.ParseWatchSheet("s.csv", []byte(dup), research.SheetOptions{Now: sheetNow}); err == nil || !strings.Contains(err.Error(), "same post") {
		t.Errorf("case duplicate: %v", err)
	}
}
