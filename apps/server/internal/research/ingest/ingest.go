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
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/research"
	"github.com/nilpntr/content-fabrique/internal/research/oembed"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// Writer is the part of the store an import writes with.
type Writer interface {
	ResearchCreatorForgotten(ctx context.Context, p research.Platform, handle string) (bool, error)
	GetResearchCreator(ctx context.Context, p research.Platform, handle string) (store.ResearchCreator, error)
	UpsertResearchCreator(ctx context.Context, in store.NewResearchCreator) (store.ResearchCreator, error)
	UpsertResearchPost(ctx context.Context, in store.NewResearchPost) (store.ResearchPost, error)
	ListResearchEvidenceForPost(ctx context.Context, postID uuid.UUID) ([]store.ResearchEvidence, error)
	AddResearchEvidence(ctx context.Context, in store.NewResearchEvidence) (store.ResearchEvidence, error)
	SaveResearchAnnotation(ctx context.Context, postID uuid.UUID, a research.Annotation) (store.ResearchAnnotation, error)
	ListResearchAnnotationsForPost(ctx context.Context, postID uuid.UUID) ([]store.ResearchAnnotation, error)
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
const OEmbedAnnotator = research.ReservedObserver

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
	if strings.EqualFold(row.Observer, OEmbedAnnotator) {
		return fmt.Errorf("observer %q is reserved for the caption statistics", row.Observer)
	}
	// The creator is checked before TikTok is asked anything: a forgotten creator is refused and a dropped one is not sampled. Nothing is
	// written yet, so a row that fails later leaves no creator behind.
	existing, err := db.GetResearchCreator(ctx, row.Post.Platform, row.Handle)
	switch {
	case errors.Is(err, store.ErrNotFound):
		gone, err := db.ResearchCreatorForgotten(ctx, row.Post.Platform, row.Handle)
		if err != nil {
			return err
		}
		if gone {
			return store.ErrForgotten
		}
	case err != nil:
		return err
	case existing.Status == store.ResearchDropped:
		return fmt.Errorf("@%s was dropped from the research; approve them again to add posts", existing.Handle)
	}

	var resolved *oembed.Result
	if row.Post.Platform == research.TikTok && opts.Resolver != nil {
		r, err := opts.Resolver.Resolve(ctx, row.Post.URL)
		if err != nil {
			return err
		}
		resolved = &r
	}

	post := store.NewResearchPost{URL: row.Post.URL, DurationS: row.DurationS}
	switch {
	case row.IDTime != nil:
		post.PostedAt, post.PostedAtSource = row.IDTime, "id"
	case row.Post.Platform == research.TikTok:
		return errors.New("the TikTok post id carries no post time") // ParseWatchSheet refuses such a row; a hand-built row is checked here
	case row.PostedAt != nil:
		post.PostedAt, post.PostedAtSource = row.PostedAt, "manual"
	}

	watched := row.WatchedAt
	if watched.IsZero() {
		watched = opts.Now()
	}
	var added, dups, notes int
	var created bool
	err = db.InTx(ctx, func(tx Writer) error {
		added, dups, notes, created = 0, 0, 0, false
		creator, err := tx.GetResearchCreator(ctx, row.Post.Platform, row.Handle)
		if errors.Is(err, store.ErrNotFound) {
			creator, err = tx.UpsertResearchCreator(ctx, store.NewResearchCreator{Platform: row.Post.Platform, Handle: row.Handle})
			created = err == nil
		}
		if err != nil {
			return err
		}
		post.CreatorID = creator.ID
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
			prev, err := tx.ListResearchAnnotationsForPost(ctx, p.ID)
			if err != nil {
				return err
			}
			if _, err := tx.SaveResearchAnnotation(ctx, p.ID, merge(row.Annotation, prev)); err != nil {
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
	if resolved != nil {
		rep.Resolved++
	}
	if checked, matched, warning := row.TimeCheck(); checked {
		rep.TimeChecks++
		if matched {
			rep.TimeMatches++
		} else {
			rep.Warnings = append(rep.Warnings, warning)
		}
	}
	if key := string(row.Post.Platform) + ":" + row.Handle; created && !creators[key] {
		creators[key] = true
		rep.NewCreators++
	}
	rep.Observations += added
	rep.Duplicates += dups
	rep.Annotations += notes
	return nil
}

// merge fills what a row leaves empty from the annotation the same person stored before: a later watch that only adds the counts or the
// cuts keeps the format, the hook and the note noted earlier. A value in the row always wins.
func merge(a research.Annotation, prev []store.ResearchAnnotation) research.Annotation {
	for _, p := range prev {
		if p.Annotator != a.Annotator {
			continue
		}
		str := func(dst *string, src *string) {
			if *dst == "" && src != nil {
				*dst = *src
			}
		}
		str(&a.FormatID, p.FormatID)
		str(&a.HookKind, p.HookKind)
		str(&a.HookTextPattern, p.HookTextPattern)
		str(&a.TextPattern, p.TextPattern)
		str(&a.CTA, p.Cta)
		str(&a.Topic, p.Topic)
		str(&a.Notes, p.Notes)
		if a.CutsEstimate == nil && p.CutsEstimate != nil {
			n := int(*p.CutsEstimate)
			a.CutsEstimate = &n
		}
		if a.FirstCutS == nil && p.FirstCutS != nil {
			f := *p.FirstCutS
			a.FirstCutS = &f
		}
		if a.CaptionLen == nil && p.CaptionLen != nil {
			n := int(*p.CaptionLen)
			a.CaptionLen = &n
		}
		if a.HashtagsN == nil && p.HashtagsN != nil {
			n := int(*p.HashtagsN)
			a.HashtagsN = &n
		}
	}
	return a
}

var hashtagRe = regexp.MustCompile(`#[\p{L}\p{N}_]+`)

// CaptionStats returns a caption's length in characters and its number of hashtags.
func CaptionStats(caption string) (length, hashtags int) {
	return utf8.RuneCountInString(caption), len(hashtagRe.FindAllString(caption, -1))
}
