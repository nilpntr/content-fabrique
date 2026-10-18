// Package media wraps ffprobe and ffmpeg. Command builders are pure functions that return argument lists (golden-tested);
// execution goes through a Runner, which in production is a sandbox provider, because these tools parse untrusted files
// (architecture section 5, D-08).
package media

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Rational is an exact frame rate such as 30000/1001.
type Rational struct {
	Num int64 `json:"num"`
	Den int64 `json:"den"`
}

// Float is Num/Den, or 0 when the rational is invalid.
func (r Rational) Float() float64 {
	if r.Den <= 0 || r.Num <= 0 {
		return 0
	}
	return float64(r.Num) / float64(r.Den)
}

// FPS is a frame rate as an exact rational and as a float.
type FPS struct {
	Rational
	Value float64 `json:"value"`
}

// AudioStream describes one audio track.
type AudioStream struct {
	// Index is the stream index in the file, usable as `-map 0:<index>`.
	Index      int    `json:"index"`
	Codec      string `json:"codec"` // ffprobe's codec name; "unknown" when ffprobe does not recognise it
	CodecTag   string `json:"codecTag,omitempty"`
	Channels   int    `json:"channels"`
	SampleRate int    `json:"sampleRate,omitempty"`
	Default    bool   `json:"default"`
	// Decodable is false for tracks ffmpeg cannot decode, such as the APAC spatial-audio track iPhones add next to
	// the AAC one.
	Decodable bool `json:"decodable"`
}

// Codecs lists the codec names found in the file.
type Codecs struct {
	Video string   `json:"video,omitempty"`
	Audio []string `json:"audio,omitempty"` // one per audio stream, in stream order
}

// Metadata is the normalised view of an ffprobe result. It never carries container or stream tags (location, device,
// creation time): only technical properties.
type Metadata struct {
	Container  string `json:"container"`         // "mp4", "mov", "matroska", ... see normaliseContainer
	FormatName string `json:"formatName"`        // ffprobe's raw format_name
	DurationMs int64  `json:"durationMs"`        // 0 when unknown
	SizeBytes  int64  `json:"sizeBytes"`         // 0 when unknown
	BitRate    int64  `json:"bitRate,omitempty"` // container bit rate, bits per second
	HasVideo   bool   `json:"hasVideo"`
	// VideoIndex is the stream index of the video stream the other fields describe, usable as `-map 0:<n>`. Meaningful
	// only when HasVideo.
	VideoIndex int  `json:"videoIndex"`
	HasAudio   bool `json:"hasAudio"`

	// Width and Height are the display size: after the rotation and the sample aspect ratio are applied, so an
	// upright iPhone portrait clip is 2160x3840 although it is stored as 3840x2160.
	Width  int `json:"width"`
	Height int `json:"height"`
	// CodedWidth and CodedHeight are the stored size.
	CodedWidth  int `json:"codedWidth"`
	CodedHeight int `json:"codedHeight"`
	// Rotation is the display-matrix rotation in degrees as ffprobe reports it (counter-clockwise positive, range
	// -180..180); -90 means the picture must be turned 90 degrees clockwise to be upright. ffmpeg applies it by
	// default (autorotate). See ClockwiseDegrees.
	Rotation int `json:"rotation"`

	FPS FPS `json:"fps"`
	// BaseFPS is the stream's base frame rate (ffprobe's r_frame_rate) when it differs from FPS by a frame or more: a phone's slow-motion clip conformed to
	// 30 frames a second, or re-encoded by an app, keeps the 120 or 240 it was shot at here. Absent when it is FPS.
	BaseFPS *FPS   `json:"baseFps,omitempty"`
	Codecs  Codecs `json:"codecs"`
	PixFmt  string `json:"pixFmt,omitempty"`

	ColorSpace     string `json:"colorSpace,omitempty"`
	ColorTransfer  string `json:"colorTransfer,omitempty"`
	ColorPrimaries string `json:"colorPrimaries,omitempty"`
	// HDR is true for PQ (smpte2084) and HLG (arib-std-b67) transfer characteristics; HDRFormat says which: "pq" or "hlg".
	HDR       bool   `json:"hdr"`
	HDRFormat string `json:"hdrFormat,omitempty"`

	AudioStreams []AudioStream `json:"audioStreams"`
	// SelectedAudio is the stream index to use for audio (-map 0:<n>), or nil when no audio stream is decodable. It is
	// the default-flagged decodable stream, else the first decodable one.
	SelectedAudio *int `json:"selectedAudio"`
}

// ClockwiseDegrees is the rotation to apply clockwise for an upright picture: 0, 90, 180 or 270 for the usual values.
func (m Metadata) ClockwiseDegrees() int { return ((-m.Rotation)%360 + 360) % 360 }

// SelectedAudioStream returns the selected audio stream.
func (m Metadata) SelectedAudioStream() (AudioStream, bool) {
	if m.SelectedAudio == nil {
		return AudioStream{}, false
	}
	for _, a := range m.AudioStreams {
		if a.Index == *m.SelectedAudio {
			return a, true
		}
	}
	return AudioStream{}, false
}

// ---- ffprobe JSON ----------------------------------------------------------

type rawProbe struct {
	Streams []rawStream `json:"streams"`
	Format  rawFormat   `json:"format"`
}

type rawFormat struct {
	FormatName string            `json:"format_name"`
	Duration   string            `json:"duration"`
	Size       string            `json:"size"`
	BitRate    string            `json:"bit_rate"`
	Tags       map[string]string `json:"tags"`
}

type rawStream struct {
	Index             int               `json:"index"`
	CodecName         string            `json:"codec_name"`
	CodecType         string            `json:"codec_type"`
	CodecTagString    string            `json:"codec_tag_string"`
	Width             int               `json:"width"`
	Height            int               `json:"height"`
	PixFmt            string            `json:"pix_fmt"`
	ColorSpace        string            `json:"color_space"`
	ColorTransfer     string            `json:"color_transfer"`
	ColorPrimaries    string            `json:"color_primaries"`
	SampleAspectRatio string            `json:"sample_aspect_ratio"`
	RFrameRate        string            `json:"r_frame_rate"`
	AvgFrameRate      string            `json:"avg_frame_rate"`
	Duration          string            `json:"duration"`
	Channels          int               `json:"channels"`
	SampleRate        string            `json:"sample_rate"`
	Disposition       map[string]int    `json:"disposition"`
	Tags              map[string]string `json:"tags"`
	SideDataList      []rawSideData     `json:"side_data_list"`
}

type rawSideData struct {
	Type     string   `json:"side_data_type"`
	Rotation *float64 `json:"rotation"`
}

// undecodableCodecTags are codec tags ffmpeg is known not to decode. APAC is Apple's spatial-audio codec; iPhone
// recordings carry it next to a normal AAC track. ffprobe reports no codec name for it. This list is a policy, not a
// probe of the local ffmpeg: a track is skipped even if a future ffmpeg learns to decode it, until we choose to allow it.
var undecodableCodecTags = map[string]bool{"apac": true}

// Normalise turns ffprobe's JSON (-print_format json -show_format -show_streams) into Metadata.
func Normalise(raw []byte) (Metadata, error) {
	var p rawProbe
	if err := json.Unmarshal(raw, &p); err != nil {
		return Metadata{}, fmt.Errorf("media: ffprobe output is not JSON: %w", err)
	}
	if len(p.Streams) == 0 && p.Format.FormatName == "" {
		return Metadata{}, fmt.Errorf("media: ffprobe output has no streams and no format")
	}
	var m Metadata
	m.FormatName = p.Format.FormatName
	m.SizeBytes = parseInt(p.Format.Size)
	m.BitRate = parseInt(p.Format.BitRate)

	var video *rawStream
	for i := range p.Streams {
		s := &p.Streams[i]
		switch s.CodecType {
		case "video":
			if s.Disposition["attached_pic"] == 1 {
				continue // cover art is not the video
			}
			if video == nil || (s.Disposition["default"] == 1 && video.Disposition["default"] != 1) {
				video = s
			}
		case "audio":
			m.AudioStreams = append(m.AudioStreams, audioStream(s))
		}
	}
	if video != nil {
		m.fillVideo(video)
	}
	m.Container = normaliseContainer(p.Format)
	m.DurationMs = durationMs(p.Format.Duration, video)
	m.HasAudio = len(m.AudioStreams) > 0
	if m.HasAudio {
		m.Codecs.Audio = make([]string, len(m.AudioStreams))
		for i, a := range m.AudioStreams {
			m.Codecs.Audio[i] = a.Codec
		}
	}
	m.SelectedAudio = selectAudio(m.AudioStreams)
	return m, nil
}

func (m *Metadata) fillVideo(s *rawStream) {
	m.HasVideo = true
	m.VideoIndex = s.Index
	m.Codecs.Video = s.CodecName
	m.PixFmt = s.PixFmt
	m.CodedWidth, m.CodedHeight = s.Width, s.Height
	m.ColorSpace, m.ColorTransfer, m.ColorPrimaries = known(s.ColorSpace), known(s.ColorTransfer), known(s.ColorPrimaries)
	switch m.ColorTransfer {
	case "smpte2084":
		m.HDR, m.HDRFormat = true, "pq"
	case "arib-std-b67":
		m.HDR, m.HDRFormat = true, "hlg"
	}

	m.Rotation = rotation(s)
	w, h := s.Width, s.Height
	if num, den, ok := parseRatio(s.SampleAspectRatio, ':'); ok && num != den {
		w = int(math.Round(float64(w) * float64(num) / float64(den)))
	}
	if abs(m.Rotation)%180 == 90 {
		w, h = h, w
	}
	m.Width, m.Height = w, h

	fr := parseRational(s.AvgFrameRate)
	if fr.Float() == 0 {
		fr = parseRational(s.RFrameRate)
	}
	m.FPS = FPS{Rational: fr, Value: fr.Float()}
	if base := parseRational(s.RFrameRate); base.Float() > 0 && math.Abs(base.Float()-fr.Float()) >= 1 {
		m.BaseFPS = &FPS{Rational: base, Value: base.Float()}
	}
}

// Slow motion (the clip type "slow_motion" of package taxonomy) is told from the frame rate, never from the pictures: a clip recorded
// at SlowMotionMinFPS or more, by its average rate or its base rate. A base rate above SlowMotionMaxFPS is a time base, not a frame
// rate (some files carry 90000/1), and says nothing.
const (
	SlowMotionMinFPS = 100
	SlowMotionMaxFPS = 1000
)

// SlowMotion says whether the probe found a high-frame-rate (slow-motion) recording.
func (m Metadata) SlowMotion() bool {
	in := func(f float64) bool { return f >= SlowMotionMinFPS && f <= SlowMotionMaxFPS }
	return in(m.FPS.Value) || (m.BaseFPS != nil && in(m.BaseFPS.Value))
}

// SlowMotionProbe is SlowMotion of a stored probe (assets.probe, the JSON of a Metadata); false when it cannot be read.
func SlowMotionProbe(probe []byte) bool {
	var m Metadata
	if len(probe) == 0 || json.Unmarshal(probe, &m) != nil {
		return false
	}
	return m.SlowMotion()
}

// rotation reads the display-matrix side data (ffmpeg 6 and later), falling back to the legacy "rotate" tag, which
// counts clockwise degrees. The result is in ffprobe's convention, normalised to -180..180.
func rotation(s *rawStream) int {
	for _, sd := range s.SideDataList {
		if sd.Rotation != nil {
			return normaliseAngle(int(math.Round(*sd.Rotation)))
		}
	}
	if t, ok := s.Tags["rotate"]; ok {
		if deg, err := strconv.Atoi(strings.TrimSpace(t)); err == nil {
			return normaliseAngle(-deg)
		}
	}
	return 0
}

func normaliseAngle(deg int) int {
	deg %= 360
	if deg > 180 {
		deg -= 360
	}
	if deg <= -180 {
		deg += 360
	}
	return deg
}

func audioStream(s *rawStream) AudioStream {
	codec := s.CodecName
	if codec == "" {
		codec = "unknown"
	}
	tag := strings.ToLower(strings.TrimSpace(s.CodecTagString))
	a := AudioStream{
		Index:      s.Index,
		Codec:      codec,
		Channels:   s.Channels,
		SampleRate: int(parseInt(s.SampleRate)),
		Default:    s.Disposition["default"] == 1,
	}
	if tag != "" && !strings.HasPrefix(tag, "[") { // "[0][0][0][0]" is ffprobe's placeholder for no tag
		a.CodecTag = tag
	}
	a.Decodable = s.CodecName != "" && s.CodecName != "unknown" && !undecodableCodecTags[tag] && !undecodableCodecTags[s.CodecName]
	return a
}

// selectAudio picks the default-flagged decodable stream, else the first decodable one.
func selectAudio(streams []AudioStream) *int {
	pick := -1
	for i, a := range streams {
		if !a.Decodable {
			continue
		}
		if a.Default {
			pick = i
			break
		}
		if pick < 0 {
			pick = i
		}
	}
	if pick < 0 {
		return nil
	}
	idx := streams[pick].Index
	return &idx
}

// normaliseContainer maps ffprobe's format_name list to one name. The MP4 family shares one demuxer, so the major brand
// tells QuickTime ("qt  ") from MP4.
func normaliseContainer(f rawFormat) string {
	names := strings.Split(f.FormatName, ",")
	for _, n := range names {
		if n == "mov" || n == "mp4" {
			if strings.HasPrefix(f.Tags["major_brand"], "qt") {
				return "mov"
			}
			return "mp4"
		}
	}
	if len(names) > 0 && names[0] != "" {
		return names[0]
	}
	return "unknown"
}

func durationMs(format string, video *rawStream) int64 {
	for _, s := range []string{format, videoDuration(video)} {
		if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil && f > 0 && !math.IsInf(f, 0) {
			return int64(math.Round(f * 1000))
		}
	}
	return 0
}

func videoDuration(s *rawStream) string {
	if s == nil {
		return ""
	}
	return s.Duration
}

// known drops ffprobe's "unknown" placeholder.
func known(s string) string {
	if s == "unknown" {
		return ""
	}
	return s
}

func parseInt(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func parseRational(s string) Rational {
	num, den, ok := parseRatio(s, '/')
	if !ok {
		return Rational{}
	}
	return Rational{Num: num, Den: den}
}

// parseRatio parses "a<sep>b" with both parts positive.
func parseRatio(s string, sep byte) (num, den int64, ok bool) {
	a, b, found := strings.Cut(s, string(sep))
	if !found {
		return 0, 0, false
	}
	num, err1 := strconv.ParseInt(strings.TrimSpace(a), 10, 64)
	den, err2 := strconv.ParseInt(strings.TrimSpace(b), 10, 64)
	if err1 != nil || err2 != nil || num <= 0 || den <= 0 {
		return 0, 0, false
	}
	return num, den, true
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
