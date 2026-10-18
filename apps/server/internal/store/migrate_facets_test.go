package store_test

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/store/storetest"
)

// The clips of a workspace from before the vocabulary keep their old free-form tags (legacy_tags, hidden) and get labels mapped from
// them by the migration, without a model: the founder's five clips (2026-10-18) as they were tagged.
func TestTheMigrationLabelsTheOldClips(t *testing.T) {
	srv := storetest.Start(t)
	dsn := srv.SuperDSN(srv.NewDatabase(t, ""))
	ctx := context.Background()
	m, err := store.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	if _, err := m.UpTo(ctx, 24); err != nil {
		t.Fatal(err)
	}

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var ws string
	if err := conn.QueryRow(ctx, `INSERT INTO workspaces (clerk_org_id, name) VALUES ('org_facets', 'Gino') RETURNING id`).Scan(&ws); err != nil {
		t.Fatal(err)
	}
	type clip struct {
		title, source, purpose, probe string
		tags                          []string
	}
	clips := map[string]clip{
		"cable":   {"Seated cable row, back view", "quicklook", "footage", "", []string{"cable row", "seated row", "gym", "back view", "back exercise", "cable machine"}},
		"machine": {"Seated machine row, side view in a gym", "quicklook", "footage", "", []string{"gym", "machine row", "back training", "side view", "machine"}},
		"wrong":   {"Back extension on bench, low angle", "quicklook", "footage", "", []string{"back extension", "gym", "low angle", "hyperextension bench"}},
		"pose":    {"Back pose, rear view against dark wall", "quicklook", "footage", "", []string{"back pose", "posing", "back view", "dark background", "physique"}},
		"mine":    {"my favourite set", "user", "footage", "", []string{"pulling", "red lighting", "strength training"}},
		"slowmo":  {"522672b3ba3348eeb7da92380ebb947f", "filename", "footage", `{"fps":{"num":21525,"den":716,"value":30.06},"baseFps":{"num":120,"den":1,"value":120}}`, []string{}},
		"ref":     {"My best video", "quicklook", "style_reference", "", []string{"cable row"}},
		"nothing": {"IMG_0001", "filename", "footage", "", []string{"gym", "dark background"}},
	}
	idOf := map[string]string{}
	for name, c := range clips {
		var id string
		probe := &c.probe
		if c.probe == "" {
			probe = nil
		}
		err := conn.QueryRow(ctx, `INSERT INTO assets (workspace_id, kind, status, filename, content_type, size_bytes, title, title_source, tags, r2_key, purpose, probe)
			VALUES ($1, 'video', 'ready', $2, 'video/mp4', 1000, $3, $4, $5, $6, $7, $8::jsonb) RETURNING id`,
			ws, name+".mp4", c.title, c.source, c.tags, "ws/"+name, c.purpose, probe).Scan(&id)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		idOf[name] = id
	}

	if _, err := m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	type labels struct {
		facets   []string
		exercise string
		legacy   []string
		words    string
	}
	read := func(name string) labels {
		t.Helper()
		var l labels
		if err := conn.QueryRow(ctx, `SELECT facets, exercise, legacy_tags, facet_words FROM assets WHERE id = $1`, idOf[name]).Scan(&l.facets, &l.exercise, &l.legacy, &l.words); err != nil {
			t.Fatal(err)
		}
		return l
	}
	for name, want := range map[string]labels{
		"cable":   {facets: []string{"type:lift", "body:back", "movement:row", "angle:back", "equipment:cable"}, exercise: "Seated cable row"},
		"machine": {facets: []string{"type:lift", "body:back", "movement:row", "angle:side", "equipment:machine"}, exercise: "Seated machine row"},
		// The old tags said back extension for what may be a row: the mapping keeps what they said; cfctl assets retag reads it again.
		"wrong":   {facets: []string{"type:lift", "body:back", "movement:hinge"}, exercise: "Back extension"},
		"pose":    {facets: []string{"type:pose", "body:back", "angle:back"}},
		"mine":    {facets: []string{"type:lift", "body:back", "movement:pull"}},
		"slowmo":  {facets: []string{"type:slow_motion"}},
		"ref":     {},
		"nothing": {},
	} {
		got := read(name)
		sameFacets := slices.Equal(got.facets, want.facets) || (len(got.facets) == 0 && len(want.facets) == 0)
		if !sameFacets || got.exercise != want.exercise {
			t.Errorf("%s: facets %v exercise %q, want %v %q", name, got.facets, got.exercise, want.facets, want.exercise)
		}
		if !slices.Equal(got.legacy, clips[name].tags) && len(clips[name].tags) > 0 {
			t.Errorf("%s: the old tags were not kept: %v", name, got.legacy)
		}
	}
	if w := read("cable").words; !strings.Contains(w, "Lifting") || !strings.Contains(w, "Back view") {
		t.Errorf("facet words = %q", w)
	}

	// The founder's 22 chips were 22 labels; now the library has a handful.
	var distinct int
	if err := conn.QueryRow(ctx, `SELECT count(DISTINCT f) FROM assets, unnest(facets) f WHERE workspace_id = $1`, ws).Scan(&distinct); err != nil || distinct > 12 {
		t.Errorf("%d distinct labels, %v", distinct, err)
	}

	// The search finds the clips by their labels' words, and the filter by the labels.
	st, err := store.Open(ctx, dsn, store.Options{AppName: "facets-test"})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	wsID, err := ids.ParseWorkspaceID(mustTypeID(t, ws))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := st.SearchAssets(ctx, wsID, store.SearchWords("posing"), nil, 10); err != nil || len(got) != 1 || got[0].ID.UUID.String() != idOf["pose"] {
		t.Errorf("search posing = %d, %v", len(got), err)
	}
	if got, err := st.FindAssets(ctx, wsID, store.AssetQuery{Facets: []string{"movement:row"}, Limit: 10}); err != nil || len(got) != 2 {
		t.Errorf("filter movement:row = %d, %v", len(got), err)
	}
	// The old tags no longer find anything ("hyperextension" was only a tag).
	if got, _ := st.SearchAssets(ctx, wsID, store.SearchWords("hyperextension"), nil, 10); len(got) != 0 {
		t.Errorf("the hidden tags are still searched: %d", len(got))
	}

	// Down clears what the backfill wrote; up writes it again, the same.
	if _, err := m.Down(ctx); err != nil {
		t.Fatal(err)
	}
	if got := read("cable"); len(got.facets) != 0 || got.exercise != "" {
		t.Errorf("after down: %+v", got)
	}
	if _, err := m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if got := read("cable"); len(got.facets) != 5 {
		t.Errorf("after up again: %+v", got)
	}
	// A run with again keeps a label a later quick look wrote only when asked to rewrite; without it, labelled clips stay as they are.
	if _, err := conn.Exec(ctx, `UPDATE assets SET facets = '{type:talk}' WHERE id = $1`, idOf["cable"]); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sqlDB.Close() }()
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := store.BackfillFacets(ctx, tx, false); err != nil || n != 0 {
		t.Errorf("a second backfill labelled %d clips, %v", n, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := read("cable"); !slices.Equal(got.facets, []string{"type:talk"}) {
		t.Errorf("a backfill overwrote a newer label: %v", got.facets)
	}
}

// mustTypeID turns a uuid into the workspace's TypeID.
func mustTypeID(t *testing.T, uuid string) string {
	t.Helper()
	var w ids.WorkspaceID
	if err := w.UUID.UnmarshalText([]byte(uuid)); err != nil {
		t.Fatal(err)
	}
	return w.String()
}
