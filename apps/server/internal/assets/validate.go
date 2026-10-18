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
	"github.com/nilpntr/content-fabrique/internal/styleprofile"
	"github.com/nilpntr/content-fabrique/internal/textclean"
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

// PartPlan cuts a file of size bytes (at least 1, at most MaxFileSize) into parts of DefaultPartSize: the part size (every
// part but the last has it) and the part count.
func PartPlan(size int64) (partSize int64, partCount int) {
	return DefaultPartSize, int(ceilDiv(size, DefaultPartSize))
}

// Compile-time checks of the limits above: the biggest allowed file fits in the parts S3 allows, and the part size is
// within what S3 and the API's own floor allow. A constant that breaks one of them fails the build (a negative constant
// does not convert to uint).
const (
	_ = uint(storage.MaxParts - (MaxFileSize+DefaultPartSize-1)/DefaultPartSize)
	_ = uint(DefaultPartSize - MinPartSize)
	_ = uint(storage.MaxPartSize - DefaultPartSize)
)

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
//
// The result is safe to store and to show, but it can still hold quotes, semicolons and non-ASCII letters. The first code
// that puts it in a Content-Disposition header (P1-04's download and proxy URLs) must encode it as RFC 6266 and RFC 5987
// require: a quoted ASCII fallback `filename=` plus `filename*=UTF-8”<percent-encoded>`, never the raw string.
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
		case textclean.Invisible(r): // the one definition of "invisible" that titles use too
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

// titleOf is the file name without its extension (the whole name when that leaves nothing), cleaned as user text (the same
// policy as a title a person types: package textclean) and at most MaxTitleChars.
func titleOf(filename string) string {
	title := filename
	if i := strings.LastIndex(filename, "."); i > 0 {
		title = filename[:i]
	}
	clean, ok := textclean.User(title, MaxTitleChars)
	if !ok {
		clean, ok = textclean.User(filename, MaxTitleChars)
	}
	if !ok {
		return "untitled" // a file name is never empty after sanitising, so this is unreachable in practice
	}
	return clean
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
	// Purpose is store.PurposeFootage or store.PurposeStyleReference.
	Purpose string
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
	s := spec{Kind: kind, ContentType: mt, Filename: name, Title: titleOf(name), Size: req.SizeBytes, Purpose: store.PurposeFootage}
	if req.Purpose != nil {
		switch *req.Purpose {
		case api.AssetPurposeFootage:
		case api.AssetPurposeStyleReference:
			// A style reference is one of the creator's finished videos (P6-05b): a video of at most 1 GiB.
			if kind != store.AssetKindVideo {
				return spec{}, &Error{Status: 415, Code: api.ErrorCodeUnsupportedContentType, Message: "a style reference must be a video (video/mp4, video/quicktime or video/webm)"}
			}
			if req.SizeBytes > styleprofile.MaxReferenceBytes {
				return spec{}, &Error{Status: 413, Code: api.ErrorCodeFileTooLarge, Message: "a style reference can be at most 1 GB"}
			}
			s.Purpose = store.PurposeStyleReference
		default:
			return spec{}, invalid("purpose must be footage or style_reference")
		}
	}
	if req.Sha256 != nil && *req.Sha256 != "" {
		if !sha256Hex.MatchString(*req.Sha256) {
			return spec{}, invalid("sha256 must be 64 hexadecimal characters")
		}
		s.SHA256 = strings.ToLower(*req.Sha256)
	}
	return s, nil
}
