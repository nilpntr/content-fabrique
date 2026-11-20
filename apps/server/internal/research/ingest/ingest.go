// Package ingest writes checked watch-sheet rows (research.ParseWatchSheet) into the research tables, resolving each TikTok link through
// oEmbed on the way (phase R2 of docs/design/creator-research-engine.md).
//
// Every write goes through the store's checked methods, so the R1 rules apply unchanged: the source allow-list and its metrics, no media,
// retention on every observation and the tombstones of `cfctl research forget` (a forgotten creator is refused before any oEmbed call).
// A row is written in one transaction. The same row imported twice is stored once (its RawRef, and oEmbed's hash of the answer, are
// compared with the observations already stored).
package ingest

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/research"
	"github.com/nilpntr/content-fabrique/internal/research/oembed"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// Writer is the part of the store an import writes with.
type Writer interface {
	GetResearchCreator(ctx context.Context, p research.Platform, handle string) (store.ResearchCreator, error)
	UpsertResearchCreator(ctx context.Context, in store.NewResearchCreator) (store.ResearchCreator, error)
	UpsertResearchPost(ctx context.Context, in store.NewResearchPost) (store.ResearchPost, error)
	ListResearchEvidenceForPost(ctx context.Context, postID uuid.UUID) ([]store.ResearchEvidence, error)
	AddResearchEvidence(ctx context.Context, in store.NewResearchEvidence) (store.ResearchEvidence, error)
	SaveResearchAnnotation(ctx context.Context, postID uuid.UUID, a research.Annotation) (store.ResearchAnnotation, error)
}

// DB is a Writer that can run a function in a transaction.
type DB interface {
	Writer
	InTx(ctx context.Context, fn func(Writer) error) error
}

// FromStore adapts the store.
func FromStore(st *store.Store) DB { return storeDB{st} }

type storeDB struct{ *store.Store }

func (s storeDB) InTx(ctx context.Context, fn func(Writer) error) error {
	return s.Store.InTx(ctx, func(tx *store.Store) error { return fn(tx) })
}

// Resolver resolves a TikTok post link (oembed.Client).
type Resolver interface {
	Resolve(ctx context.Context, link string) (oembed.Result, error)
}

// OEmbedAnnotator is the annotator of the caption statistics read from oEmbed. A person cannot use it as their name.
const OEmbedAnnotator = "oembed"

// Options configure an import.
type Options struct {
	// Resolver resolves TikTok links; nil skips oEmbed (the post time still comes from the id).
	Resolver Resolver
	// Retention sets retain_until; the zero value is the default (12 months).
	Retention research.Retention
	// Now is the clock (the observed time of oEmbed answers and of rows without watched_at); nil is time.Now.
	Now func() time.Time
}

// RowError is a row that was not imported.
type RowError struct {
	Line int
	URL  string
	Err  error
}

func (e RowError) Error() string { return fmt.Sprintf("line %d (%s): %v", e.Line, e.URL, e.Err) }

// Report says what an import did.
type Report struct {
	Rows        int
	Imported    int
	NewCreators int
	// Resolved counts the posts oEmbed answered for (from the network, the cache or a fixture).
	Resolved int
	// Observations counts the observations stored; Duplicates the ones already stored by an earlier import.
	Observations int
	Duplicates   int
	Annotations  int
	// TimeChecks counts the TikTok rows whose sheet shows a posted date; TimeMatches those whose id time agrees with it.
	TimeChecks  int
	TimeMatches int
	Warnings    []string
	Failed      []RowError
}

// Import writes the rows in order. A row that fails is reported and the next one goes on; a 429 from TikTok or a cancelled context stops
// the import and is returned with the report so far.
func Import(ctx context.Context, db DB, rows []research.SheetRow, opts Options) (Report, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	rep := Report{Rows: len(rows)}
	creators := map[string]bool{}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		err := importRow(ctx, db, row, opts, &rep, creators)
		if errors.Is(err, oembed.ErrRateLimited) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			rep.Failed = append(rep.Failed, RowError{Line: row.Line, URL: row.Post.URL, Err: err})
			return rep, err
		}
		if err != nil {
			rep.Failed = append(rep.Failed, RowError{Line: row.Line, URL: row.Post.URL, Err: err})
			continue
		}
		rep.Imported++
	}
	return rep, nil
}

func importRow(ctx context.Context, db DB, row research.SheetRow, opts Options, rep *Report, creators map[string]bool) error {
	if row.Observer == OEmbedAnnotator {
		return fmt.Errorf("observer %q is reserved for the caption statistics", OEmbedAnnotator)
	}
	// The creator first: it checks the tombstones, so a forgotten creator is refused before TikTok is asked anything.
	creator, err := db.GetResearchCreator(ctx, row.Post.Platform, row.Handle)
	if errors.Is(err, store.ErrNotFound) {
		creator, err = db.UpsertResearchCreator(ctx, store.NewResearchCreator{Platform: row.Post.Platform, Handle: row.Handle})
		if err == nil {
			key := string(row.Post.Platform) + ":" + row.Handle
			if !creators[key] {
				creators[key] = true
				rep.NewCreators++
			}
		}
	}
	if err != nil {
		return err
	}
	if creator.Status == store.ResearchDropped {
		return fmt.Errorf("@%s was dropped from the research; approve them again to add posts", creator.Handle)
	}

	var resolved *oembed.Result
	if row.Post.Platform == research.TikTok && opts.Resolver != nil {
		r, err := opts.Resolver.Resolve(ctx, row.Post.URL)
		if err != nil {
			return err
		}
		resolved = &r
		rep.Resolved++
	}

	post := store.NewResearchPost{URL: row.Post.URL, CreatorID: creator.ID, DurationS: row.DurationS}
	if row.Post.Platform == research.TikTok {
		t, err := research.TikTokPostTime(row.Post.ExternalID)
		if err != nil {
			return err
		}
		post.PostedAt, post.PostedAtSource = &t, "id"
		if row.PostedAt != nil {
			rep.TimeChecks++
			if research.ShownDateMatches(t, *row.PostedAt, row.PostedDateOnly) {
				rep.TimeMatches++
			} else {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("line %d: the post id says it was posted %s UTC, the sheet says %s; the id's time is kept",
					row.Line, t.Format("2006-01-02 15:04"), shown(*row.PostedAt, row.PostedDateOnly)))
			}
		}
	} else if row.PostedAt != nil {
		post.PostedAt, post.PostedAtSource = row.PostedAt, "manual"
	}

	watched := row.WatchedAt
	if watched.IsZero() {
		watched = opts.Now()
	}
	var added, dups, notes int
	err = db.InTx(ctx, func(tx Writer) error {
		added, dups, notes = 0, 0, 0
		p, err := tx.UpsertResearchPost(ctx, post)
		if err != nil {
			return err
		}
		stored, err := tx.ListResearchEvidenceForPost(ctx, p.ID)
		if err != nil {
			return err
		}
		have := map[string]bool{}
		for _, e := range stored {
			if e.RawRef != nil {
				have[e.Source+":"+*e.RawRef] = true
			}
		}
		observe := func(in store.NewResearchEvidence) error {
			if have[string(in.Source)+":"+in.RawRef] {
				dups++
				return nil
			}
			in.PostID, in.Retention = p.ID, opts.Retention
			if _, err := tx.AddResearchEvidence(ctx, in); err != nil {
				return err
			}
			added++
			return nil
		}
		if err := observe(store.NewResearchEvidence{
			Source: research.SourceWatchSheet, Method: research.MethodManual, ObservedAt: watched, Observer: row.Observer,
			Metrics: row.Metrics, RawRef: row.RawRef,
		}); err != nil {
			return err
		}
		if row.HasAnnotation() {
			if _, err := tx.SaveResearchAnnotation(ctx, p.ID, row.Annotation); err != nil {
				return err
			}
			notes++
		}
		if resolved == nil {
			return nil
		}
		if err := observe(store.NewResearchEvidence{
			Source: research.SourceOEmbed, Method: research.MethodAPI, ObservedAt: opts.Now(), Observer: OEmbedAnnotator, RawRef: resolved.RawRef,
		}); err != nil {
			return err
		}
		// Our own derived numbers about the caption: its length and its hashtags. The caption itself is not stored.
		captionLen, tags := CaptionStats(resolved.Title)
		if _, err := tx.SaveResearchAnnotation(ctx, p.ID, research.Annotation{
			CaptionLen: &captionLen, HashtagsN: &tags, Annotator: OEmbedAnnotator, Confidence: 1,
		}); err != nil {
			return err
		}
		notes++
		return nil
	})
	if err != nil {
		return err
	}
	rep.Observations += added
	rep.Duplicates += dups
	rep.Annotations += notes
	return nil
}

func shown(t time.Time, dateOnly bool) string {
	if dateOnly {
		return t.Format(time.DateOnly)
	}
	return t.Format(time.RFC3339)
}

var hashtagRe = regexp.MustCompile(`#[\p{L}\p{N}_]+`)

// CaptionStats returns a caption's length in characters and its number of hashtags.
func CaptionStats(caption string) (length, hashtags int) {
	return utf8.RuneCountInString(caption), len(hashtagRe.FindAllString(caption, -1))
}
