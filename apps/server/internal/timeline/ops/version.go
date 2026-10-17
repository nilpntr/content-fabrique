package ops

import (
	"fmt"

	"github.com/nilpntr/content-fabrique/internal/timeline"
	"github.com/nilpntr/content-fabrique/internal/timeline/diff"
)

// AuthorKind says who made a version.
type AuthorKind string

// The two kinds of author.
const (
	AuthorAgent AuthorKind = "agent"
	AuthorUser  AuthorKind = "user"
)

// Author is who made a version: the editor agent (ID names the run) or a user (ID names the user).
type Author struct {
	Kind AuthorKind `json:"author_kind"`
	ID   string     `json:"author_id"`
}

// Version is one immutable state of a timeline. Versions form a chain: each holds the full spec, the ops that made it
// from its parent and the RFC 6902 diff from the parent, so the history can be listed without replaying anything. A
// Version is never changed after it is made; a change is a new Version. (The database table that stores them arrives
// with P3-06.)
type Version struct {
	// Number counts from 1.
	Number int `json:"number"`
	// Parent is the number of the version this one was made from, 0 for the first.
	Parent int           `json:"parent"`
	Spec   timeline.Spec `json:"spec"`
	// Ops is the batch that made this version from its parent; empty for the first version.
	Ops []Op `json:"ops"`
	// Diff is the JSON Patch from the parent's spec to this one's; empty for the first version.
	Diff       diff.Patch `json:"diff"`
	AuthorKind AuthorKind `json:"author_kind"`
	AuthorID   string     `json:"author_id"`
	// Summary has one line per change, from diff.Summarize.
	Summary []string `json:"summary"`
}

// InitialVersion makes version 1 from a spec that did not come from ops (the agent's first draft, or an import). The
// spec is copied.
func InitialVersion(spec timeline.Spec, author Author) (*Version, error) {
	if err := checkAuthor(author); err != nil {
		return nil, err
	}
	cp, err := cloneSpec(spec)
	if err != nil {
		return nil, err
	}
	return &Version{
		Number: 1, Spec: *cp, Ops: []Op{}, Diff: diff.Patch{},
		AuthorKind: author.Kind, AuthorID: author.ID, Summary: []string{"created the timeline"},
	}, nil
}

func checkAuthor(a Author) error {
	switch a.Kind {
	case AuthorAgent, AuthorUser:
		return nil
	}
	return fmt.Errorf("ops: author kind %q must be %q or %q", a.Kind, AuthorAgent, AuthorUser)
}

// NewVersion applies batch to the parent version's spec and, if that works, returns the child version: number
// parent.Number+1, the new spec, the batch, the diff from the parent and its summary.
//
// It returns a nil Version when there is nothing to store: the batch failed (the Result has the errors, and the parent
// stays the latest version), or it changed nothing (the Result has a no_change warning). The parent is not modified.
func NewVersion(parent Version, batch []Op, author Author, catalog timeline.AssetCatalog) (*Version, Result) {
	if err := checkAuthor(author); err != nil {
		return nil, Result{Issues: []Issue{opIssue(NoOp, "", CodeInvalidOp, "", err.Error())}}
	}
	spec, res := Apply(parent.Spec, batch, catalog)
	if !res.OK() {
		return nil, res
	}
	patch, err := diff.Specs(parent.Spec, spec)
	if err != nil {
		res.Issues = append(res.Issues, opIssue(NoOp, "", CodeInvalidOp, "", err.Error()))
		return nil, res
	}
	if len(patch) == 0 {
		res.Issues = append(res.Issues, Issue{
			OpIndex: NoOp, Code: CodeNoChange, Severity: timeline.SeverityWarning,
			Message: "the ops left the timeline as it was, so there is no new version",
		})
		return nil, res
	}
	summary, err := diff.Summarize(parent.Spec, patch)
	if err != nil {
		res.Issues = append(res.Issues, opIssue(NoOp, "", CodeInvalidOp, "", err.Error()))
		return nil, res
	}
	return &Version{
		Number: parent.Number + 1, Parent: parent.Number, Spec: spec, Ops: slicesClone(batch), Diff: patch,
		AuthorKind: author.Kind, AuthorID: author.ID, Summary: summary,
	}, res
}

func slicesClone(batch []Op) []Op {
	out := make([]Op, len(batch))
	copy(out, batch)
	return out
}
