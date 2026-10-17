// Package render turns a validated timeline spec (internal/timeline) into an ffmpeg command and runs it
// (docs/design/timeline-v1.md section 6).
//
// The two halves are kept apart on purpose:
//
//   - Compile is pure. It takes a spec, a Profile and an AssetResolver and returns a Plan: the ffmpeg argument list,
//     the filter_complex, the frame-snapped cut list, the time map and the sound cues. It never starts a process, reads
//     a file or looks at the clock, so the same input gives the same Plan, byte for byte, and the golden plan tests
//     need no ffmpeg.
//   - Execute runs a Plan through a media.Runner (internal/media) and turns ffmpeg's -progress file into callbacks. In
//     production the runner is a media.SandboxRunner over the docker or hardened provider of internal/sandbox, with the
//     media-tools image; in development, the local provider.
//
// Text and captions (P3-04): every text item, every caption and the preview watermark is a Dialogue event of one generated ASS
// script (Plan.Subtitles), burned in by the ass filter after the clips are joined. The text is laid out with the style registry
// and safe zones of internal/timeline/style, the same code timeline.Validate checks with; Execute stages the script and the
// OFL fonts next to the output so every provider draws it the same way.
package render
