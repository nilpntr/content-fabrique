package renderjobs

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// Proof previews (docs/design/proof-edit-v1.md): a preview is a proof when the request says so or, left to the server, when the
// creator turned proof_edits on; it lives next to the plain preview of the same version; only a preview can be one; and "make it final"
// renders the same version with the final profile and no numbers.
func TestProofPreviewsAndMakeItFinal(t *testing.T) {
	f := newFixture(t, "org_proof")
	e, ctx := f.e, context.Background()
	who := e.caller(f.w)

	plain, created, err := e.svc.Request(ctx, who, f.project.ID, f.spec, store.ProfilePreview)
	if err != nil || !created || plain.Proof {
		t.Fatalf("a preview without proof_edits: %+v %v %v", plain, created, err)
	}
	proof, created, err := e.svc.RequestWith(ctx, who, f.project.ID, f.spec, store.ProfilePreview, ProofOn)
	if err != nil || !created || !proof.Proof || proof.ID == plain.ID {
		t.Fatalf("an explicit proof: %+v %v %v", proof, created, err)
	}
	if _, _, err := e.svc.RequestWith(ctx, who, f.project.ID, f.spec, store.ProfileFinal, ProofOn); err == nil || err.Status != 400 {
		t.Errorf("a final proof: %v, want 400", err)
	}

	// The creator turns proofs on: a preview left to the server is the proof, which exists already.
	if _, err := e.st.SaveCreatorStyleSettings(ctx, f.w.ID, json.RawMessage(`{"proof_edits":"on"}`), ids.UserID{}); err != nil {
		t.Fatal(err)
	}
	again, created, err := e.svc.Request(ctx, who, f.project.ID, f.spec, store.ProfilePreview)
	if err != nil || created || again.ID != proof.ID {
		t.Errorf("an automatic proof: %v %v %v, want the existing proof %v", again.ID, created, err, proof.ID)
	}
	if off, _, err := e.svc.RequestWith(ctx, who, f.project.ID, f.spec, store.ProfilePreview, ProofOff); err != nil || off.ID != plain.ID {
		t.Errorf("an explicit plain preview: %v %v", off.ID, err)
	}

	// Make it final: the final render of the proof's version, without numbers; asked again, the same one; of a final, refused.
	fin, created, err := e.svc.MakeFinal(ctx, who, proof.ID)
	if err != nil || !created || fin.Profile != store.ProfileFinal || fin.Proof || fin.TimelineVersion != proof.TimelineVersion {
		t.Fatalf("make it final: %+v %v %v", fin, created, err)
	}
	if again, created, err := e.svc.MakeFinal(ctx, who, plain.ID); err != nil || created || again.ID != fin.ID {
		t.Errorf("make it final again: %v %v %v", again.ID, created, err)
	}
	if _, _, err := e.svc.MakeFinal(ctx, who, fin.ID); err == nil || err.Status != 400 {
		t.Errorf("make a final render final: %v, want 400", err)
	}
	// Another workspace's proof is not found.
	o := e.fixture(t, "org_proof_other")
	if _, _, err := e.svc.MakeFinal(ctx, e.caller(o.w), proof.ID); err == nil || err.Status != 404 {
		t.Errorf("another workspace's proof: %v, want 404", err)
	}
}
