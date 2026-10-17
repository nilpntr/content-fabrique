package assets

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

func TestPartPlan(t *testing.T) {
	for _, size := range []int64{1, 1 << 20, DefaultPartSize - 1, DefaultPartSize, DefaultPartSize + 1, 3 << 30, MaxFileSize} {
		ps, n := PartPlan(size)
		if ps < MinPartSize || ps > storage.MaxPartSize {
			t.Errorf("size %d: part size %d is outside [%d, %d]", size, ps, MinPartSize, storage.MaxPartSize)
		}
		if n < 1 || n > storage.MaxParts {
			t.Errorf("size %d: %d parts", size, n)
		}
		// The parts add up to the file exactly, and the last one is neither empty nor longer than the others.
		var sum int64
		for i := 1; i <= n; i++ {
			p := PartSizeOf(size, ps, n, i)
			if p < 1 || p > ps {
				t.Fatalf("size %d: part %d is %d bytes (part size %d)", size, i, p, ps)
			}
			sum += p
		}
		if sum != size {
			t.Errorf("size %d: parts add up to %d", size, sum)
		}
	}
	if ps, n := PartPlan(3 << 30); ps != 16<<20 || n != 192 {
		t.Errorf("3 GiB = %d parts of %d, want 192 of 16 MiB", n, ps)
	}
	if ps, n := PartPlan(MaxFileSize); ps != 16<<20 || n != 1280 {
		t.Errorf("20 GiB = %d parts of %d, want 1280 of 16 MiB", n, ps)
	}
}

func TestClassify(t *testing.T) {
	ok := map[string]string{
		"video/mp4": "video", "VIDEO/MP4": "video", "video/mp4; codecs=avc1": "video", " video/quicktime ": "video", "video/webm": "video",
		"audio/mpeg": "audio", "audio/wav": "audio", "audio/x-m4a": "audio", "audio/mp4": "audio", "audio/ogg; codecs=opus": "audio",
		"image/jpeg": "image", "image/png": "image", "image/heic": "image",
	}
	for in, kind := range ok {
		if _, got, accepted := classify(in); !accepted || got != kind {
			t.Errorf("classify(%q) = %q, %v, want %q", in, got, accepted, kind)
		}
	}
	if mt, _, _ := classify("Video/MP4; codecs=avc1"); mt != "video/mp4" {
		t.Errorf("the stored media type = %q, want video/mp4", mt)
	}
	for _, in := range []string{
		"", "video", "video/", "video/x-matroska", "video/*", "application/octet-stream", "application/pdf", "text/html", "image/gif",
		"image/svg+xml", "image/webp", "audio/", "audio/*", "audio//x", "audio/a b", "audio/../x", "multipart/form-data", "video/mp4,video/webm",
	} {
		if mt, _, accepted := classify(in); accepted {
			t.Errorf("classify(%q) accepted as %q", in, mt)
		}
	}
}

func TestSanitizeFilename(t *testing.T) {
	good := map[string]string{
		"clip.mp4":                             "clip.mp4",
		"My Video (final).MOV":                 "My Video (final).MOV",
		"../../etc/passwd":                     "passwd",
		`C:\Users\ada\clip.mp4`:                "clip.mp4",
		"a/b/c.mp4":                            "c.mp4",
		"  spaced  .mp4  ":                     "spaced  .mp4",
		"..hidden.mp4":                         "hidden.mp4",
		"trailing.dots...":                     "trailing.dots",
		"tab\tand\nnewline.mp4":                "tabandnewline.mp4",
		"nul\x00byte.mp4":                      "nulbyte.mp4",
		"evil\u202Emp4.exe":                    "evilmp4.exe", // the right-to-left override is removed
		"zero\u200Bwidth.mp4":                  "zerowidth.mp4",
		"line\u2028sep.mp4":                    "linesep.mp4",
		"no\u00a0break.mp4":                    "no break.mp4",
		"café.mp4":                             "café.mp4",
		"cafe\u0301.mp4":                       "café.mp4", // NFC
		"日本語のビデオ.mp4":                          "日本語のビデオ.mp4",
		"emoji 💪 workout.mp4":                  "emoji 💪 workout.mp4",
		"semi;colon\"quote'.mp4":               "semi;colon\"quote'.mp4",
		"CON":                                  "CON",
		"a" + strings.Repeat("b", 10) + ".mp4": "a" + strings.Repeat("b", 10) + ".mp4",
	}
	for in, want := range good {
		got, ok := sanitizeFilename(in)
		if !ok || got != want {
			t.Errorf("sanitizeFilename(%q) = %q, %v, want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", " ", "...", "/", "a/", `\`, "\x00", "\u202E", "\xff\xfe", "foo\xffbar.mp4", "dir/..", "  ./  "} {
		if got, ok := sanitizeFilename(in); ok {
			t.Errorf("sanitizeFilename(%q) = %q, want it refused", in, got)
		}
	}
}

func TestSanitizeFilenameCapsLength(t *testing.T) {
	long := strings.Repeat("x", 1000) + ".mp4"
	got, ok := sanitizeFilename(long)
	if !ok || len(got) != MaxFilenameBytes || !strings.HasSuffix(got, ".mp4") {
		t.Errorf("long name: %d bytes %q, %v", len(got), got[len(got)-8:], ok)
	}
	// A cut never splits a multi-byte character.
	multi := strings.Repeat("日", 200) + ".mov"
	got, ok = sanitizeFilename(multi)
	if !ok || len(got) > MaxFilenameBytes || !utf8.ValidString(got) || !strings.HasSuffix(got, ".mov") {
		t.Errorf("multi-byte name: %d bytes valid=%v %v", len(got), utf8.ValidString(got), ok)
	}
	// A huge "extension" is not kept whole.
	got, ok = sanitizeFilename("a." + strings.Repeat("e", 600))
	if !ok || len(got) > MaxFilenameBytes {
		t.Errorf("long extension: %d bytes, %v", len(got), ok)
	}
}

func TestTitleOf(t *testing.T) {
	for in, want := range map[string]string{
		"clip.mp4":               "clip",
		"My.Video.final.mov":     "My.Video.final",
		"noextension":            "noextension",
		"trail .mp4":             "trail",
		"日本語.mp4":                "日本語",
		strings.Repeat("é", 300): strings.Repeat("é", MaxTitleChars),
	} {
		if got := titleOf(in); got != want {
			t.Errorf("titleOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateCreate(t *testing.T) {
	good := api.CreateAssetRequest{Filename: "a/b/Clip One.MP4", SizeBytes: 3 << 30, ContentType: "Video/MP4; codecs=avc1"}
	sp, e := validateCreate(good)
	if e != nil || sp.Kind != store.AssetKindVideo || sp.ContentType != "video/mp4" || sp.Filename != "Clip One.MP4" || sp.Title != "Clip One" || sp.Size != 3<<30 || sp.SHA256 != "" {
		t.Fatalf("validateCreate = %+v, %v", sp, e)
	}
	sum := strings.ToUpper(strings.Repeat("ab", 32))
	withSum := good
	withSum.Sha256 = &sum
	if sp, e := validateCreate(withSum); e != nil || sp.SHA256 != strings.ToLower(sum) {
		t.Errorf("sha256 = %q, %v, want it lower-cased", sp.SHA256, e)
	}

	bad := func(name string, mutate func(*api.CreateAssetRequest), status int, code api.ErrorCode) {
		t.Helper()
		req := good
		mutate(&req)
		_, e := validateCreate(req)
		if e == nil || e.Status != status || e.Code != code {
			t.Errorf("%s: %v, want %d %s", name, e, status, code)
		}
	}
	short := "abc"
	bad("zero size", func(r *api.CreateAssetRequest) { r.SizeBytes = 0 }, 400, api.ErrorCodeInvalidRequest)
	bad("negative size", func(r *api.CreateAssetRequest) { r.SizeBytes = -5 }, 400, api.ErrorCodeInvalidRequest)
	bad("21 GB", func(r *api.CreateAssetRequest) { r.SizeBytes = MaxFileSize + 1 }, 413, api.ErrorCodeFileTooLarge)
	bad("type", func(r *api.CreateAssetRequest) { r.ContentType = "application/zip" }, 415, api.ErrorCodeUnsupportedContentType)
	bad("no name", func(r *api.CreateAssetRequest) { r.Filename = "/.." }, 400, api.ErrorCodeInvalidRequest)
	bad("short sha", func(r *api.CreateAssetRequest) { r.Sha256 = &short }, 400, api.ErrorCodeInvalidRequest)
	if _, e := validateCreate(api.CreateAssetRequest{Filename: "x.mp4", SizeBytes: MaxFileSize, ContentType: "video/mp4"}); e != nil {
		t.Errorf("exactly 20 GiB was refused: %v", e)
	}
}

func TestCheckClaim(t *testing.T) {
	parts := func(nums ...int) []api.CompletedPartRef {
		out := make([]api.CompletedPartRef, len(nums))
		for i, n := range nums {
			out[i] = api.CompletedPartRef{PartNumber: n, Etag: `"e"`}
		}
		return out
	}
	if e := checkClaim(parts(3, 1, 2), 3); e != nil {
		t.Errorf("parts in any order: %v", e)
	}
	for name, p := range map[string][]api.CompletedPartRef{
		"too few":   parts(1, 2),
		"too many":  parts(1, 2, 3, 4),
		"duplicate": parts(1, 2, 2),
		"zero":      parts(0, 1, 2),
		"too high":  parts(1, 2, 4),
		"negative":  parts(-1, 2, 3),
	} {
		if e := checkClaim(p, 3); e == nil || e.Status != 422 || e.Code != api.ErrorCodePartsMismatch {
			t.Errorf("%s: %v, want 422 parts_mismatch", name, e)
		}
	}
	noTag := parts(1, 2, 3)
	noTag[1].Etag = `""`
	if e := checkClaim(noTag, 3); e == nil || e.Status != 400 {
		t.Errorf("an empty ETag: %v, want 400", e)
	}
}

// The enums the handler converts with plain casts are all in the spec's enums.
func TestEnumCastsAreValid(t *testing.T) {
	for _, k := range []string{store.AssetKindVideo, store.AssetKindAudio, store.AssetKindImage} {
		if !api.AssetKind(k).Valid() {
			t.Errorf("asset kind %q is not in the AssetKind enum", k)
		}
	}
	for _, s := range []string{store.AssetUploading, store.AssetUploaded, store.AssetProcessing, store.AssetReady, store.AssetFailed} {
		if !api.AssetStatus(s).Valid() {
			t.Errorf("asset status %q is not in the AssetStatus enum", s)
		}
	}
	for _, c := range []api.ErrorCode{
		api.ErrorCodeNotFound, api.ErrorCodeInsufficientRole, api.ErrorCodeUploadNotActive, api.ErrorCodeUploadExpired, api.ErrorCodeFileTooLarge,
		api.ErrorCodeUnsupportedContentType, api.ErrorCodePartsMismatch, api.ErrorCodeSizeMismatch, api.ErrorCodeTooManyUploads,
		api.ErrorCodeStorageUnavailable, api.ErrorCodeStorageNotConfigured,
	} {
		if !c.Valid() {
			t.Errorf("error code %q is not in the ErrorCode enum", c)
		}
	}
}
