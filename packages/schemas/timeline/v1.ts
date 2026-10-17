/* eslint-disable */
/**
 * This file was generated from v1.schema.json by json-schema-to-typescript (`bun run gen` in packages/schemas).
 * Do not edit it by hand: change the schema and regenerate. CI fails when this file is out of date.
 */

/**
 * The constant "contentfabrique.timeline/v1". It names the spec version the document follows.
 */
export type SchemaVersion = "contentfabrique.timeline/v1";
/**
 * The rendered video: format, size, frame rate and background. The size must be the one of the format: 9:16 is 1080x1920, 1:1 is 1080x1080, 4:5 is 1080x1350 and 16:9 is 1920x1080.
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "output".
 */
export type Output = Output1 & Output2;
export type Output1 =
  | {
      format?: "9:16";
      width?: 1080;
      height?: 1920;
    }
  | {
      format?: "1:1";
      width?: 1080;
      height?: 1080;
    }
  | {
      format?: "4:5";
      width?: 1080;
      height?: 1350;
    }
  | {
      format?: "16:9";
      width?: 1920;
      height?: 1080;
    };
/**
 * The aspect ratio the project was created with (D-25). It fixes the width and height.
 */
export type Format = "9:16" | "1:1" | "4:5" | "16:9";
/**
 * Width in pixels. 1080, except 1920 for 16:9.
 */
export type Width = number;
/**
 * Height in pixels. 1920 for 9:16, 1080 for 1:1 and 16:9, 1350 for 4:5.
 */
export type Height = number;
/**
 * Frames per second of the render.
 */
export type FrameRate = 24 | 25 | 30 | 60;
/**
 * Colour behind the video where nothing covers it (letterboxing in contain mode), as #rrggbb.
 */
export type Background = string;
/**
 * Exactly one video track (its id is "main"), at most one text track, at most one captions track and at most one audio track, in any order.
 */
export type Tracks = Track[];
/**
 * A track, told apart by its "type": video, text, captions or audio.
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "track".
 */
export type Track = VideoTrack | TextTrack | CaptionsTrack | AudioTrack;
/**
 * Always "main" for the video track.
 */
export type TrackId = "main";
/**
 * "video".
 */
export type TrackType = "video";
/**
 * A lowercase id of 1 to 32 characters: a letter, then letters, digits or underscores, such as "c1" or "t3". Ids are unique across the whole spec. The edit ops name items by these ids.
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "itemId".
 */
export type ItemId = string;
/**
 * TypeID of an asset: the prefix "ast_" and a 26-character base32 UUIDv7.
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "assetId".
 */
export type AssetId = string;
/**
 * TypeID of a moment: the prefix "mom_" and a 26-character base32 UUIDv7.
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "momentId".
 */
export type MomentId = string;
/**
 * Where the clip starts in the asset, in ms.
 */
export type InPoint = number;
/**
 * Where the clip ends in the asset, in ms. Exclusive.
 */
export type OutPoint = number;
/**
 * When the clip starts in the output, in ms. It is redundant with the durations on purpose: the first clip is at 0 and each next one at the end of the previous one minus its own transition duration.
 */
export type StartOnTheTimeline = number;
/**
 * Playback speed from 0.25 (slow motion) to 4 (fast). Default 1. A clip has either speed or speed_ramp, not both (checked by the Go layer).
 */
export type Speed = number;
/**
 * A speed that changes along the clip, as 2 to 16 points {at_ms, speed}. at_ms is a source offset: milliseconds into the clip's source range, counted from source.in_ms (0 to out_ms - in_ms), strictly increasing. The speed (0.25 to 4) changes linearly in source time between two points, is the first point's speed before the first and the last point's speed after the last. The clip's output duration is the integral of 1/speed over its source range. It replaces speed: give one of the two. The clip's own audio is muted (it cannot follow the ramp). Sorted points and the range are checked by the Go layer.
 *
 * @minItems 2
 * @maxItems 16
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "speedRamp".
 */
export type SpeedRamp =
  | [
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      }
    ]
  | [
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      }
    ]
  | [
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      }
    ]
  | [
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      }
    ]
  | [
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      }
    ]
  | [
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      }
    ]
  | [
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      }
    ]
  | [
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      }
    ]
  | [
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      }
    ]
  | [
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      }
    ]
  | [
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      }
    ]
  | [
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      }
    ]
  | [
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      }
    ]
  | [
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      }
    ]
  | [
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      },
      {
        at_ms: SourceOffset;
        speed: Speed1;
      }
    ];
/**
 * Where the point is, in ms from source.in_ms (in source time).
 */
export type SourceOffset = number;
/**
 * The playback speed at the point, 0.25 to 4.
 */
export type Speed1 = number;
/**
 * cover fills the frame and crops around the focus point. contain fits the whole source and pads with the background colour. blur_fill fits the whole source over a blurred, scaled copy of itself.
 */
export type Mode = "cover" | "contain" | "blur_fill";
/**
 * 0 is the left edge of the source, 1 the right edge.
 */
export type FocusX = number;
/**
 * 0 is the top edge of the source, 1 the bottom edge.
 */
export type FocusY = number;
/**
 * Extra zoom into the centre of the framed clip, from 1 (none) to 3. Default 1.
 */
export type Zoom = number;
/**
 * none, punch_in, push_in, pull_back or shake.
 */
export type Preset = "none" | "punch_in" | "push_in" | "pull_back" | "shake";
/**
 * Only for punch_in (where the zoom has landed) and shake (where it starts): milliseconds from the clip's first frame, inside the clip. Without it punch_in zooms over the first frames of the clip and shake starts at its first frame.
 */
export type At = number;
/**
 * The peak scale increase, 0.05 (5 % bigger) to 0.35. Defaults: punch_in 0.15, push_in 0.1, pull_back 0.15, shake 0.08 (also the size of the shake).
 */
export type Strength = number;
/**
 * A fixed colour preset: none, punchy (more contrast and saturation), warm, cool, bw (black and white), matte (lifted blacks), auto (a mild contrast curve that keeps blacks and whites where they are, plus the clip's exposure_ev nudge, so shots from different angles meet at one exposure without washing out low-key footage) or gym_moody (warm skin, a little contrast above the shadows, a soft vignette; dark shadows are left alone).
 */
export type Preset1 = "none" | "punchy" | "warm" | "cool" | "bw" | "matte" | "auto" | "gym_moody";
/**
 * True to correct the clip's exposure by its exposure_ev hint (from the analysis; a nudge of at most 0.7 stops, black point kept) before the preset, so shots from different angles meet at one exposure. The preset auto always does this, whatever match says. Default false. Without an exposure_ev on the clip it changes nothing.
 */
export type MatchExposure = boolean;
/**
 * A small nudge, -1 to 1 stops (EV): how much brighter this clip is than the exposure the edit should have (positive: too bright, to be darkened; negative: too dark, to be lifted). It must come from highlight or midtone percentiles of the clip, never from its mean brightness, because a low-key clip (black wall, lit subject) has a low mean and is correctly exposed. The renderer applies the opposite, limited to 0.7 stops and ignoring hints under 0.15, with a highlight-preserving curve that keeps the black point, when look.match is true or the preset is auto. Without it, nothing is corrected.
 */
export type ExposureHint = number;
/**
 * How the clip begins. A cut is instant. The other types blend with the previous clip over duration_ms, and the clip then starts that long before the previous one ends. The first clip must have a cut.
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "transition".
 */
export type TransitionIn =
  | {
      type: Type;
    }
  | {
      type: Type1;
      duration_ms: Duration;
    };
/**
 * "cut": no blend, no duration.
 */
export type Type = "cut";
/**
 * crossfade blends the two clips. dip_to_black fades out to black and in again. whip_left is a fast slide to the left with motion blur (whip is the same, kept for older edits); whip_right slides to the right. zoom_through zooms into the outgoing clip and out of the incoming one through a blur. flash dips to white.
 */
export type Type1 = "crossfade" | "dip_to_black" | "whip" | "whip_left" | "whip_right" | "zoom_through" | "flash";
/**
 * Length of the transition in ms, 100 to 1000. It must be shorter than both clips it joins (checked by the Go layer).
 */
export type Duration = number;
/**
 * What the clip's own sound does. mute: nothing (the default: a clip with no audio settings is silent). bed: a low ambience under the edit, evened out across the clips and never normalised up to the programme, so the export is not silent where no sound is playing and the creator can add a trend sound on top; use it for footage with no speech (gym clatter, room tone). full: the programme: the clip's sound at its own level, normalised with the rest and ducked under sound effects; use it for speech. gain_db is a trim on top of the mode's level.
 */
export type Mode1 = "mute" | "bed" | "full";
/**
 * The old spelling of mode: true is full, false is mute. Not together with mode.
 */
export type UseSourceAudio = boolean;
/**
 * Volume change of the clip's audio in dB, from -30 to +6. Default 0.
 */
export type Gain = number;
/**
 * Why the editor picked this clip, 280 characters at most. Shown to the creator in the UI and never rendered.
 */
export type Why = string;
/**
 * The clips in time order. The first clip must have a cut (or no transition_in). An empty list is structurally valid and fails the duration check.
 */
export type Clips = VideoClip[];
/**
 * A lowercase id of 1 to 32 characters: a letter, then letters, digits or underscores, such as "c1" or "t3". Ids are unique across the whole spec. The edit ops name items by these ids.
 */
export type ItemId1 = string;
/**
 * "text".
 */
export type TrackType1 = "text";
/**
 * A piece of text shown for a while. Give it exactly one of anchor or position.
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "textItem".
 */
export type TextItem = TextItem1 & TextItem2;
export type TextItem1 =
  | {
      anchor: unknown;
    }
  | {
      position: unknown;
    };
/**
 * When the text appears, in ms. Text keeps its absolute time when clips move.
 */
export type Start = number;
/**
 * How long the text stays, in ms. At least 300. It must end inside the total duration (checked by the Go layer).
 */
export type Duration1 = number;
/**
 * The words to show: 1 to 120 characters and at most 3 lines. Lines are separated by a newline (\n). No other control character is allowed, and neither are the line and paragraph separators U+2028 and U+2029.
 */
export type Text = string;
/**
 * The id of a text style from the style registry, such as "title_bold". The spec can't name a font directly.
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "styleId".
 */
export type StyleId = string;
/**
 * A safe place for the text: upper_third, center or lower_third. Set this or position, not both.
 */
export type Anchor = "upper_third" | "center" | "lower_third";
/**
 * 0 is the left edge of the frame, 1 the right edge.
 */
export type PositionX = number;
/**
 * 0 is the top edge of the frame, 1 the bottom edge.
 */
export type PositionY = number;
/**
 * How the text enters: none, pop_in, fade, typewriter or slide_up. Default none.
 */
export type Animation = "none" | "pop_in" | "fade" | "typewriter" | "slide_up";
/**
 * The text items. They may overlap each other and may sit anywhere inside the total duration.
 */
export type TextItems = TextItem[];
/**
 * A lowercase id of 1 to 32 characters: a letter, then letters, digits or underscores, such as "c1" or "t3". Ids are unique across the whole spec. The edit ops name items by these ids.
 */
export type ItemId2 = string;
/**
 * "captions".
 */
export type TrackType2 = "captions";
/**
 * "transcript": the words come from the transcript of the clips' audio.
 */
export type Source = "transcript";
/**
 * word_highlight shows a few words and highlights the one being said. chunk shows each group of words at once.
 */
export type Style = "word_highlight" | "chunk";
/**
 * True to render the captions. False keeps the settings without rendering them.
 */
export type Enabled = boolean;
/**
 * True to write the captions in capitals. Default true.
 */
export type Uppercase = boolean;
/**
 * The most words on screen at once, 1 to 4. Default 2.
 */
export type MaximumWords = number;
/**
 * A lowercase id of 1 to 32 characters: a letter, then letters, digits or underscores, such as "c1" or "t3". Ids are unique across the whole spec. The edit ops name items by these ids.
 */
export type ItemId3 = string;
/**
 * "audio".
 */
export type TrackType3 = "audio";
/**
 * A sound in the mix, told apart by its "kind": trend_sound (cue-only, never reproduced by us), licensed_track (from the licensed library), creator_audio (the creator's own upload) or sfx (a sound effect the renderer synthesises: no asset, always in the export).
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "audioItem".
 */
export type AudioItem = TrendSound | LicensedTrack | CreatorAudio | SoundEffect;
/**
 * "trend_sound".
 */
export type Kind = "trend_sound";
/**
 * TypeID of a trend: the prefix "trd_" and a 26-character base32 UUIDv7.
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "trendId".
 */
export type TrendId = string;
/**
 * When the sound starts in the output, in ms.
 */
export type Start1 = number;
/**
 * Where in the sound to start playing, in ms.
 */
export type SourceOffset1 = number;
/**
 * Always "cue_only" for a trend sound: it is never reproduced by us.
 */
export type ExportPolicy = "cue_only";
/**
 * How long the sound plays, in ms, 1 or more. Without it, it plays to the end of the timeline. The sound must end inside the total duration, and source_offset_ms + this duration must fit inside the asset (both checked by the Go layer).
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "audioDuration".
 */
export type Duration2 = number;
/**
 * Volume change in decibels, from -30 to +6. 0 leaves the level as it is.
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "gainDb".
 */
export type GainInDB = number;
/**
 * Length of the fade in at the start, in ms. Without it, there is no fade. fade_in_ms + fade_out_ms must not exceed the sound's duration (checked by the Go layer).
 */
export type FadeIn = number;
/**
 * Length of the fade out at the end, in ms. Without it, there is no fade. fade_in_ms + fade_out_ms must not exceed the sound's duration (checked by the Go layer).
 */
export type FadeOut = number;
/**
 * "licensed_track".
 */
export type Kind1 = "licensed_track";
/**
 * TypeID of a licence: the prefix "lic_" and a 26-character base32 UUIDv7.
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "licenseId".
 */
export type LicenceId = string;
/**
 * When the track starts in the output, in ms.
 */
export type Start2 = number;
/**
 * Where in the track to start playing, in ms.
 */
export type SourceOffset2 = number;
/**
 * Always "licensed": the track is mixed into the render.
 */
export type ExportPolicy1 = "licensed";
/**
 * "creator_audio".
 */
export type Kind2 = "creator_audio";
/**
 * When the audio starts in the output, in ms.
 */
export type Start3 = number;
/**
 * Where in the audio to start playing, in ms.
 */
export type SourceOffset3 = number;
/**
 * Always "licensed": the audio is mixed into the render.
 */
export type ExportPolicy2 = "licensed";
/**
 * A sound effect that the renderer synthesises itself (no asset, no licence: it is ours, so its export is "licensed" and it is in every render and export). Each effect is built from layers (a transient, a body, a sub with a pitch drop, air or tail), banded, lightly saturated, glued by a compressor and given a short dark room, then loudness-matched to its category. Categories: whoosh (an air pass-by for a whip or a cut; variants air 0.6 s, fast 0.35 s, heavy 1.0 s with weight), swish (a quick thin air pass for small moves; thin 0.3 s, blade 0.2 s, cloth 0.4 s), impact (a low hit with a crack for a punch-in or the drop; punch 0.8 s, deep 1.4 s with a long sub, crack 0.5 s bright, metal 1.2 s), riser (a rising tension build that peaks at its end, so start it that long before the moment it leads to; tension 1.5 s, sweep 1.0 s filter sweep, pitch 2.0 s tonal, long 3.0 s), downlifter (a falling release after a hit or on the new section; fall 1.2 s, short 0.6 s, tape 0.8 s tape stop), sub_drop (a pitch-dropping sine under a hit; boom 1.0 s, quick 0.5 s, long 2.0 s), pop (a small bubble for an element appearing; bubble 0.3 s, tick 0.15 s, double 0.35 s), click (a UI click on the frame of an on-screen press; ui 80 ms, soft 100 ms, tonal 120 ms, mech 180 ms), glitch (a digital stutter for a glitchy cut; stutter 0.3 s, crush 0.25 s, chirp 0.4 s), shimmer (a sparkle for something magical or new; sparkle 1.2 s, wash 1.5 s, rise 1.0 s), sting (a logo or reveal hit with a tonal bloom; hit 2.5 s, bright 2.0 s, dark 3.0 s). It starts at at_ms and is cut at the end of the timeline. It is mixed at a fixed level plus gain_db, so it stays an accent. With speech (a clip in mode full) it is mixed after the loudness normalisation, above the programme, and the programme is ducked a few dB around it; with no speech (clips in mode bed or mute) it is mixed with the bed and the two are normalised together toward -17 LUFS, so the export has room for a trend sound.
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "sfxSound".
 */
export type SoundEffect = SfxSound;
/**
 * "sfx".
 */
export type Kind3 = "sfx";
/**
 * The effect category: whoosh, swish, impact, riser, downlifter, sub_drop, pop, click, glitch, shimmer, sting.
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "sfxName".
 */
export type Effect =
  | "whoosh"
  | "swish"
  | "impact"
  | "riser"
  | "downlifter"
  | "sub_drop"
  | "pop"
  | "click"
  | "glitch"
  | "shimmer"
  | "sting";
/**
 * Optional variant of the effect, one of the variants of its category: whoosh: air, fast, heavy; swish: thin, blade, cloth; impact: punch, deep, crack, metal; riser: tension, sweep, pitch, long; downlifter: fall, short, tape; sub_drop: boom, quick, long; pop: bubble, tick, double; click: ui, soft, tonal, mech; glitch: stutter, crush, chirp; shimmer: sparkle, wash, rise; sting: hit, bright, dark. Without it the category's first variant is used, which is what the effects were before variants existed. "auto" picks one of the category's variants from a hash of the item id, so repeats of one effect differ. Every item also gets a small, deterministic jitter of pitch, length and filter from its id, so two items of the same variant are never exactly the same sample.
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "sfxVariant".
 */
export type Variant = string;
/**
 * When the effect starts in the output, in ms. For an impact or a riser that should land on a beat, start it before the beat by the time it takes to build: a riser ends as long after it starts as its variant is long (1500 ms for the default).
 */
export type Start4 = number;
/**
 * Always "licensed": the effect is ours and is mixed into the render.
 */
export type ExportPolicy3 = "licensed";
/**
 * The sounds in the mix.
 */
export type AudioItems = AudioItem[];
/**
 * Where the marker is in the output, in ms. It must be inside the total duration (checked by the Go layer).
 */
export type Time = number;
/**
 * beat is a beat of the bar, downbeat the first beat of a bar, drop the big moment of the track and section the start of a part.
 */
export type Kind4 = "beat" | "downbeat" | "drop" | "section";
/**
 * How strong the marker is, from 0 to 1. Default 1.
 */
export type Strength1 = number;
/**
 * Beat grid and section marks that the editor uses to place cuts. They are never rendered.
 */
export type Markers = Marker[];
/**
 * TypeID of an idea: the prefix "ide_" and a 26-character base32 UUIDv7.
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "ideaId".
 */
export type IdeaId = string;
/**
 * TypeID of a concept: the prefix "con_" and a 26-character base32 UUIDv7.
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "conceptId".
 */
export type ConceptId = string;
/**
 * A time or duration as a whole number of milliseconds, from 0 to 86 400 000 (24 hours). No floats.
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "ms".
 */
export type Milliseconds = number;
/**
 * A number from 0 to 1.
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "unit".
 */
export type UnitInterval = number;
/**
 * Length of the fade in at the start, in ms. Without it, there is no fade. fade_in_ms + fade_out_ms must not exceed the sound's duration (checked by the Go layer).
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "fadeIn".
 */
export type FadeIn1 = number;
/**
 * Length of the fade out at the end, in ms. Without it, there is no fade. fade_in_ms + fade_out_ms must not exceed the sound's duration (checked by the Go layer).
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "fadeOut".
 */
export type FadeOut1 = number;

/**
 * A Content Fabrique edit: the declarative timeline (an EDL in JSON) that agents and users change through edit ops and that the Go compiler turns into one ffmpeg invocation. Every time is an integer number of milliseconds.
 */
export interface Timeline {
  schema: SchemaVersion;
  output: Output;
  tracks: Tracks;
  markers: Markers;
  meta?: Meta;
}
export interface Output2 {
  format: Format;
  width: Width;
  height: Height;
  fps: FrameRate;
  background: Background;
}
/**
 * The main track: the clips of the edit, one after the other. Items are sorted by at_ms, the first starts at 0 and has a cut, and each next one starts where the previous one ends (minus its own transition duration).
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "videoTrack".
 */
export interface VideoTrack {
  id: TrackId;
  type: TrackType;
  items: Clips;
}
/**
 * A cut from a source asset placed on the main track. Its output duration is floor((source.out_ms - source.in_ms) / speed + 0.5) ms.
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "videoItem".
 */
export interface VideoClip {
  id: ItemId;
  asset_id: AssetId;
  moment_id?: MomentId;
  source: SourceRange;
  at_ms: StartOnTheTimeline;
  speed?: Speed;
  speed_ramp?: SpeedRamp;
  reframe?: Reframe;
  motion?: Motion;
  look?: Look;
  exposure_ev?: ExposureHint;
  transition_in?: TransitionIn;
  audio?: ClipAudio;
  why?: Why;
}
/**
 * The part of the asset to use, in the asset's own time. in_ms must be below out_ms and out_ms within the asset's duration (checked by the Go layer).
 */
export interface SourceRange {
  in_ms: InPoint;
  out_ms: OutPoint;
}
/**
 * How the source is fitted to the output frame. Without it, the clip uses cover, focus 0.5/0.5 and zoom 1.
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "reframe".
 */
export interface Reframe {
  mode: Mode;
  focus?: FocusPoint;
  zoom?: Zoom;
}
/**
 * The point of the source that the crop is centred on, as fractions of its width and height. Default 0.5/0.5. Only cover uses it.
 */
export interface FocusPoint {
  x: FocusX;
  y: FocusY;
}
/**
 * A camera move on the clip, applied as a digital zoom on the already-reframed frame (after reframe, so it composes with every reframe mode and never shows a border). punch_in is a fast zoom (about 5 frames) that lands at at_ms and holds, push_in a slow continuous zoom over the whole clip, pull_back the reverse (starts zoomed, pulls back to the full frame) and shake a short camera shake that starts at at_ms and dies out (deterministic: the pattern comes from the clip's id). Needs a clip of at least 400 ms (checked by the Go layer).
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "motion".
 */
export interface Motion {
  preset: Preset;
  at_ms?: At;
  strength?: Strength;
}
/**
 * A colour treatment for the clip. Without it, the clip has no treatment.
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "look".
 */
export interface Look {
  preset: Preset1;
  match?: MatchExposure;
}
/**
 * What the clip's own sound does: mode mute, bed or full (or the old use_source). Without it, the clip is silent.
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "videoAudio".
 */
export interface ClipAudio {
  mode?: Mode1;
  use_source?: UseSourceAudio;
  gain_db?: Gain;
}
/**
 * Titles and captions that are written by hand. They are rendered as styled, animated text. At most one text track.
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "textTrack".
 */
export interface TextTrack {
  id: ItemId1;
  type: TrackType1;
  items: TextItems;
}
export interface TextItem2 {
  id: ItemId;
  at_ms: Start;
  duration_ms: Duration1;
  text: Text;
  style: StyleId;
  anchor?: Anchor;
  position?: Position;
  animation?: Animation;
}
/**
 * A free place for the text's centre as fractions of the frame. The text must stay inside the format's safe zone (checked by the Go layer). Set this or anchor, not both.
 */
export interface Position {
  x: PositionX;
  y: PositionY;
}
/**
 * Captions made from the transcript's word timestamps. It holds settings, not items. At most one captions track.
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "captionsTrack".
 */
export interface CaptionsTrack {
  id: ItemId2;
  type: TrackType2;
  from: Source;
  style: Style;
  enabled: Enabled;
  uppercase?: Uppercase;
  max_words?: MaximumWords;
}
/**
 * Music and sound on top of the clips' own audio. Items may overlap, because they are mixed. At most one audio track.
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "audioTrack".
 */
export interface AudioTrack {
  id: ItemId3;
  type: TrackType3;
  items: AudioItems;
}
/**
 * A trending sound. It is a cue for the creator to add the sound in TikTok and is never mixed or exported by us (D-03), so its export must be "cue_only". Previews play a generated beat guide instead.
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "trendSound".
 */
export interface TrendSound {
  id: ItemId;
  kind: Kind;
  trend_id: TrendId;
  at_ms: Start1;
  source_offset_ms: SourceOffset1;
  export: ExportPolicy;
  duration_ms?: Duration2;
  gain_db?: GainInDB;
  fade_in_ms?: FadeIn;
  fade_out_ms?: FadeOut;
}
/**
 * A track from the licensed library. It is mixed into the render, so its export must be "licensed".
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "licensedTrack".
 */
export interface LicensedTrack {
  id: ItemId;
  kind: Kind1;
  asset_id: AssetId;
  license_id: LicenceId;
  at_ms: Start2;
  source_offset_ms: SourceOffset2;
  export: ExportPolicy1;
  duration_ms?: Duration2;
  gain_db?: GainInDB;
  fade_in_ms?: FadeIn;
  fade_out_ms?: FadeOut;
}
/**
 * Audio the creator uploaded themselves, with a rights attestation. It is mixed into the render, so its export must be "licensed".
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "creatorAudio".
 */
export interface CreatorAudio {
  id: ItemId;
  kind: Kind2;
  asset_id: AssetId;
  at_ms: Start3;
  source_offset_ms: SourceOffset3;
  export: ExportPolicy2;
  duration_ms?: Duration2;
  gain_db?: GainInDB;
  fade_in_ms?: FadeIn;
  fade_out_ms?: FadeOut;
}
export interface SfxSound {
  id: ItemId;
  kind: Kind3;
  sfx: Effect;
  variant?: Variant;
  at_ms: Start4;
  export: ExportPolicy3;
  gain_db?: GainInDB;
}
/**
 * A point on the beat grid or a section boundary, in output time.
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "marker".
 */
export interface Marker {
  at_ms: Time;
  kind: Kind4;
  strength?: Strength1;
}
/**
 * Links back to the idea and concept this edit was made for.
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "meta".
 */
export interface Meta {
  idea_id?: IdeaId;
  concept_id?: ConceptId;
}
