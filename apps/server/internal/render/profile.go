package render

import (
	"fmt"
)

// ProfileName names a render profile.
type ProfileName string

// The two profiles.
const (
	ProfilePreview ProfileName = "preview"
	ProfileFinal   ProfileName = "final"
)

// Variant says which copy of an asset a profile reads.
type Variant string

// The asset variants: the 720p proxy that previews read, and the original upload that the final render reads.
const (
	VariantProxy    Variant = "proxy"
	VariantOriginal Variant = "original"
)

// Loudness targets of the mix (design note, audio graph): integrated loudness in LUFS, true peak in dBTP and loudness
// range in LU.
const (
	TargetLUFS = -14
	TargetTP   = -1
	TargetLRA  = 11
)

// AudioSampleRate is the sample rate of the whole audio graph and of the output.
const AudioSampleRate = 48000

// Profile is one row of the design note's profile table.
type Profile struct {
	Name ProfileName
	// SizeDivisor divides the spec's output size: 2 for the preview (half the format's size), 1 for the final.
	SizeDivisor int
	// Source is the asset variant the resolver is asked for.
	Source Variant
	// X264Preset, CRF and X264Profile ("" for none) configure the video encoder.
	X264Preset  string
	CRF         int
	X264Profile string
	// AudioBitrate is the AAC bitrate, for example "128k".
	AudioBitrate string
	// Faststart moves the MP4 index to the front (+faststart).
	Faststart bool
	// BeatGuide mixes in the clicks generated from the markers (preview only).
	BeatGuide bool
	// Watermark marks the profile as watermarked. The watermark needs a font, so it arrives with P3-04: until then the
	// Plan reports it in PendingWatermark and draws nothing.
	Watermark bool
	// LoudnormPasses is 1 for a single dynamic pass and 2 for measure-then-apply.
	LoudnormPasses int
}

// Preview is the cheap profile the editor and the agent look at.
var Preview = Profile{
	Name:           ProfilePreview,
	SizeDivisor:    2,
	Source:         VariantProxy,
	X264Preset:     "veryfast",
	CRF:            28,
	AudioBitrate:   "128k",
	BeatGuide:      true,
	Watermark:      true,
	LoudnormPasses: 1,
}

// Final is the profile of the exported file.
var Final = Profile{
	Name:           ProfileFinal,
	SizeDivisor:    1,
	Source:         VariantOriginal,
	X264Preset:     "medium",
	CRF:            18,
	X264Profile:    "high",
	AudioBitrate:   "192k",
	Faststart:      true,
	LoudnormPasses: 2,
}

// Profiles lists every profile, in a fixed order.
func Profiles() []Profile { return []Profile{Preview, Final} }

// ParseProfile returns the profile with the given name.
func ParseProfile(name string) (Profile, error) {
	switch ProfileName(name) {
	case ProfilePreview:
		return Preview, nil
	case ProfileFinal:
		return Final, nil
	}
	return Profile{}, fmt.Errorf("render: unknown profile %q (want preview or final)", name)
}
