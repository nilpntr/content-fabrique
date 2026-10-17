package assets

import (
	"mime"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// Limits of the upload API.
const (
	// MaxFileSize is the largest file: 20 GiB (the spec's maximum of size_bytes).
	MaxFileSize = 20 << 30
	// MinPartSize and DefaultPartSize: every part but the last is at least 8 MiB; the server picks 16 MiB (architecture
	// section 5 uses 16 to 64 MiB), which is 1,280 parts for the largest file.
	MinPartSize     = 8 << 20
	DefaultPartSize = 16 << 20
	// FirstBatch is how many part URLs the create call returns. MaxPartBatch is the most one /parts call may ask for.
	FirstBatch   = 20
	MaxPartBatch = 100
	// MaxFilenameBytes is the longest stored file name; assets.filename checks it too.
	MaxFilenameBytes = 255
	// MaxTitleChars is the longest title (the table checks it).
	MaxTitleChars = 255
)

// PartPlan cuts a file of size bytes into parts: the part size (every part but the last has it) and the part count. The
// size is at least MinPartSize and the count at most storage.MaxParts. size must be positive.
func PartPlan(size int64) (partSize int64, partCount int) {
	partSize = DefaultPartSize
	if ceilDiv(size, partSize) > storage.MaxParts {
		const mib = 1 << 20
		partSize = ceilDiv(ceilDiv(size, storage.MaxParts), mib) * mib
	}
	return partSize, int(ceilDiv(size, partSize))
}

// PartSizeOf is the exact size of part number n (1-based) of an upload cut by PartPlan.
func PartSizeOf(size, partSize int64, partCount, n int) int64 {
	if n < partCount {
		return partSize
	}
	return size - int64(partCount-1)*partSize
}

func ceilDiv(a, b int64) int64 { return (a + b - 1) / b }

// audioType matches audio/* media types (a type and a subtype made of token characters, no parameters).
var audioType = regexp.MustCompile(`^audio/[a-z0-9][a-z0-9.+_-]{0,100}$`)

var allowedTypes = map[string]string{
	"video/mp4":       store.AssetKindVideo,
	"video/quicktime": store.AssetKindVideo,
	"video/webm":      store.AssetKindVideo,
	"image/jpeg":      store.AssetKindImage,
	"image/png":       store.AssetKindImage,
	"image/heic":      store.AssetKindImage,
}

// classify normalises a Content-Type (lower case, parameters dropped) and returns the media type and the asset kind, or
// ok=false when the type is not accepted.
func classify(contentType string) (mediaType, kind string, ok bool) {
	mt, _, err := mime.ParseMediaType(strings.TrimSpace(contentType))
	if err != nil {
		return "", "", false
	}
	mt = strings.ToLower(mt)
	if kind, ok := allowedTypes[mt]; ok {
		return mt, kind, true
	}
	if audioType.MatchString(mt) {
		return mt, store.AssetKindAudio, true
	}
	return "", "", false
}

// sanitizeFilename turns the file name a client sent into one that is safe to store, show and put in a header: valid UTF-8
// in NFC, without any directory part, control or invisible formatting characters (the right-to-left override that makes
// "exe.mp4" read backwards, among them), without leading or trailing dots and spaces, and at most MaxFilenameBytes long (a
// short extension survives the cut). ok is false when nothing is left.
func sanitizeFilename(raw string) (string, bool) {
	if !utf8.ValidString(raw) {
		return "", false
	}
	name := norm.NFC.String(raw)
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), unicode.Is(unicode.Zl, r), unicode.Is(unicode.Zp, r), r == utf8.RuneError:
			return -1
		case unicode.Is(unicode.Zs, r):
			return ' '
		}
		return r
	}, name)
	name = strings.Trim(name, ". ")
	if name == "" {
		return "", false
	}
	return capBytes(name, MaxFilenameBytes), true
}

// capBytes shortens name to at most max bytes at a character boundary, keeping an extension of up to 16 bytes.
func capBytes(name string, limit int) string {
	if len(name) <= limit {
		return name
	}
	ext := ""
	if i := strings.LastIndex(name, "."); i > 0 && len(name)-i <= 16 {
		ext = name[i:]
	}
	stem := name[:len(name)-len(ext)]
	stem = stem[:limit-len(ext)]
	for len(stem) > 0 && !utf8.ValidString(stem) {
		stem = stem[:len(stem)-1]
	}
	stem = strings.TrimRight(stem, ". ")
	if stem == "" {
		return strings.TrimLeft(ext, ".")
	}
	return stem + ext
}

// titleOf is the file name without its extension (the whole name when that leaves nothing), at most MaxTitleChars.
func titleOf(filename string) string {
	title := filename
	if i := strings.LastIndex(filename, "."); i > 0 {
		title = filename[:i]
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = filename
	}
	if utf8.RuneCountInString(title) > MaxTitleChars {
		title = string([]rune(title)[:MaxTitleChars])
	}
	return title
}

// sha256Hex matches a hex SHA-256 in either case.
var sha256Hex = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// spec is a validated request to start an upload.
type spec struct {
	Kind        string
	ContentType string
	Filename    string
	Title       string
	Size        int64
	SHA256      string // lower case, or empty
}

// validateCreate checks a create request and returns what the server will store, or the error to answer with.
func validateCreate(req api.CreateAssetRequest) (spec, *Error) {
	if req.SizeBytes < 1 {
		return spec{}, invalid("size_bytes must be at least 1")
	}
	if req.SizeBytes > MaxFileSize {
		return spec{}, &Error{Status: 413, Code: api.ErrorCodeFileTooLarge, Message: "files can be at most 20 GB"}
	}
	mt, kind, ok := classify(req.ContentType)
	if !ok {
		return spec{}, &Error{Status: 415, Code: api.ErrorCodeUnsupportedContentType, Message: "this file type is not supported (video/mp4, video/quicktime, video/webm, audio/*, image/jpeg, image/png and image/heic are)"}
	}
	name, ok := sanitizeFilename(req.Filename)
	if !ok {
		return spec{}, invalid("filename is not usable")
	}
	s := spec{Kind: kind, ContentType: mt, Filename: name, Title: titleOf(name), Size: req.SizeBytes}
	if req.Sha256 != nil && *req.Sha256 != "" {
		if !sha256Hex.MatchString(*req.Sha256) {
			return spec{}, invalid("sha256 must be 64 hexadecimal characters")
		}
		s.SHA256 = strings.ToLower(*req.Sha256)
	}
	return s, nil
}
