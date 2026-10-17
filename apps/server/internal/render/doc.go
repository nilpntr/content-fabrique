// Package render turns a validated timeline spec (internal/timeline) into an ffmpeg command and runs it
// (docs/design/timeline-v1.md section 6).
//
// The two halves are kept apart on purpose:
//
//   - Compile is pure. It takes a spec, a Profile and an AssetResolver and returns a Plan: the ffmpeg argument list,
//     the filter_complex, the frame-snapped cut list, the time map and the sound cues. It never starts a process, reads
//     a file or looks at the clock, so the same input gives the same Plan, byte for byte, and the golden plan tests
//     need no ffmpeg.
//   - Execute runs a Plan through a Runner and turns ffmpeg's -progress output into callbacks. The Runner is the seam
//     where the sandbox providers of P1-03 plug in; LocalRunner starts ffmpeg as a child process.
//
// What this package does not do yet: text and captions (P3-04, which adds the ASS file and the style registry) and the
// preview watermark (it needs a font from the same registry). Text items compile to nothing and are listed in
// Plan.PendingText, so a test can assert that nothing is drawn.
package render
