package research

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The watch sheet (phase R2): what a person types in after watching a public post as a normal viewer. TikTok's and Instagram's terms ban
// automated collection, so this is our only source of saves and shares for other people's posts. A sheet is a CSV (a spreadsheet's
// export) or a JSON array of objects with the same keys. ParseWatchSheet checks every row with the same rules the store applies (the
// post link, the source's metrics, the vocabulary, the title form, the notes) and refuses media anywhere in it (CheckFixture), so a sheet
// that parses is one the store accepts.

//go:embed watch-sheet.template.csv
var watchSheetTemplate []byte

// WatchSheetTemplate returns the empty watch sheet with its explanation (`cfctl research template`).
func WatchSheetTemplate() []byte { return slices.Clone(watchSheetTemplate) }

// SheetColumns are the columns of the watch sheet, in the template's order. Only url is required.
func SheetColumns() []string {
	return []string{
		"url", "handle", "observer", "watched_at", "posted_at", "length", "format", "hook_kind", "hook_text", "caption_pattern", "cta",
		"topic", "cuts", "first_cut_s", MetricViews, MetricLikes, MetricComments, MetricSaves, MetricShares, "notes",
	}
}

// Limits of a sheet: a week's watching is about 50 rows.
const (
	MaxSheetBytes = 2 << 20
	MaxSheetRows  = 1000
)

// SheetRow is one checked row of a watch sheet.
type SheetRow struct {
	// Line is the row's line in a CSV file, or its 1-based index in a JSON array.
	Line int
	Post PostRef
	// Handle is the creator's normalized handle (from the link for TikTok).
	Handle   string
	Observer string
	// WatchedAt is when the person watched; zero when the sheet does not say.
	WatchedAt time.Time
	// PostedAt is the date (or time) the post shows, nil when not noted. PostedDateOnly says it was a date without a time.
	PostedAt       *time.Time
	PostedDateOnly bool
	DurationS      *float64
	// Metrics are the counts as seen (only the five names of Metrics()).
	Metrics map[string]float64
	// Annotation is what the person noted about the hook and form; its Annotator is the observer.
	Annotation Annotation
	// RawRef is the hex SHA-256 of the row as written (canonical link, observer and the non-empty cells): the same row imported twice
	// has the same RawRef, so it is stored once.
	RawRef string
}

// HasAnnotation says whether the row notes anything about the post's form (so an annotation is worth storing).
func (r SheetRow) HasAnnotation() bool {
	a := r.Annotation
	return a.FormatID != "" || a.HookKind != "" || a.HookTextPattern != "" || a.TextPattern != "" || a.CTA != "" || a.Topic != "" ||
		a.CutsEstimate != nil || a.FirstCutS != nil || a.Notes != ""
}

// SheetOptions are the defaults and the context of a parse.
type SheetOptions struct {
	// Observer is used for rows whose observer cell is empty.
	Observer string
	// Vocabulary checks the format ids and topics; the zero value is DefaultVocabulary.
	Vocabulary Vocabulary
	// Now is the time the parse runs (a watched or posted time after it is refused); zero means time.Now().
	Now time.Time
}

// ParseWatchSheet reads a watch sheet: a CSV file, or JSON when name ends in .json. It returns every row checked, or every problem
// found, each with its line, and stores nothing.
func ParseWatchSheet(name string, data []byte, opts SheetOptions) ([]SheetRow, error) {
	if len(data) > MaxSheetBytes {
		return nil, fmt.Errorf("research: the sheet is over %d bytes; split it", MaxSheetBytes)
	}
	if opts.Vocabulary.Formats == nil || opts.Vocabulary.Taxonomy == nil {
		opts.Vocabulary = DefaultVocabulary()
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	var (
		cells []map[string]string
		lines []int
		err   error
	)
	if strings.EqualFold(path.Ext(name), ".json") {
		cells, lines, err = readSheetJSON(data)
	} else {
		cells, lines, err = readSheetCSV(data)
	}
	if err != nil {
		return nil, err
	}
	if len(cells) > MaxSheetRows {
		return nil, fmt.Errorf("research: the sheet has %d rows, at most %d", len(cells), MaxSheetRows)
	}
	// The same media guard as a recorded fixture: no media key, no link to a media file or a video host, no data: URI, no very long text.
	asJSON, err := json.Marshal(cells)
	if err != nil {
		return nil, fmt.Errorf("research: sheet: %w", err)
	}
	if err := CheckFixture(asJSON); err != nil {
		return nil, fmt.Errorf("research: sheet: %w", err)
	}
	var (
		rows []SheetRow
		errs []error
		seen = map[string]int{}
	)
	for i, c := range cells {
		row, err := parseSheetRow(c, opts)
		if err != nil {
			errs = append(errs, fmt.Errorf("line %d: %w", lines[i], err))
			continue
		}
		row.Line = lines[i]
		if first, dup := seen[row.Post.URL]; dup {
			errs = append(errs, fmt.Errorf("line %d: the same post as line %d (one row per post in a sheet)", row.Line, first))
			continue
		}
		seen[row.Post.URL] = row.Line
		rows = append(rows, row)
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("research: the sheet has %d problem(s), nothing was imported:\n%w", len(errs), errors.Join(errs...))
	}
	return rows, nil
}

// checkHeader refuses a column that names media and a column that is not in the sheet.
func checkHeader(name string) error {
	for _, part := range mediaKeyParts {
		if strings.Contains(name, part) {
			return fmt.Errorf("%w: column %q", ErrMedia, name)
		}
	}
	if !slices.Contains(SheetColumns(), name) {
		return fmt.Errorf("research: unknown column %q (the columns are %s)", name, strings.Join(SheetColumns(), ", "))
	}
	return nil
}

func readSheetCSV(data []byte) ([]map[string]string, []int, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")) // a spreadsheet's byte order mark
	r := csv.NewReader(bytes.NewReader(data))
	r.Comment = '#'
	r.TrimLeadingSpace = true
	header, err := r.Read()
	if errors.Is(err, io.EOF) {
		return nil, nil, errors.New("research: the sheet is empty: it needs the header row of the template")
	}
	if err != nil {
		return nil, nil, fmt.Errorf("research: sheet: %w", err)
	}
	var errs []error
	for i, h := range header {
		header[i] = strings.ToLower(strings.TrimSpace(h))
		if err := checkHeader(header[i]); err != nil {
			errs = append(errs, err)
		}
		if slices.Index(header, header[i]) < i {
			errs = append(errs, fmt.Errorf("research: column %q appears twice", header[i]))
		}
	}
	if !slices.Contains(header, "url") {
		errs = append(errs, errors.New("research: the sheet has no url column"))
	}
	if len(errs) > 0 {
		return nil, nil, errors.Join(errs...)
	}
	var (
		cells []map[string]string
		lines []int
	)
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("research: sheet: %w", err)
		}
		line, _ := r.FieldPos(0)
		row := map[string]string{}
		for i, v := range rec {
			if v = strings.TrimSpace(v); v != "" {
				row[header[i]] = v
			}
		}
		if len(row) == 0 { // a spreadsheet's empty row: ",,,,"
			continue
		}
		cells = append(cells, row)
		lines = append(lines, line)
	}
	return cells, lines, nil
}

func readSheetJSON(data []byte) ([]map[string]string, []int, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, nil, fmt.Errorf("research: sheet is not JSON: %w", err)
	}
	if obj, ok := v.(map[string]any); ok {
		v = obj["rows"]
	}
	list, ok := v.([]any)
	if !ok {
		return nil, nil, errors.New(`research: a JSON sheet is an array of rows, or {"rows": [...]}`)
	}
	var (
		cells []map[string]string
		lines []int
		errs  []error
	)
	for i, item := range list {
		obj, ok := item.(map[string]any)
		if !ok {
			errs = append(errs, fmt.Errorf("row %d: not an object", i+1))
			continue
		}
		row := map[string]string{}
		for k, val := range obj {
			key := strings.ToLower(strings.TrimSpace(k))
			if err := checkHeader(key); err != nil {
				errs = append(errs, fmt.Errorf("row %d: %w", i+1, err))
				continue
			}
			var s string
			switch x := val.(type) {
			case nil:
			case string:
				s = x
			case json.Number:
				s = x.String()
			default:
				errs = append(errs, fmt.Errorf("row %d: %s must be a string or a number", i+1, key))
				continue
			}
			if s = strings.TrimSpace(s); s != "" {
				row[key] = s
			}
		}
		cells = append(cells, row)
		lines = append(lines, i+1)
	}
	return cells, lines, errors.Join(errs...)
}

func parseSheetRow(c map[string]string, opts SheetOptions) (SheetRow, error) {
	var (
		row  SheetRow
		errs []error
	)
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	ref, err := ParsePostURL(c["url"])
	if c["url"] == "" {
		return row, errors.New("url is empty")
	}
	if err != nil {
		return row, err
	}
	row.Post = ref
	row.Handle = ref.Handle
	if h := c["handle"]; h != "" {
		n, err := NormalizeHandle(h)
		switch {
		case err != nil:
			errs = append(errs, err)
		case ref.Handle != "" && n != ref.Handle:
			bad("handle %q is not the handle in the link (%q)", n, ref.Handle)
		default:
			row.Handle = n
		}
	} else if row.Handle == "" {
		bad("a %s link does not carry the creator: fill the handle", ref.Platform)
	}

	row.Observer = strings.TrimSpace(c["observer"])
	if row.Observer == "" {
		row.Observer = strings.TrimSpace(opts.Observer)
	}
	if row.Observer == "" || len(row.Observer) > 80 {
		bad("observer: who watched, 1 to 80 characters (a cell, or --observer)")
	}

	if s := c["watched_at"]; s != "" {
		t, _, err := parseSheetTime(s)
		switch {
		case err != nil:
			bad("watched_at: %v", err)
		case t.After(opts.Now.Add(Day)):
			bad("watched_at is in the future")
		default:
			row.WatchedAt = t
		}
	}
	if s := c["posted_at"]; s != "" {
		t, dateOnly, err := parseSheetTime(s)
		if err == nil {
			err = CheckPostTime(&t, "manual", opts.Now)
		}
		if err != nil {
			bad("posted_at: %v", err)
		} else {
			row.PostedAt, row.PostedDateOnly = &t, dateOnly
		}
	}
	if s := c["length"]; s != "" {
		d, err := parseLength(s)
		if err != nil {
			bad("length: %v", err)
		} else {
			row.DurationS = &d
		}
	}

	row.Metrics = map[string]float64{}
	for _, m := range Metrics() {
		s := c[m]
		if s == "" {
			continue
		}
		n, err := parseCount(s)
		if err != nil {
			bad("%s: %v", m, err)
			continue
		}
		row.Metrics[m] = n
	}
	if err := CheckObservation(SourceWatchSheet, MethodManual, ref.Platform, row.Metrics); err != nil {
		errs = append(errs, err)
	}

	lower := func(k string) string { return strings.ToLower(c[k]) }
	a := Annotation{
		FormatID: lower("format"), HookKind: lower("hook_kind"), HookTextPattern: c["hook_text"], TextPattern: lower("caption_pattern"),
		CTA: lower("cta"), Topic: lower("topic"), Notes: c["notes"], Annotator: row.Observer, Confidence: 1,
	}
	if s := c["cuts"]; s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 || n > 500 {
			bad("cuts: a whole number from 0 to 500")
		} else {
			a.CutsEstimate = &n
		}
	}
	if s := c["first_cut_s"]; s != "" {
		f, err := strconv.ParseFloat(strings.TrimSuffix(s, "s"), 64)
		if err != nil || math.IsNaN(f) || f < 0 || f > 7200 {
			bad("first_cut_s: seconds from 0 to 7200")
		} else {
			a.FirstCutS = &f
		}
	}
	row.Annotation = a
	if row.Observer != "" {
		if err := opts.Vocabulary.CheckAnnotation(a); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return row, errors.Join(errs...)
	}

	// The fingerprint of the row as written: the canonical link, the observer and the cells. A row imported again (the same sheet, or the
	// same link spelled another way) gets the same RawRef and is not stored twice.
	canon := map[string]string{}
	for k, v := range c {
		canon[k] = v
	}
	canon["url"], canon["observer"] = ref.URL, row.Observer
	b, _ := json.Marshal(canon) // map keys are sorted
	sum := sha256.Sum256(b)
	row.RawRef = hex.EncodeToString(sum[:])
	return row, nil
}

// parseSheetTime reads a date (2026-11-02, as a post shows it) or a time with its zone (RFC 3339).
func parseSheetTime(s string) (t time.Time, dateOnly bool, err error) {
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return t, true, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), false, nil
	}
	return time.Time{}, false, fmt.Errorf("%q is not a date (2026-11-02) or a time with its zone (2026-11-02T18:30:00+01:00)", clip(s))
}

// parseLength reads a video length: 42, 42s, 42.5 or 0:42.
func parseLength(s string) (float64, error) {
	var v float64
	if m, sec, ok := strings.Cut(s, ":"); ok {
		mi, err1 := strconv.Atoi(m)
		se, err2 := strconv.Atoi(sec)
		if err1 != nil || err2 != nil || mi < 0 || se < 0 || se > 59 || len(sec) != 2 {
			return 0, fmt.Errorf("%q is not m:ss", clip(s))
		}
		v = float64(mi*60 + se)
	} else {
		f, err := strconv.ParseFloat(strings.TrimSuffix(strings.ToLower(s), "s"), 64)
		if err != nil || math.IsNaN(f) {
			return 0, fmt.Errorf("%q is not a length in seconds (42, 42s or 0:42)", clip(s))
		}
		v = f
	}
	if v <= 0 || v > 7200 {
		return 0, errors.New("from 1 second to 2 hours")
	}
	return v, nil
}

// parseCount reads a count as a platform shows it: 987, 12,345, 1.2K, 3.4M or 1B. The result is a whole number.
func parseCount(s string) (float64, error) {
	t := strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(s), ",", ""), " ", "")
	mult := 1.0
	if n := len(t); n > 0 {
		switch t[n-1] {
		case 'k', 'K':
			mult, t = 1e3, t[:n-1]
		case 'm', 'M':
			mult, t = 1e6, t[:n-1]
		case 'b', 'B':
			mult, t = 1e9, t[:n-1]
		}
	}
	if mult == 1 && strings.Contains(t, ".") {
		return 0, fmt.Errorf("%q: a whole number, or a shown count such as 1.2K", clip(s))
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return 0, fmt.Errorf("%q is not a count (1234, 1,234, 1.2K, 3.4M)", clip(s))
	}
	v := math.Round(f * mult)
	if v > 1e12 {
		return 0, fmt.Errorf("%q is too large", clip(s))
	}
	return v, nil
}

// ShownDateMatches says whether a post time (from its id, in UTC) agrees with the date or time the post shows, as typed into the sheet.
// A shown date is a calendar day in the viewer's time zone, which may be anything from UTC-12 to UTC+14, so the day is widened by those
// offsets; a shown time with its zone must be within an hour.
func ShownDateMatches(posted, shown time.Time, dateOnly bool) bool {
	if !dateOnly {
		d := posted.Sub(shown)
		return d >= -time.Hour && d <= time.Hour
	}
	from := shown.Add(-14 * time.Hour)
	to := shown.Add(Day + 12*time.Hour)
	return !posted.Before(from) && posted.Before(to)
}
