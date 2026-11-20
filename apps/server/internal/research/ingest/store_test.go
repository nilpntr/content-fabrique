package ingest_test

import (
	"context"
	"os"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/jobs/jobstest"
	"github.com/nilpntr/content-fabrique/internal/research/ingest"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/store/storetest"
)

func TestMain(m *testing.M) { os.Exit(storetest.Run(m)) }

// The recorded sheet into a real database (Docker): the store's checks and the CHECK constraints accept every row, a row is one
// transaction, and a second import stores nothing twice. Replay only: no network.
func TestImportIntoTheDatabase(t *testing.T) {
	ctx := context.Background()
	srv := storetest.Start(t)
	dsn := srv.SuperDSN(srv.NewDatabase(t, ""))
	jobstest.Migrate(t, dsn)
	st, err := store.Open(ctx, dsn, store.Options{AppName: "research-ingest-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)

	rows := recordedSheet(t)
	db := ingest.FromStore(st)
	rep, err := ingest.Import(ctx, db, rows, ingest.Options{Resolver: replayResolver(t)})
	if err != nil || len(rep.Failed) != 0 || rep.Imported != 30 || rep.NewCreators != 5 || rep.Observations != 60 || rep.TimeMatches != 10 {
		t.Fatalf("import: %+v %v", rep, err)
	}
	creators, err := st.ListResearchCreators(ctx, "")
	if err != nil || len(creators) != 5 {
		t.Fatalf("creators: %d %v", len(creators), err)
	}
	p, err := st.GetResearchPostByURL(ctx, rows[12].Post.URL)
	if err != nil || p.PostedAtSource == nil || *p.PostedAtSource != "id" {
		t.Fatalf("post: %+v %v", p, err)
	}
	notes, err := st.ListResearchAnnotationsForPost(ctx, p.ID)
	if err != nil || len(notes) != 2 {
		t.Fatalf("annotations: %d %v", len(notes), err)
	}
	again, err := ingest.Import(ctx, db, rows, ingest.Options{Resolver: replayResolver(t)})
	if err != nil || again.Observations != 0 || again.Duplicates != 60 {
		t.Fatalf("second import: %+v %v", again, err)
	}

	// A forgotten creator's rows are refused.
	if _, err := st.ForgetResearchCreator(ctx, "tiktok", "synthetic_lifter_c"); err != nil {
		t.Fatal(err)
	}
	rep, err = ingest.Import(ctx, db, rows, ingest.Options{Resolver: replayResolver(t)})
	if err != nil || len(rep.Failed) != 6 || rep.Imported != 24 {
		t.Fatalf("after forget: %+v %v", rep, err)
	}
}
