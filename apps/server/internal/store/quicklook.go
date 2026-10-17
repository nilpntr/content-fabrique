package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store/db"
)

// The quick look (P1-04b): a title, a one-line description and tags the first minute after a video is ready. The columns and
// their meaning are in migration 00011.

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

// ApplyQuickLook stores the result on a ready asset: the description and tags always, the title only while its source is not
// the user's. It returns the row as it is afterwards. ErrNotFound when the asset is not ready in this workspace any more.
func (s *Store) ApplyQuickLook(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, r QuickLookResult) (Asset, error) {
	tags := r.Tags
	if tags == nil {
		tags = []string{}
	}
	a, err := s.q.ApplyQuickLook(ctx, db.ApplyQuickLookParams{
		WorkspaceID: ws, ID: id, Title: r.Title, Description: r.Description, Tags: tags, Detail: detailOrEmpty(r.Detail),
	})
	return a, mapErr(err)
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

// SearchQuery turns what a person typed in the search box into a tsquery for ListAssets: every word (letters and digits, in
// any script) must match, as a prefix, so "ro" finds "row". Everything else in the text is ignored, which is also what makes
// it safe to hand to to_tsquery: the output holds only quoted words, ":*" and "&". It returns "" when no word is left.
func SearchQuery(text string) string {
	words := strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	var terms []string
	for _, w := range words {
		if len([]rune(w)) > maxSearchWordRunes {
			w = string([]rune(w)[:maxSearchWordRunes])
		}
		terms = append(terms, "'"+strings.ToLower(w)+"':*")
		if len(terms) == maxSearchWords {
			break
		}
	}
	return strings.Join(terms, " & ")
}
