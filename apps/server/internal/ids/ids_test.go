package ids

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestPrefixesAreValidTypeIDPrefixes(t *testing.T) {
	re := regexp.MustCompile(`^[a-z]([a-z_]{0,61}[a-z])?$`)
	seen := map[string]bool{}
	for _, p := range Prefixes {
		if !re.MatchString(p) {
			t.Errorf("prefix %q is not a valid TypeID prefix", p)
		}
		if seen[p] {
			t.Errorf("prefix %q is listed twice", p)
		}
		seen[p] = true
	}
	if len(Prefixes) != 17 {
		t.Errorf("D-24 lists 17 prefixes, got %d", len(Prefixes))
	}
}

func TestRoundTrip(t *testing.T) {
	for _, p := range Prefixes {
		u, err := NewUUID()
		if err != nil {
			t.Fatal(err)
		}
		s := Encode(p, u)
		if !strings.HasPrefix(s, p+"_") || len(s) != len(p)+1+26 {
			t.Fatalf("Encode(%q) = %q", p, s)
		}
		got, err := Decode(p, s)
		if err != nil || got != u {
			t.Fatalf("Decode(%q, %q) = %v, %v; want %v", p, s, got, err, u)
		}
	}
}

// A vector from the TypeID spec (valid.yml), so the conversion is checked against the standard and not only
// against itself.
func TestSpecVector(t *testing.T) {
	const (
		typeID = "prefix_01h455vb4pex5vsknk084sn02q"
		uuidS  = "01890a5d-ac96-774b-bcce-b302099a8057"
	)
	u := uuid.MustParse(uuidS)
	if got := Encode("prefix", u); got != typeID {
		t.Fatalf("Encode = %q, want %q", got, typeID)
	}
	got, err := Decode("prefix", typeID)
	if err != nil || got != u {
		t.Fatalf("Decode = %v, %v", got, err)
	}
}

func TestDecodeRejects(t *testing.T) {
	u, _ := NewUUID()
	good := Encode(PrefixWorkspace, u)
	cases := map[string]string{
		"wrong prefix":   Encode(PrefixUser, u),
		"empty":          "",
		"no prefix":      good[len("wks_"):],
		"nil id":         "wks_00000000000000000000000000",
		"not v7":         Encode(PrefixWorkspace, uuid.New()),
		"upper case":     strings.ToUpper(good),
		"short suffix":   good[:len(good)-1],
		"bad character":  good[:len(good)-1] + "u",
		"overflow":       "wks_8" + good[len("wks_")+1:],
		"uuid text":      u.String(),
		"double prefix":  "wks_wks_" + good[len("wks_"):],
		"trailing space": good + " ",
	}
	for name, in := range cases {
		if _, err := Decode(PrefixWorkspace, in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: Decode(%q) err = %v, want ErrInvalid", name, in, err)
		}
	}
}

func TestTypedIDs(t *testing.T) {
	w, err := NewWorkspaceID()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(w.String(), "wks_") || w.IsZero() {
		t.Fatalf("workspace id = %q", w)
	}
	got, err := ParseWorkspaceID(w.String())
	if err != nil || got != w {
		t.Fatalf("ParseWorkspaceID = %v, %v", got, err)
	}
	// A user TypeID is not a workspace ID.
	u, _ := NewUserID()
	if _, err := ParseWorkspaceID(u.String()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a usr_ id parsed as a workspace id: %v", err)
	}
	if _, err := ParseUserID(u.String()); err != nil {
		t.Fatal(err)
	}
	if !(WorkspaceID{}).IsZero() {
		t.Fatal("zero value should be zero")
	}
}

func TestJSONAndSQLForms(t *testing.T) {
	type doc struct {
		ID WorkspaceID `json:"id"`
	}
	w, _ := NewWorkspaceID()
	b, err := json.Marshal(doc{w})
	if err != nil || string(b) != `{"id":"`+w.String()+`"}` {
		t.Fatalf("json = %s, %v", b, err)
	}
	var back doc
	if err := json.Unmarshal(b, &back); err != nil || back.ID != w {
		t.Fatalf("unmarshal = %+v, %v", back, err)
	}
	u, _ := NewUserID()
	if err := json.Unmarshal([]byte(`{"id":"`+u.String()+`"}`), &back); err == nil {
		t.Fatal("a usr_ id was accepted for a workspace field")
	}

	// Postgres sees the plain UUID, never the TypeID.
	v, err := w.Value()
	if err != nil || v != w.UUID.String() {
		t.Fatalf("Value = %v, %v", v, err)
	}
	var scanned WorkspaceID
	if err := scanned.Scan(w.UUID.String()); err != nil || scanned != w {
		t.Fatalf("Scan = %v, %v", scanned, err)
	}
}

func TestNewUUIDIsV7AndOrdered(t *testing.T) {
	a, _ := NewUUID()
	b, _ := NewUUID()
	if a.Version() != 7 || b.Version() != 7 {
		t.Fatalf("versions %d %d", a.Version(), b.Version())
	}
	if strings.Compare(a.String(), b.String()) >= 0 {
		t.Fatalf("UUIDv7 values should sort by creation time: %s >= %s", a, b)
	}
}
