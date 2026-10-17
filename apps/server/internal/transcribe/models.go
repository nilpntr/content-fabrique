package transcribe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ModelSpec is a model file the worker may use: where it comes from and the SHA-256 it must have. The files are not in the
// media-tools image (D-27): the worker downloads one on first use into the cache directory, checks it, and mounts it read-only into
// the sandbox. A file whose hash differs is never used, however it got there.
type ModelSpec struct {
	// Name is the short name configuration uses (TRANSCRIBE_MODEL).
	Name string
	// File is the file name in the cache directory.
	File string
	// URL is a pinned location: a Hugging Face commit, not a branch, so the bytes cannot change under the hash.
	URL    string
	SHA256 string
	// Size is the file's length in bytes, checked while downloading so a wrong file is stopped early.
	Size int64
	// DTW is the alignment-heads preset of whisper.cpp for this model (-dtw), which the word timings need. Empty for a model that is
	// not a speech model.
	DTW string
	// EnglishOnly models cannot be asked for another language.
	EnglishOnly bool
}

const (
	whisperCommit = "5359861c739e955e79d9a303bcbc70fb988958b1" // ggerganov/whisper.cpp on Hugging Face
	vadCommit     = "9ffd54a1e1ee413ddf265af9913beaf518d1639b" // ggml-org/whisper-vad on Hugging Face
)

// The pinned models. Add a model by adding its row, with the hash computed from the download and checked against the repository's
// LFS pointer (X-Linked-ETag), and by naming its licence in packages/media-tools/LICENSES.md.
var (
	// ModelLargeV3TurboQ5 is the default for local development: Whisper large-v3-turbo, quantised to 5 bits (574 MB).
	ModelLargeV3TurboQ5 = ModelSpec{
		Name: "large-v3-turbo-q5_0", File: "ggml-large-v3-turbo-q5_0.bin",
		URL:    "https://huggingface.co/ggerganov/whisper.cpp/resolve/" + whisperCommit + "/ggml-large-v3-turbo-q5_0.bin",
		SHA256: "394221709cd5ad1f40c46e6031ca61bce88931e6e088c188294c6d5a55ffa7e2", Size: 574041195, DTW: "large.v3.turbo",
	}
	// ModelTinyEn is what CI uses: Whisper tiny, English only (78 MB).
	ModelTinyEn = ModelSpec{
		Name: "tiny.en", File: "ggml-tiny.en.bin",
		URL:    "https://huggingface.co/ggerganov/whisper.cpp/resolve/" + whisperCommit + "/ggml-tiny.en.bin",
		SHA256: "921e4cf8686fdd993dcd081a5da5b6c365bfde1162e72b08d75ac75289920b1f", Size: 77704715, DTW: "tiny.en", EnglishOnly: true,
	}
	// ModelSileroVAD is the voice-activity model of whisper.cpp (Silero VAD v6.2.0 converted to ggml, MIT, 885 KB).
	ModelSileroVAD = ModelSpec{
		Name: "silero-v6.2.0", File: "ggml-silero-v6.2.0.bin",
		URL:    "https://huggingface.co/ggml-org/whisper-vad/resolve/" + vadCommit + "/ggml-silero-v6.2.0.bin",
		SHA256: "2aa269b785eeb53a82983a20501ddf7c1d9c48e33ab63a41391ac6c9f7fb6987", Size: 885098,
	}
)

// SpeechModels are the speech models TRANSCRIBE_MODEL may name.
var SpeechModels = []ModelSpec{ModelLargeV3TurboQ5, ModelTinyEn}

// SpeechModel finds a speech model by its short name.
func SpeechModel(name string) (ModelSpec, bool) {
	for _, m := range SpeechModels {
		if m.Name == name {
			return m, true
		}
	}
	return ModelSpec{}, false
}

var fileNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// ModelCache holds the downloaded models in one directory.
type ModelCache struct {
	// Dir is the cache directory. It is created on first use.
	Dir string
	// Client downloads; nil means a client with no overall timeout (a model is hundreds of MB) that gives up on a silent server.
	Client *http.Client

	mu       sync.Mutex
	verified map[string]verifiedFile
}

type verifiedFile struct {
	size    int64
	modTime time.Time
}

// ErrModelHash is a model file whose SHA-256 is not the pinned one.
var ErrModelHash = errors.New("transcribe: the model file does not match its pinned SHA-256")

// Ensure returns the host path of the model file, downloading it first when it is not in the cache. The file is checked against
// spec.SHA256 before the path is returned, and again when it has changed on disk since. A file that fails the check is deleted and
// downloaded once more; a download that fails the check is an error (ErrModelHash) and leaves nothing behind.
func (c *ModelCache) Ensure(ctx context.Context, spec ModelSpec) (string, error) {
	if !fileNameRE.MatchString(spec.File) || len(spec.SHA256) != 64 {
		return "", fmt.Errorf("transcribe: model %q has an invalid spec", spec.Name)
	}
	c.mu.Lock()
	defer c.mu.Unlock()                               // one download at a time: two jobs asking for the same model must not fetch it twice
	if err := os.MkdirAll(c.Dir, 0o755); err != nil { // #nosec G301 -- the sandbox's user must be able to read the files
		return "", fmt.Errorf("transcribe: model cache: %w", err)
	}
	path := filepath.Join(c.Dir, spec.File)
	if ok, err := c.check(path, spec); err != nil {
		return "", err
	} else if ok {
		return path, nil
	}
	if err := c.download(ctx, spec, path); err != nil {
		return "", err
	}
	if ok, err := c.check(path, spec); err != nil {
		return "", err
	} else if !ok {
		return "", fmt.Errorf("%w: %s", ErrModelHash, spec.File)
	}
	return path, nil
}

// check reports whether path holds the model. A missing file is false without error; a file with the wrong hash is deleted.
func (c *ModelCache) check(path string, spec ModelSpec) (bool, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("transcribe: model cache: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return false, fmt.Errorf("transcribe: %s in the model cache is not a regular file", spec.File)
	}
	if v, ok := c.verified[path]; ok && v.size == fi.Size() && v.modTime.Equal(fi.ModTime()) {
		return true, nil
	}
	sum, err := fileSHA256(path)
	if err != nil {
		return false, fmt.Errorf("transcribe: model cache: %w", err)
	}
	if sum != spec.SHA256 {
		_ = os.Remove(path)
		return false, nil
	}
	if c.verified == nil {
		c.verified = map[string]verifiedFile{}
	}
	c.verified[path] = verifiedFile{size: fi.Size(), modTime: fi.ModTime()}
	return true, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path) // #nosec G304 -- a fixed file name inside the cache directory
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// download fetches spec.URL into a temporary file next to path, checks its hash and renames it into place.
func (c *ModelCache) download(ctx context.Context, spec ModelSpec, path string) error {
	if !strings.HasPrefix(spec.URL, "https://") && !strings.HasPrefix(spec.URL, "http://127.0.0.1") {
		return fmt.Errorf("transcribe: model %q: the URL must be https", spec.Name)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, spec.URL, nil)
	if err != nil {
		return err
	}
	client := c.Client
	if client == nil {
		client = &http.Client{Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: time.Minute, TLSHandshakeTimeout: 30 * time.Second,
		}}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("transcribe: download %s: %w", spec.File, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("transcribe: download %s: %s", spec.File, resp.Status)
	}
	if spec.Size > 0 && resp.ContentLength > 0 && resp.ContentLength != spec.Size {
		return fmt.Errorf("transcribe: download %s: the server announces %d bytes, the model has %d", spec.File, resp.ContentLength, spec.Size)
	}
	tmp, err := os.CreateTemp(c.Dir, spec.File+".part-*")
	if err != nil {
		return fmt.Errorf("transcribe: model cache: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // a no-op after the rename
	h := sha256.New()
	limit := spec.Size
	if limit <= 0 {
		limit = 4 << 30
	}
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, limit+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("transcribe: download %s: %w", spec.File, err)
	}
	if n > limit {
		return fmt.Errorf("transcribe: download %s: more than the %d bytes of the model", spec.File, limit)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != spec.SHA256 {
		return fmt.Errorf("%w: %s is %s", ErrModelHash, spec.File, got)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil { // #nosec G302 -- the sandbox's user must be able to read it
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("transcribe: model cache: %w", err)
	}
	return nil
}
