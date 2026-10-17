package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store/db"
)

// CreatorStyle is a row of creator_style_profiles: the creator's style profile (P6-05) with the measures of the references it was made
// from. Profile and Measures are JSON documents the styleprofile package reads and writes; the store keeps them as they are.
type CreatorStyle = db.CreatorStyleProfile

// NewCreatorStyle is a profile to store.
type NewCreatorStyle struct {
	// Version is the shape version of Profile (styleprofile.ProfileVersion).
	Version int
	Profile json.RawMessage
	// Measures is a JSON array, one entry per reference.
	Measures        json.RawMessage
	ReferencesCount int
}

// GetCreatorStyle returns the workspace's style profile, or ErrNotFound when it has none.
func (s *Store) GetCreatorStyle(ctx context.Context, ws ids.WorkspaceID) (CreatorStyle, error) {
	r, err := s.q.GetCreatorStyle(ctx, ws)
	return r, mapErr(err)
}

// SaveCreatorStyle stores the workspace's style profile, replacing the one it had.
func (s *Store) SaveCreatorStyle(ctx context.Context, ws ids.WorkspaceID, in NewCreatorStyle) (CreatorStyle, error) {
	if in.Version < 1 || len(in.Profile) == 0 || !json.Valid(in.Profile) {
		return CreatorStyle{}, fmt.Errorf("%w: a style profile needs a version and a JSON profile", ErrInvalid)
	}
	if len(in.Measures) == 0 {
		in.Measures = json.RawMessage(`[]`)
	}
	r, err := s.q.UpsertCreatorStyle(ctx, db.UpsertCreatorStyleParams{
		WorkspaceID: ws, Version: int32(in.Version), Profile: in.Profile, Measures: in.Measures, ReferencesCount: int32(in.ReferencesCount), //nolint:gosec // small
	})
	return r, mapErr(err)
}

// DeleteCreatorStyle removes the workspace's style profile (and so everything derived from the references). It is not an error to
// delete a profile that is not there.
func (s *Store) DeleteCreatorStyle(ctx context.Context, ws ids.WorkspaceID) error {
	return mapErr(s.q.DeleteCreatorStyle(ctx, ws))
}
