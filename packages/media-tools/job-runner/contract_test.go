package main

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

// testdata/manifest.json and status.json are written by the server's remote provider (apps/server/internal/sandbox,
// TestRemoteWireMatchesRunnerContract). The runner must parse the manifest, and its own Status must encode the status the
// provider reads, field for field.
func TestContractTestdata(t *testing.T) {
	data, err := os.ReadFile("testdata/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	m, err := parseManifest(data)
	if err != nil {
		t.Fatalf("the provider's manifest does not parse: %v", err)
	}
	if len(m.Steps) != 2 || m.Steps[1].TimeoutMs != 60000 || m.Outputs[0].MaxBytes != 1<<30 || m.Limits.TimeoutMs != 1800000 {
		t.Errorf("manifest %+v", m)
	}
	data, err = os.ReadFile("testdata/status.json")
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var st Status
	if err := dec.Decode(&st); err != nil {
		t.Fatalf("the provider's status has fields the runner does not write: %v", err)
	}
	again, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(again), bytes.TrimSpace(data)) {
		t.Errorf("the runner writes the status differently:\n%s\nthe provider reads:\n%s", again, data)
	}
}
