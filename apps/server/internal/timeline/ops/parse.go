package ops

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// ParseOp decodes one op strictly. The op is first checked against its schema (so an unknown field, a key written in
// another case, a value out of range or a missing field is an issue naming the field), then decoded into its Go type
// with unknown fields disallowed. idx is the op's index in its batch, for the issues.
func ParseOp(data []byte, idx int) (Op, []Issue) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, []Issue{opIssue(idx, "", CodeInvalidOp, "", fmt.Sprintf("the op is not valid JSON: %v", err))}
	}
	return parseOpDoc(doc, data, idx)
}

func parseOpDoc(doc any, data []byte, idx int) (Op, []Issue) {
	if issues := checkOpDoc(doc, idx); len(issues) > 0 {
		return nil, issues
	}
	// checkOpDoc has made sure doc is an object with a known "op".
	obj, _ := doc.(map[string]any)
	name, _ := obj["op"].(string)
	op := newOp(name)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, []Issue{opIssue(idx, name, CodeInvalidOp, "", err.Error())}
	}
	delete(fields, "op")
	body, err := json.Marshal(fields)
	if err != nil {
		return nil, []Issue{opIssue(idx, name, CodeInvalidOp, "", err.Error())}
	}
	if err := decodeStrict(body, op); err != nil {
		return nil, []Issue{opIssue(idx, name, CodeInvalidOp, "", fmt.Sprintf("cannot decode the op: %v", err))}
	}
	return op, nil
}

// ParseBatch decodes a JSON array of ops. If any op is invalid it returns no ops and the issues of every invalid op;
// an empty batch, or one of more than MaxBatchOps ops, is an issue too.
func ParseBatch(data []byte) ([]Op, Result) {
	var raws []json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&raws); err != nil {
		return nil, Result{Issues: []Issue{opIssue(NoOp, "", CodeInvalidOp, "", fmt.Sprintf("a batch must be a JSON array of ops: %v", err))}}
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, Result{Issues: []Issue{opIssue(NoOp, "", CodeInvalidOp, "", "unexpected data after the batch")}}
	}
	if len(raws) == 0 || len(raws) > MaxBatchOps {
		return nil, Result{Issues: []Issue{opIssue(NoOp, "", CodeBatchSize, "", fmt.Sprintf("a batch holds 1 to %d ops (got %d)", MaxBatchOps, len(raws)))}}
	}
	batch := make([]Op, 0, len(raws))
	var res Result
	for i, raw := range raws {
		op, issues := ParseOp(raw, i)
		res.Issues = append(res.Issues, issues...)
		batch = append(batch, op)
	}
	if !res.OK() {
		return nil, res
	}
	return batch, res
}

func decodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("unexpected data after the op")
	}
	return nil
}

// cloneSpec deep-copies a spec by encoding it and decoding it again. The decoder is the spec's own strict one; the spec
// was just encoded from Go types, so the schema and key-case checks of timeline.Parse would find nothing.
func cloneSpec(spec timeline.Spec) (*timeline.Spec, error) {
	data, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("cannot encode the spec: %w", err)
	}
	var out timeline.Spec
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("cannot decode the spec: %w", err)
	}
	return &out, nil
}
