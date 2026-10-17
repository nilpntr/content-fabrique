package timeline

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Code names a kind of problem. The list is closed and documented in the design note (section 3); the editor agent
// sees the codes and the messages.
type Code string

// The issue codes.
const (
	// CodeSchema is a structural problem the JSON Schema found: wrong type, range or enum, unknown field, a missing
	// field, the format/size pairing, track counts, anchor-or-position, the first clip not a cut, the trend_sound
	// export rule. The message says which.
	CodeSchema Code = "schema"
	// CodeDuplicateID is an item or track id used twice anywhere in the spec.
	CodeDuplicateID Code = "duplicate_id"
	// CodeGap is time on the main track that no clip covers, or a first clip that does not start at 0.
	CodeGap Code = "gap"
	// CodeOverlap is a clip that starts before the previous one ends, by more than its transition explains.
	CodeOverlap Code = "overlap"
	// CodeSourceOutOfRange is a clip or audio item reading outside its asset, or with in_ms not below out_ms.
	CodeSourceOutOfRange Code = "source_out_of_range"
	// CodeShotTooShort is a clip whose output duration is under 300 ms.
	CodeShotTooShort Code = "shot_too_short"
	// CodeDurationOutOfRange is a total duration under 1 s or over 180 s.
	CodeDurationOutOfRange Code = "duration_out_of_range"
	// CodeOutsideTimeline is a text item, audio item or marker that is not inside the total duration.
	CodeOutsideTimeline Code = "outside_timeline"
	// CodeUnsafeText is text whose box leaves the safe zone of the format.
	CodeUnsafeText Code = "unsafe_text"
	// CodeUnknownAsset is an asset id the catalog does not know.
	CodeUnknownAsset Code = "unknown_asset"
	// CodeTransitionTooLong is a transition not shorter than both clips it joins.
	CodeTransitionTooLong Code = "transition_too_long"
	// CodeUnlicensedAudio is a licensed_track without a licence record or a creator_audio without an attestation (D-03).
	CodeUnlicensedAudio Code = "unlicensed_audio"
	// CodeFadeTooLong is a fade in plus fade out that is longer than the sound.
	CodeFadeTooLong Code = "fade_too_long"
	// CodeUnknownStyle is a text style the style registry does not know. Until P3-04 adds the real registry it is a
	// warning, not an error: the stub registry may lag behind the styles the agent was told about. P3-04 makes it an
	// error.
	CodeUnknownStyle Code = "unknown_style"
	// CodeWrongAssetKind is a video clip on an audio asset, or a licensed_track or creator_audio item on an asset that
	// is not audio.
	CodeWrongAssetKind Code = "wrong_asset_kind"
	// CodeFirstTransition is a transition_in on the first clip in time order. The schema only checks the first clip of
	// the array; an array that is out of order but contiguous by time can put another clip first.
	CodeFirstTransition Code = "first_transition"
)

// Severity says whether an issue blocks a version.
type Severity string

// The severities. Only errors block a version.
const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// Issue is one problem found in a spec.
type Issue struct {
	Code Code `json:"code"`
	// Path is a JSON Pointer into the spec, such as /tracks/0/items/3/source/out_ms. It is empty for the whole
	// document.
	Path string `json:"path"`
	// Message is written for a model to act on.
	Message  string   `json:"message"`
	Severity Severity `json:"severity"`
}

// Result is what Validate returns.
type Result struct {
	Issues []Issue `json:"issues"`
}

// OK reports whether the spec has no error (warnings do not count).
func (r Result) OK() bool {
	for _, i := range r.Issues {
		if i.Severity == SeverityError {
			return false
		}
	}
	return true
}

// Errors returns the issues that block a version.
func (r Result) Errors() []Issue {
	var out []Issue
	for _, i := range r.Issues {
		if i.Severity == SeverityError {
			out = append(out, i)
		}
	}
	return out
}

// Codes returns the distinct codes of the issues, in order of first appearance.
func (r Result) Codes() []Code {
	var out []Code
	seen := map[Code]bool{}
	for _, i := range r.Issues {
		if !seen[i.Code] {
			seen[i.Code] = true
			out = append(out, i.Code)
		}
	}
	return out
}

// Validate checks a spec in two layers.
//
// Layer 1 is structure: the embedded JSON Schema. Its errors become issues with code "schema", reported once per
// problem (only the matching branch of a tagged union, see schemaIssues). If layer 1 finds anything, layer 2 does
// not run, because the semantic checks assume a well-formed spec.
//
// Layer 2 is semantics: everything the schema cannot express, with one code per kind of problem. See semantic.go.
//
// assets is where asset durations, kinds and rights come from; nil means the catalog is empty. Validate never
// panics, whatever the bytes.
func Validate(spec []byte, assets AssetCatalog) Result {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(spec))
	if err != nil {
		return Result{Issues: []Issue{{
			Code: CodeSchema, Severity: SeverityError,
			Message: fmt.Sprintf("the spec is not valid JSON: %v", err),
		}}}
	}
	if issues := validateStructure(doc); len(issues) > 0 {
		return Result{Issues: issues}
	}
	// The document is decoded twice, once into generic values for the schema and once into the Go types: the schema
	// library validates generic values, and the typed decode is what the semantic layer reads. The shape check runs on
	// the generic document, so nothing is decoded a third time.
	parsed, err := parseDoc(doc, spec)
	if err != nil {
		issue := Issue{Code: CodeSchema, Severity: SeverityError, Message: err.Error()}
		var de *DecodeError
		if errors.As(err, &de) {
			issue.Path, issue.Message = de.Path, de.Message
		}
		return Result{Issues: []Issue{issue}}
	}
	if assets == nil {
		assets = MemoryCatalog(nil)
	}
	return Result{Issues: checkSemantics(parsed, assets)}
}
