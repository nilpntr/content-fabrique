package styleprofile

import (
	"slices"
	"testing"
)

// Proof edits are the creator's own choice only, and off for everyone else (docs/design/proof-edit-v1.md).
func TestProofEditsAreOptIn(t *testing.T) {
	if r := Resolve(nil, &Settings{ProofEdits: LevelOn}, Settings{}, ""); r.ProofsOn() || r.Sources.ProofEdits != SourceDefault {
		t.Errorf("a suggestion turned proofs on: %+v", r)
	}
	on := Resolve(nil, nil, Settings{ProofEdits: LevelOn}, VibeHype)
	if !on.ProofsOn() || on.Sources.ProofEdits != SourceYou {
		t.Errorf("the creator's proofs: %q from %q", on.ProofEdits, on.Sources.ProofEdits)
	}
	if _, err := (Settings{ProofEdits: "sometimes"}).Normalize(); err == nil {
		t.Error("proof_edits takes off or on")
	}
	got, err := ParseSettings([]byte(`{"proof_edits":"on"}`))
	if err != nil || got.ProofEdits != LevelOn {
		t.Errorf("parse: %+v, %v", got, err)
	}
	if !slices.Contains(SettingNames(), "proof_edits") || !slices.Equal(SettingValues("proof_edits"), []string{LevelOff, LevelOn}) {
		t.Error("proof_edits is not a setting")
	}
}
