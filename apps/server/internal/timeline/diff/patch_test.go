package diff

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustPatch(t testing.TB, js string) Patch {
	t.Helper()
	p, err := ParsePatch([]byte(js))
	if err != nil {
		t.Fatalf("patch %s: %v", js, err)
	}
	return p
}

func equalDocs(t testing.TB, a, b string) bool {
	t.Helper()
	eq, err := Equal([]byte(a), []byte(b))
	if err != nil {
		t.Fatal(err)
	}
	return eq
}

// The examples of RFC 6902 appendix A.
func TestApplyPatchRFC6902Examples(t *testing.T) {
	cases := []struct {
		name, doc, patch, want string
	}{
		{"A.1 add an object member", `{"foo":"bar"}`, `[{"op":"add","path":"/baz","value":"qux"}]`, `{"baz":"qux","foo":"bar"}`},
		{"A.2 add an array element", `{"foo":["bar","baz"]}`, `[{"op":"add","path":"/foo/1","value":"qux"}]`, `{"foo":["bar","qux","baz"]}`},
		{"A.3 remove an object member", `{"baz":"qux","foo":"bar"}`, `[{"op":"remove","path":"/baz"}]`, `{"foo":"bar"}`},
		{"A.4 remove an array element", `{"foo":["bar","qux","baz"]}`, `[{"op":"remove","path":"/foo/1"}]`, `{"foo":["bar","baz"]}`},
		{"A.5 replace a value", `{"baz":"qux","foo":"bar"}`, `[{"op":"replace","path":"/baz","value":"boo"}]`, `{"baz":"boo","foo":"bar"}`},
		{"A.6 move a value", `{"foo":{"bar":"baz","waldo":"fred"},"qux":{"corge":"grault"}}`,
			`[{"op":"move","from":"/foo/waldo","path":"/qux/thud"}]`, `{"foo":{"bar":"baz"},"qux":{"corge":"grault","thud":"fred"}}`},
		{"A.7 move an array element", `{"foo":["all","grass","cows","eat"]}`, `[{"op":"move","from":"/foo/1","path":"/foo/3"}]`, `{"foo":["all","cows","eat","grass"]}`},
		{"A.8 test a value: success", `{"baz":"qux","foo":["a",2,"c"]}`,
			`[{"op":"test","path":"/baz","value":"qux"},{"op":"test","path":"/foo/1","value":2}]`, `{"baz":"qux","foo":["a",2,"c"]}`},
		{"A.10 add a nested member object", `{"foo":"bar"}`, `[{"op":"add","path":"/child","value":{"grandchild":{}}}]`, `{"foo":"bar","child":{"grandchild":{}}}`},
		{"A.11 ignore unrecognised elements", `{"foo":"bar"}`, `[{"op":"add","path":"/baz","value":"qux","xyz":123}]`, ``},
		{"A.14 ~ escape ordering", `{"/":9,"~1":10}`, `[{"op":"test","path":"/~01","value":10}]`, `{"/":9,"~1":10}`},
		{"A.15 comparing strings and numbers", `{"/":9,"~1":10}`, `[{"op":"test","path":"/~01","value":"10"}]`, ``},
		{"A.16 add an array value", `{"foo":["bar"]}`, `[{"op":"add","path":"/foo/-","value":["abc","def"]}]`, `{"foo":["bar",["abc","def"]]}`},
		{"copy", `{"a":{"b":1}}`, `[{"op":"copy","from":"/a","path":"/c"},{"op":"replace","path":"/c/b","value":2}]`, `{"a":{"b":1},"c":{"b":2}}`},
		{"add null", `{}`, `[{"op":"add","path":"/a","value":null}]`, `{"a":null}`},
		{"replace the whole document", `{"a":1}`, `[{"op":"replace","path":"","value":[1,2]}]`, `[1,2]`},
		{"1 equals 1.0 in test", `{"a":1}`, `[{"op":"test","path":"/a","value":1.0}]`, `{"a":1}`},
		{"numbers keep their text", `{"a":0.30000000000000004}`, `[{"op":"add","path":"/b","value":12345678901234567890}]`, `{"a":0.30000000000000004,"b":12345678901234567890}`},
		{"add at the end of an array by index", `[1]`, `[{"op":"add","path":"/1","value":2}]`, `[1,2]`},
		{"move onto itself", `{"a":[1,2]}`, `[{"op":"move","from":"/a/0","path":"/a/0"}]`, `{"a":[1,2]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			patch, err := ParsePatch([]byte(tc.patch))
			if tc.name == "A.11 ignore unrecognised elements" {
				// Unlike the RFC's example, ParsePatch is strict: a patch with an unknown member is refused.
				if err == nil {
					t.Fatal("accepted a member the RFC does not define")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			out, err := ApplyPatch([]byte(tc.doc), patch)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("expected an error, got %s", out)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !equalDocs(t, string(out), tc.want) {
				t.Fatalf("got %s want %s", out, tc.want)
			}
		})
	}
}

func TestApplyPatchErrors(t *testing.T) {
	cases := map[string][2]string{
		"remove a missing member":      {`{"a":1}`, `[{"op":"remove","path":"/b"}]`},
		"replace a missing member":     {`{"a":1}`, `[{"op":"replace","path":"/b","value":1}]`},
		"add into a missing parent":    {`{"a":1}`, `[{"op":"add","path":"/b/c","value":1}]`},
		"add into a scalar":            {`{"a":1}`, `[{"op":"add","path":"/a/c","value":1}]`},
		"index past the end":           {`[1]`, `[{"op":"add","path":"/2","value":1}]`},
		"index out of range on remove": {`[1]`, `[{"op":"remove","path":"/1"}]`},
		"leading zero index":           {`[1,2]`, `[{"op":"replace","path":"/01","value":1}]`},
		"negative index":               {`[1,2]`, `[{"op":"replace","path":"/-1","value":1}]`},
		"plus index":                   {`[1,2]`, `[{"op":"replace","path":"/+1","value":1}]`},
		"dash on replace":              {`[1,2]`, `[{"op":"replace","path":"/-","value":1}]`},
		"failed test":                  {`{"a":1}`, `[{"op":"test","path":"/a","value":2}]`},
		"test a missing path":          {`{"a":1}`, `[{"op":"test","path":"/b","value":2}]`},
		"move into its own child":      {`{"a":{"b":1}}`, `[{"op":"move","from":"/a","path":"/a/b"}]`},
		"move from a missing path":     {`{"a":1}`, `[{"op":"move","from":"/x","path":"/y"}]`},
		"copy from a missing path":     {`{"a":1}`, `[{"op":"copy","from":"/x","path":"/y"}]`},
		"remove the whole document":    {`{"a":1}`, `[{"op":"remove","path":""}]`},
		"pointer without a slash":      {`{"a":1}`, `[{"op":"remove","path":"a"}]`},
		"the second operation fails":   {`{"a":1}`, `[{"op":"add","path":"/b","value":1},{"op":"remove","path":"/c"}]`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ApplyPatch([]byte(tc[0]), mustPatch(t, tc[1])); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	if _, err := ApplyPatch([]byte(`{`), nil); err == nil {
		t.Fatal("invalid document accepted")
	}
	if _, err := ApplyPatch([]byte(`{} {}`), nil); err == nil {
		t.Fatal("trailing data accepted")
	}
}

func TestApplyPatchDoesNotChangeItsInput(t *testing.T) {
	doc := []byte(`{"a":[1,2,3],"b":{"c":1}}`)
	cp := string(doc)
	patch := mustPatch(t, `[{"op":"remove","path":"/a/0"},{"op":"replace","path":"/b/c","value":9}]`)
	if _, err := ApplyPatch(doc, patch); err != nil {
		t.Fatal(err)
	}
	if string(doc) != cp {
		t.Fatal("the document changed")
	}
	// A failing patch changes nothing either (it works on a copy).
	if _, err := ApplyPatch(doc, mustPatch(t, `[{"op":"remove","path":"/a/0"},{"op":"remove","path":"/zzz"}]`)); err == nil {
		t.Fatal("expected an error")
	}
	if string(doc) != cp {
		t.Fatal("the document changed")
	}
}

func TestOperationEncoding(t *testing.T) {
	p := Patch{
		{Op: OpAdd, Path: "/a", Value: json.RawMessage(`null`)},
		{Op: OpRemove, Path: "/b"},
		{Op: OpMove, From: "/c", Path: "/d"},
		{Op: OpCopy, From: "/c", Path: "/e"},
		{Op: OpReplace, Path: "/f", Value: json.RawMessage(`[1]`)},
		{Op: OpTest, Path: "/g", Value: json.RawMessage(`"x"`)},
	}
	got, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"op":"add","path":"/a","value":null},{"op":"remove","path":"/b"},{"op":"move","from":"/c","path":"/d"},` +
		`{"op":"copy","from":"/c","path":"/e"},{"op":"replace","path":"/f","value":[1]},{"op":"test","path":"/g","value":"x"}]`
	if string(got) != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	back, err := ParsePatch(got)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := json.Marshal(back)
	if string(again) != want {
		t.Fatalf("round trip: %s", again)
	}
	if _, err := json.Marshal(Patch{{Op: OpAdd, Path: "/a"}}); err == nil {
		t.Fatal("an add without a value must not encode")
	}
	if _, err := json.Marshal(Patch{{Op: "twiddle", Path: "/a"}}); err == nil {
		t.Fatal("an unknown op must not encode")
	}
	for _, bad := range []string{`[{"op":"add","path":"/a"}]`, `[{"op":"twiddle","path":"/a"}]`, `{"op":"add"}`, `[] x`, `[{"op":"remove","path":"/a","zzz":1}]`} {
		if _, err := ParsePatch([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestPointer(t *testing.T) {
	if got := Pointer("tracks", "0", "a/b", "c~d"); got != "/tracks/0/a~1b/c~0d" {
		t.Fatal(got)
	}
	if got := Pointer(); got != "" {
		t.Fatal(got)
	}
	toks, err := parsePointer("/a~1b/c~0d/~01")
	if err != nil || strings.Join(toks, "|") != "a/b|c~d|~1" {
		t.Fatalf("%v %v", toks, err)
	}
}

func TestEqual(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{`{"a":1,"b":[1,2]}`, `{"b":[1,2],"a":1.0}`, true},
		{`1e2`, `100`, true},
		{`"a"`, `"a"`, true},
		{`{"a":1}`, `{"a":1,"b":null}`, false},
		{`[1,2]`, `[2,1]`, false},
		{`null`, `0`, false},
		{`{"a":"1"}`, `{"a":1}`, false},
	} {
		got, err := Equal([]byte(tc.a), []byte(tc.b))
		if err != nil || got != tc.want {
			t.Errorf("Equal(%s, %s) = %v, %v", tc.a, tc.b, got, err)
		}
	}
	if _, err := Equal([]byte(`{`), []byte(`{}`)); err == nil {
		t.Fatal("invalid JSON accepted")
	}
}
