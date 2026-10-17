package ops

import "github.com/nilpntr/content-fabrique/internal/timeline"

// The codes the engine adds to the timeline's own (timeline.Code). The editor agent sees them with the messages, which
// are written to be acted on.
const (
	// CodeInvalidOp is an op that does not match the op schema: a missing or unknown field, a value out of range, a
	// wrong type. Field says where in the op.
	CodeInvalidOp timeline.Code = "invalid_op"
	// CodeUnknownOp is an "op" value the engine does not know.
	CodeUnknownOp timeline.Code = "unknown_op"
	// CodeBatchSize is an empty batch or one of more than MaxBatchOps ops.
	CodeBatchSize timeline.Code = "batch_size"
	// CodeUnknownItem is an id that names nothing in the timeline.
	CodeUnknownItem timeline.Code = "unknown_item"
	// CodeWrongItemKind is an id that names an item of another kind than the op works on (a text item given to
	// remove_item, say).
	CodeWrongItemKind timeline.Code = "wrong_item_kind"
	// CodeIDTaken is an id given to a new item that is already in use.
	CodeIDTaken timeline.Code = "id_taken"
	// CodeBadPosition is an index or after_id that does not name a place on the main track.
	CodeBadPosition timeline.Code = "bad_position"
	// CodeBadArgument is a combination of fields that is wrong for the item (trim_item with duration_ms on a clip), or
	// a beat grid the beat utilities refuse.
	CodeBadArgument timeline.Code = "bad_argument"
	// CodeCutNotSnapped is a warning: snap_cuts_to_beats left a cut where it was.
	CodeCutNotSnapped timeline.Code = "cut_not_snapped"
	// CodeNoCuts is a warning: snap_cuts_to_beats found no cut to snap.
	CodeNoCuts timeline.Code = "no_cuts"
	// CodeNoChange is a warning: the batch left the timeline as it was, so there is no new version.
	CodeNoChange timeline.Code = "no_change"
)

// NoOp marks an issue that no op in particular caused (Issue.OpIndex).
const NoOp = -1

// Issue is one problem with a batch.
type Issue struct {
	// OpIndex is the index in the batch of the op that caused the issue, or NoOp (-1) when it is not known.
	OpIndex int `json:"op_index"`
	// Op is the name of that op, empty when OpIndex is NoOp.
	Op   string        `json:"op,omitempty"`
	Code timeline.Code `json:"code"`
	// Field is a JSON Pointer into the op, for problems with the op itself (CodeInvalidOp and the like).
	Field string `json:"field,omitempty"`
	// Path is a JSON Pointer into the resulting spec, for problems that timeline.Validate found.
	Path string `json:"path,omitempty"`
	// Message is written for a model to act on.
	Message  string            `json:"message"`
	Severity timeline.Severity `json:"severity"`
}

// Created is an item that an add_* or set_sound op made, with the id it got.
type Created struct {
	OpIndex int `json:"op_index"`
	// Kind is "clip", "text" or "sound".
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// Result is what Apply returns next to the spec.
type Result struct {
	Issues []Issue `json:"issues"`
	// Created lists the items the batch added, so the agent knows their ids. Empty when the batch failed.
	Created []Created `json:"created,omitempty"`
}

// OK reports whether the batch was applied: no issue is an error (warnings do not count).
func (r Result) OK() bool {
	for _, i := range r.Issues {
		if i.Severity == timeline.SeverityError {
			return false
		}
	}
	return true
}

// Errors returns the issues that stopped the batch.
func (r Result) Errors() []Issue {
	var out []Issue
	for _, i := range r.Issues {
		if i.Severity == timeline.SeverityError {
			out = append(out, i)
		}
	}
	return out
}

// Codes returns the distinct codes of the issues, in order of first appearance.
func (r Result) Codes() []timeline.Code {
	var out []timeline.Code
	seen := map[timeline.Code]bool{}
	for _, i := range r.Issues {
		if !seen[i.Code] {
			seen[i.Code] = true
			out = append(out, i.Code)
		}
	}
	return out
}
