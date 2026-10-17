package timeline

import (
	"path/filepath"
	"testing"
)

// FuzzValidate checks that Validate never panics, whatever the bytes, and that a result without errors means the spec
// decodes. `go test` runs the seed corpus (every fixture, plus truncations and mutations of them) as ordinary
// tests; to explore further run `go test -fuzz FuzzValidate ./internal/timeline`.
func FuzzValidate(f *testing.F) {
	for _, sub := range []string{"valid", "invalid"} {
		files, _ := filepath.Glob(filepath.Join(fixturesDir, sub, "*.json"))
		for _, file := range files {
			b := readFile(f, file)
			f.Add(b)
			f.Add(b[:len(b)/2])                      // truncated
			f.Add(append([]byte{}, b[len(b)/3:]...)) // headless
		}
	}
	for _, seed := range []string{"", "null", "[]", "{}", `{"tracks":[{}]}`, `{"tracks":[null]}`, "\x00", `{"schema":1e999}`} {
		f.Add([]byte(seed))
	}
	cat := MemoryCatalog{
		"ast_01j3er3kwcgrasdgd4aznmxxe0": {DurationMs: 60000, Kind: AssetVideo, Rights: RightsAttested},
		"ast_01j0h9rew727hk2pt0v5b31h44": {DurationMs: 90000, Kind: AssetAudio, Rights: RightsLicensed},
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		res := Validate(data, cat)
		for _, i := range res.Issues {
			if i.Code == "" || i.Message == "" || i.Severity == "" {
				t.Fatalf("incomplete issue %+v", i)
			}
		}
		if res.OK() {
			if _, err := Parse(data); err != nil {
				t.Fatalf("accepted a spec that does not decode: %v", err)
			}
		}
	})
}
