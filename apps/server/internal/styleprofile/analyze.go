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
	// TempDir is where the sandbox writes ("" for the system's temporary directory); it must be inside the sandbox's allowed roots.
	TempDir string
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
	m, err := MeasureVideoIn(ctx, a.Runner, a.TempDir, path)
	if err != nil {
		return Analysis{}, err
	}
	if m.Audio, err = MeasureAudioIn(ctx, a.Runner, a.TempDir, path, m.CutsS); err != nil {
		return Analysis{}, err
	}
	m.Movement = measureMovement(ctx, a.Runner, a.TempDir, path, m.DurationS, m.CutsS)
	out := Analysis{Measures: m}
	if a.Reader != nil {
		res, verr := a.Reader.ReadWith(ctx, ws, uuid.New(), path, m.DurationS, m.CutsS)
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
	if p.Version < MinProfileVersion || p.Version > ProfileVersion {
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
	p, row, err := loadRow(ctx, st, ws)
	if p == nil || err != nil {
		return nil, nil, err
	}
	var ms []Measures
	if err := json.Unmarshal(row.Measures, &ms); err != nil {
		return nil, nil, fmt.Errorf("styleprofile: the stored measures: %w", err)
	}
	return p, ms, nil
}

// LoadProfile returns only the profile (the editor reads it on every tool call and has no use for the measures), or nil when the
// workspace has none or the stored shape is one this version does not know.
func LoadProfile(ctx context.Context, st StyleReader, ws ids.WorkspaceID) (*Profile, error) {
	p, _, err := loadRow(ctx, st, ws)
	return p, err
}

func loadRow(ctx context.Context, st StyleReader, ws ids.WorkspaceID) (*Profile, store.CreatorStyle, error) {
	row, err := st.GetCreatorStyle(ctx, ws)
	if errors.Is(err, store.ErrNotFound) {
		return nil, row, nil
	}
	if err != nil {
		return nil, row, err
	}
	p, err := ParseProfile(row.Profile)
	if errors.Is(err, ErrUnsupportedVersion) {
		return nil, row, nil
	}
	return p, row, err
}

// Save aggregates the measures into a profile and stores both, replacing the workspace's profile (without a summary).
func Save(ctx context.Context, st StyleStore, ws ids.WorkspaceID, ms []Measures) (*Profile, error) {
	return SaveWith(ctx, st, ws, ms, SaveOptions{})
}

// SaveOptions are what a profile is stored with besides its measures.
type SaveOptions struct {
	// Summary is the AI summary (nil for none); ReferenceIDs the style_references the measures come from.
	Summary      *Summary
	ReferenceIDs []uuid.UUID
}

// SaveWith is Save with a summary and the ids of the references.
func SaveWith(ctx context.Context, st StyleStore, ws ids.WorkspaceID, ms []Measures, opts SaveOptions) (*Profile, error) {
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
	var sj json.RawMessage
	if opts.Summary != nil {
		if sj, err = json.Marshal(opts.Summary); err != nil {
			return nil, err
		}
	}
	if _, err := st.SaveCreatorStyle(ctx, ws, store.NewCreatorStyle{Version: ProfileVersion, Profile: pj, Measures: mj, ReferencesCount: len(ms), Summary: sj, ReferenceIDs: opts.ReferenceIDs}); err != nil {
		return nil, err
	}
	return p, nil
}

// SettingsReader reads the creator's settings. *store.Store is the real one.
type SettingsReader interface {
	GetCreatorStyleSettings(ctx context.Context, ws ids.WorkspaceID) (store.CreatorStyleSettings, error)
}

// LoadStyle reads everything of the workspace's style: the profile and its summary, and the creator's settings (when st is also a
// SettingsReader), resolved without an edit's vibe. The Style may be Empty; it is nil only with an error. Settings that this version
// cannot read are ignored, like a profile of an unknown shape.
func LoadStyle(ctx context.Context, st StyleReader, ws ids.WorkspaceID) (*Style, error) {
	p, row, err := loadRow(ctx, st, ws)
	if err != nil {
		return nil, err
	}
	out := &Style{Profile: p}
	if p != nil {
		if out.Summary, err = ParseSummary(row.Summary); err != nil {
			out.Summary = nil // an unreadable summary is no summary; the measures still hold
		}
	}
	if sr, ok := st.(SettingsReader); ok {
		srow, err := sr.GetCreatorStyleSettings(ctx, ws)
		switch {
		case err == nil:
			if mine, perr := ParseSettings(srow.Settings); perr == nil {
				out.Mine = mine
			}
		case !errors.Is(err, store.ErrNotFound):
			return nil, err
		}
	}
	return out.ForEdit(""), nil
}

// MaxReferences is how many references a profile is made from: the most recent ones are kept.
const MaxReferences = 10
