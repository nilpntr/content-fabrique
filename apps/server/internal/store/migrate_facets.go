package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/pressly/goose/v3"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/taxonomy"
)

// FacetBackfillVersion is the Go migration that labels the existing clips with the vocabulary (00025 adds the columns): every footage
// asset's old free-form tags (legacy_tags) and the title the quick look wrote are mapped through the synonym map, and a clip the probe
// found recorded at a high frame rate is slow motion. No model is called. A clip that already has facets (a quick look of quicklook.v4
// ran before this migration) is left alone. Down clears what it wrote (00025's down then drops the columns).
const FacetBackfillVersion = 26

func facetBackfill() *goose.Migration {
	return goose.NewGoMigration(FacetBackfillVersion,
		&goose.GoFunc{RunTx: func(ctx context.Context, tx *sql.Tx) error { _, err := BackfillFacets(ctx, tx, false); return err }},
		&goose.GoFunc{RunTx: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE assets SET facets = '{}', exercise = '', facet_words = '' WHERE cardinality(facets) > 0 OR exercise <> ''`)
			return err
		}},
	)
}

// Execer is what BackfillFacets runs on: a transaction of the migration, or a connection of cfctl.
type Execer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// BackfillFacets maps the old free-form tags (and the title the quick look wrote, and the probe's frame rate) of every footage asset of
// every workspace onto the vocabulary (taxonomy.FromLegacy) and stores the facets, the exercise and their search words. With again it
// also rewrites assets that have facets already (after a better synonym map); without, they are left alone. It returns how many assets
// it labelled. SYSTEM: it works across workspaces, row by row with each row's own id.
func BackfillFacets(ctx context.Context, db Execer, again bool) (int, error) {
	tax := taxonomy.Default()
	rows, err := db.QueryContext(ctx, `
		SELECT id, array_to_json(legacy_tags)::text, title, title_source, COALESCE(probe::text, '') FROM assets
		WHERE purpose = 'footage' AND kind = 'video' AND ($1 OR (cardinality(facets) = 0 AND exercise = ''))
		  AND (cardinality(legacy_tags) > 0 OR title_source = 'quicklook' OR probe IS NOT NULL)
		ORDER BY id`, again)
	if err != nil {
		return 0, fmt.Errorf("facet backfill: list: %w", err)
	}
	type update struct {
		id       string
		facets   string // a JSON array: database/sql has no text[] of its own
		exercise string
		words    string
	}
	var updates []update
	for rows.Next() {
		var (
			id, legacyJSON, title, source, probe string
			legacy                               []string
		)
		if err := rows.Scan(&id, &legacyJSON, &title, &source, &probe); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("facet backfill: read: %w", err)
		}
		if err := json.Unmarshal([]byte(legacyJSON), &legacy); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("facet backfill: the tags of %s: %w", id, err)
		}
		set, exercise := tax.FromLegacy(taxonomy.Legacy{Tags: legacy, Title: title, FromQuickLook: source == TitleFromQuickLook})
		if media.SlowMotionProbe([]byte(probe)) {
			set = tax.WithProbe(set, taxonomy.Ref{Facet: "type", Value: "slow_motion"})
		}
		if len(set) == 0 && exercise == "" {
			continue
		}
		facets, err := json.Marshal(set.Strings())
		if err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("facet backfill: %w", err)
		}
		updates = append(updates, update{id: id, facets: string(facets), exercise: exercise, words: tax.SearchText(set)})
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("facet backfill: %w", err)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("facet backfill: %w", err)
	}
	for _, u := range updates {
		if _, err := db.ExecContext(ctx, `UPDATE assets SET facets = ARRAY(SELECT jsonb_array_elements_text($2::jsonb)), exercise = $3, facet_words = $4 WHERE id = $1`,
			u.id, u.facets, u.exercise, u.words); err != nil {
			return 0, fmt.Errorf("facet backfill: write %s: %w", u.id, err)
		}
		// The moments of the clip analysis carry the clip's labels.
		if _, err := db.ExecContext(ctx, `UPDATE moments SET labels = ARRAY(SELECT jsonb_array_elements_text($2::jsonb)) WHERE asset_id = $1 AND source = 'analysis'`, u.id, u.facets); err != nil {
			return 0, fmt.Errorf("facet backfill: relabel the moments of %s: %w", u.id, err)
		}
	}
	return len(updates), nil
}
