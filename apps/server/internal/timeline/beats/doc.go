// Package beats holds the beat utilities of the timeline engine (P3-09): a beat
// grid, cut-to-beat snapping and tempo-aware durations. It uses the standard
// library only and knows nothing about the timeline types, so the edit ops
// (snap_cuts_to_beats, P3-02) and the editor tools build on it, not the other
// way round.
//
// # Time and rounding
//
// All public times are whole milliseconds on the timeline (int64), as in the
// schema. Beat positions are not whole numbers, so a grid keeps them as float64
// and rounds once, at the edge: beat n of a constant tempo is
//
//	t(n) = first + n × 60000 / bpm
//
// computed as one multiplication and one division (never by adding a rounded
// period n times), then rounded half up to a millisecond when it is handed out.
// A beat that lands exactly between two milliseconds (x.5) rounds up, the same
// rule as the frame formula below.
//
// # Frames
//
// The renderer snaps times to frames with frame(ms) = floor(ms × fps / 1000 +
// 0.5) (docs/design/timeline-v1.md §6). This package uses the same formula, in
// integer arithmetic for whole milliseconds so that no float error can move a
// .5 case, and its inverse FrameToMs(f) = floor(f × 1000 / fps + 0.5). The
// round trip MsToFrame(FrameToMs(f)) == f holds for every f because
// FrameToMs is off by at most half a millisecond and a frame is at least
// 16.6 ms long. A time t is frame-aligned when FrameToMs(MsToFrame(t)) == t.
//
// # Snap error
//
// A snapped cut is placed on the frame nearest to the exact beat time x, then
// converted back to milliseconds. The frame step costs at most half a frame
// (|f × 1000 / fps − x| ≤ 500 / fps ms) and the conversion at most half a
// millisecond, so the error is at most 500/fps + 0.5 ms, which is less than one
// frame (1000/fps ms) for every supported frame rate (24, 25, 30 and 60). The
// property tests check this at all four frame rates with random tempos.
package beats
