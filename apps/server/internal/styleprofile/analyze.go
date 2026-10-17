package styleprofile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// Analyzer analyses reference videos: the picture and the sound in the sandbox, the title text with one small vision call.
type Analyzer struct {
	Runner media.Runner
	// Reader makes the text read; nil skips it (the measures then have no Text).
	Reader *Reader
}

// Analysis is the result for one reference.
type Analysis struct {
	Measures Measures
	// Vision is what the text read did and cost; nil when none was made.
	Vision *VisionResult
	// VisionErr is a failure of the text read that does not stop the analysis (a busy API): the measures are kept without a text.
	VisionErr error
}

// AnalyzeFile measures one reference video for a workspace. A failure of the picture or the sound is an error; a failure of the text
// read is not (see Analysis.VisionErr).
func (a *Analyzer) AnalyzeFile(ctx context.Context, ws ids.WorkspaceID, path string) (Analysis, error) {
	if a.Runner == nil {
		return Analysis{}, errors.New("styleprofile: no runner")
	}
	m, err := MeasureVideo(ctx, a.Runner, path)
	if err != nil {
		return Analysis{}, err
	}
	if m.Audio, err = MeasureAudio(ctx, a.Runner, path, m.CutsS); err != nil {
		return Analysis{}, err
	}
	out := Analysis{Measures: m}
	if a.Reader != nil {
		res, verr := a.Reader.Read(ctx, ws, uuid.New(), path, m.DurationS)
		out.Vision = &res
		out.VisionErr = verr
		out.Measures.Text = res.Text
	}
	return out, nil
}

// ErrUnsupportedVersion is returned for a stored profile of a shape this code does not know.
var ErrUnsupportedVersion = errors.New("styleprofile: the stored profile has a shape this version does not know")

// ParseProfile decodes a stored profile and checks its version.
func ParseProfile(raw json.RawMessage) (*Profile, error) {
	var p Profile
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("styleprofile: %w", err)
	}
	if p.Version != ProfileVersion {
		return nil, fmt.Errorf("%w (version %d)", ErrUnsupportedVersion, p.Version)
	}
	return &p, nil
}

// StyleStore is what saving and loading a profile needs. *store.Store is the real one.
type StyleStore interface {
	GetCreatorStyle(ctx context.Context, ws ids.WorkspaceID) (store.CreatorStyle, error)
	SaveCreatorStyle(ctx context.Context, ws ids.WorkspaceID, in store.NewCreatorStyle) (store.CreatorStyle, error)
}

// StyleReader is what loading a profile needs. *store.Store is the real one.
type StyleReader interface {
	GetCreatorStyle(ctx context.Context, ws ids.WorkspaceID) (store.CreatorStyle, error)
}

// Load reads the workspace's profile and the measures it was made from. It returns nil (and no error) when the workspace has none, or
// when the stored one is of a shape this version does not know (the editor then works without a style, as it did before).
func Load(ctx context.Context, st StyleReader, ws ids.WorkspaceID) (*Profile, []Measures, error) {
	row, err := st.GetCreatorStyle(ctx, ws)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	p, err := ParseProfile(row.Profile)
	if errors.Is(err, ErrUnsupportedVersion) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var ms []Measures
	if err := json.Unmarshal(row.Measures, &ms); err != nil {
		return nil, nil, fmt.Errorf("styleprofile: the stored measures: %w", err)
	}
	return p, ms, nil
}

// Save aggregates the measures into a profile and stores both, replacing the workspace's profile.
func Save(ctx context.Context, st StyleStore, ws ids.WorkspaceID, ms []Measures) (*Profile, error) {
	p := Aggregate(ms)
	if p == nil {
		return nil, errors.New("styleprofile: a profile needs at least one reference")
	}
	pj, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	mj, err := json.Marshal(ms)
	if err != nil {
		return nil, err
	}
	if _, err := st.SaveCreatorStyle(ctx, ws, store.NewCreatorStyle{Version: ProfileVersion, Profile: pj, Measures: mj, ReferencesCount: len(ms)}); err != nil {
		return nil, err
	}
	return p, nil
}

// MaxReferences is how many references a profile is made from: the most recent ones are kept.
const MaxReferences = 10
