package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store/db"
)

// The quick look (P1-04b): a title, a one-line description and tags the first minute after a video is ready. The columns and
// their meaning are in the quick look migration (the one that adds assets.quick_look_status).

// The values of assets.title_source.
const (
	TitleFromFilename  = "filename"
	TitleFromQuickLook = "quicklook"
	TitleFromUser      = "user"
)

// The values of assets.quick_look_status. An asset the quick look does not apply to has none (nil).
const (
	QuickLookPending   = "pending"
	QuickLookSucceeded = "succeeded"
	QuickLookFailed    = "failed"
	QuickLookSkipped   = "skipped"
)

// SetQuickLookHook registers fn to run, in the transaction that marks a video ready (FinishAssetIngest), when the quick look
// is owed for it: the asset is then `pending` and fn enqueues the quick_look job in the same transaction, so the job exists
// exactly when the ready asset does. An error from fn rolls the ingest's write back (the ingest job then runs again). Call it
// during setup, before the Store is used from several goroutines; passing nil removes the hook. Without a hook (the api, the
// tests of other packages) the asset is still marked pending, and `cfctl assets quicklook` can run it.
func (s *Store) SetQuickLookHook(fn func(ctx context.Context, tx *Store, a Asset) error) {
	s.hooks.quickLookDue = fn
}

// quickLookDue marks a just-finished video's quick look as owed, runs the hook, and returns the asset as it is afterwards.
// Not a video, or a quick look that has run already, changes nothing and runs no hook.
func (s *Store) quickLookDue(ctx context.Context, a Asset) (Asset, error) {
	if a.Kind != AssetKindVideo {
		return a, nil
	}
	marked, err := s.q.MarkQuickLookPending(ctx, db.MarkQuickLookPendingParams{WorkspaceID: a.WorkspaceID, ID: a.ID})
	if err != nil {
		if errors.Is(mapErr(err), ErrNotFound) {
			return a, nil
		}
		return a, mapErr(err)
	}
	if s.hooks != nil && s.hooks.quickLookDue != nil {
		if err := s.hooks.quickLookDue(ctx, s, marked); err != nil {
			return a, fmt.Errorf("quick look hook: %w", err)
		}
	}
	return marked, nil
}

// QuickLookResult is what a successful quick look records. The caller has validated it (package quicklook).
type QuickLookResult struct {
	Title       string
	Description string
	Tags        []string
	// Detail is the jsonb detail: the cached frames, the model, the prompt version.
	Detail json.RawMessage
}

// SkipQuickLook marks a pending quick look skipped: its asset failed its ingest, or its job could not run for a day, so
// nothing is owed. ErrNotFound when the asset is not in this workspace or its quick look was not pending.
func (s *Store) SkipQuickLook(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID) (Asset, error) {
	a, err := s.q.SkipQuickLook(ctx, db.SkipQuickLookParams{WorkspaceID: ws, ID: id})
	return a, mapErr(err)
}

// ApplyQuickLook stores the result on a ready asset: the description and tags always, the title only while its source is not
// the user's. It returns the row as it is afterwards. ErrNotFound when the asset is not ready in this workspace any more.
func (s *Store) ApplyQuickLook(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, r QuickLookResult) (Asset, error) {
	tags := r.Tags
	if tags == nil {
		tags = []string{}
	}
	var a Asset
	err := s.inTx(ctx, func(tx *Store) error {
		var err error
		a, err = tx.q.ApplyQuickLook(ctx, db.ApplyQuickLookParams{
			WorkspaceID: ws, ID: id, Title: r.Title, Description: r.Description, Tags: tags, Detail: detailOrEmpty(r.Detail),
		})
		if err != nil {
			return mapErr(err)
		}
		// The tags label the clip analysis's moments too (the analysis may have run before the quick look landed).
		return mapErr(tx.q.RelabelAssetMoments(ctx, db.RelabelAssetMomentsParams{WorkspaceID: ws, AssetID: id, Labels: tags}))
	})
	return a, err
}

// FailQuickLook marks the quick look failed and records the detail (an error code, the frames when they were cut). The
// title, description and tags are left as they were. ErrNotFound when the asset is not ready in this workspace any more.
func (s *Store) FailQuickLook(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, detail json.RawMessage) (Asset, error) {
	a, err := s.q.FailQuickLook(ctx, db.FailQuickLookParams{WorkspaceID: ws, ID: id, Detail: detailOrEmpty(detail)})
	return a, mapErr(err)
}

// UpdateAssetTitle sets the title a person typed and marks it as theirs, so the quick look never overwrites it. The caller
// has cleaned it. ErrNotFound when the asset is not in this workspace or is deleted.
func (s *Store) UpdateAssetTitle(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, title string) (Asset, error) {
	a, err := s.q.UpdateAssetTitle(ctx, db.UpdateAssetTitleParams{WorkspaceID: ws, ID: id, Title: title})
	return a, mapErr(err)
}

// ListAssetsForQuickLook returns up to limit ready videos of the workspace, oldest first: those without a successful quick
// look, or every one with includeDone.
func (s *Store) ListAssetsForQuickLook(ctx context.Context, ws ids.WorkspaceID, includeDone bool, limit int) ([]Asset, error) {
	if limit < 1 || limit > 10000 {
		return nil, fmt.Errorf("%w: limit %d", ErrInvalid, limit)
	}
	rows, err := s.q.ListAssetsForQuickLook(ctx, db.ListAssetsForQuickLookParams{WorkspaceID: ws, IncludeDone: includeDone, RowLimit: int32(limit)}) //nolint:gosec // checked
	return rows, mapErr(err)
}

func detailOrEmpty(d json.RawMessage) json.RawMessage {
	if len(d) == 0 {
		return json.RawMessage(`{}`)
	}
	return d
}

// Limits of a search.
const (
	maxSearchWords     = 8
	maxSearchWordRunes = 40
	// MaxSearchQueryChars is the longest search text a request may carry.
	MaxSearchQueryChars = 100
)

// SearchWords turns what a person typed in the search box into the words of a search: the text in NFC form (so "cafe" typed
// with a combining accent is the same word as the precomposed one), split at every character that is not a letter, a digit or
// a combining mark, lower-cased, at most 8 words of at most 40 characters. Everything else only separates words, which is also
// what makes the result safe to hand to cf_search_query. Each word must then match as a prefix of a stored word or as a stem.
func SearchWords(text string) []string {
	words := strings.FieldsFunc(norm.NFC.String(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsMark(r)
	})
	out := make([]string, 0, min(len(words), maxSearchWords))
	for _, w := range words {
		if r := []rune(w); len(r) > maxSearchWordRunes {
			w = string(r[:maxSearchWordRunes])
		}
		out = append(out, strings.ToLower(w))
		if len(out) == maxSearchWords {
			break
		}
	}
	return out
}

// AssetSubjectsRow is the subject boxes stored with the quick look of one asset (the JSON of the quick look's "subjects" member).
type AssetSubjectsRow = db.ListAssetSubjectsRow

// ListAssetSubjects reads the subject boxes of several assets in one query. Assets without any are not in the result; the caller
// validates what it reads (package subject).
func (s *Store) ListAssetSubjects(ctx context.Context, ws ids.WorkspaceID, assets []ids.AssetID) ([]AssetSubjectsRow, error) {
	if len(assets) == 0 {
		return nil, nil
	}
	rows, err := s.q.ListAssetSubjects(ctx, db.ListAssetSubjectsParams{WorkspaceID: ws, AssetIds: assetUUIDs(assets)})
	return rows, mapErr(err)
}
