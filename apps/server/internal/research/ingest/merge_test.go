package ingest_test

import (
	"context"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/research"
	"github.com/nilpntr/content-fabrique/internal/research/ingest"
	"github.com/nilpntr/content-fabrique/internal/research/oembed"
)

// A later watch that fills only some cells keeps what the same person noted before; a row whose oEmbed call fails leaves no creator.
func TestImportMergesAndLeavesNothingOnFailure(t *testing.T) {
	ctx := context.Background()
	const link = "https://www.tiktok.com/@synthetic_lifter_a/video/7657610837863983488"
	parse := func(sheet string) []research.SheetRow {
		t.Helper()
		rows, err := research.ParseWatchSheet("s.csv", []byte(sheet), research.SheetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	db := newFakeDB()
	if rep, err := ingest.Import(ctx, db, parse("url,observer,format,hook_text,notes\n"+link+",sam,one_take,<TOPIC> 101,long take\n"), ingest.Options{}); err != nil || rep.Imported != 1 {
		t.Fatalf("first: %+v %v", rep, err)
	}
	upper := strings.Replace(link, "synthetic_lifter_a", "Synthetic_Lifter_A", 1)
	if rep, err := ingest.Import(ctx, db, parse("url,observer,cuts,saves\n"+upper+",sam,5,1.2K\n"), ingest.Options{}); err != nil || rep.Imported != 1 {
		t.Fatalf("second: %+v %v", rep, err)
	}
	if len(db.posts) != 1 {
		t.Fatalf("the same post in another case became %d posts", len(db.posts))
	}
	var got research.Annotation
	for _, a := range db.annotations {
		got = a
	}
	if got.FormatID != "one_take" || got.HookTextPattern != "<TOPIC> 101" || got.Notes != "long take" || got.CutsEstimate == nil || *got.CutsEstimate != 5 {
		t.Fatalf("merged annotation: %+v", got)
	}

	db = newFakeDB()
	gone := &countingResolver{err: oembed.ErrUnavailable}
	rep, err := ingest.Import(ctx, db, parse("url,observer\n"+link+",sam\n"), ingest.Options{Resolver: gone})
	if err != nil || len(rep.Failed) != 1 || rep.NewCreators != 0 || len(db.creators) != 0 || len(db.posts) != 0 {
		t.Fatalf("unavailable post: %+v %v, %d creators", rep, err, len(db.creators))
	}
}
