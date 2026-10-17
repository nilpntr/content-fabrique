/* eslint-disable */
/**
 * This file was generated from ops.v1.schema.json by json-schema-to-typescript (`bun run gen` in packages/schemas).
 * Do not edit it by hand: change the schema and regenerate. CI fails when this file is out of date.
 */

/**
 * One edit op on a timeline (timeline spec v1), told apart by its "op" field. The editor agent changes a timeline only by sending a batch of these ops (the opBatch definition). A batch is atomic: every op is applied in order to a copy, the main track is rippled so it stays contiguous, new ids are assigned, and the result is validated. If anything is wrong, nothing changes and every problem is reported with the index of the op that caused it. All times are integer milliseconds.
 */
export type EditOp =
  | AddClip
  | RemoveItem
  | MoveItem
  | TrimItem
  | ReplaceSource
  | SetSpeed
  | SetReframe
  | AddText
  | UpdateText
  | RemoveText
  | SetTransition
  | SetSound
  | RemoveSound
  | SnapCutsToBeats
  | SetLook
  | SetCaptions
  | SetClipAudio
  | SetMotion
  | SetSpeedRamp;
/**
 * "add_clip".
 */
export type Op = "add_clip";
/**
 * Optional id for the new clip. Without it the next free "c<n>" is used. It must not be in use anywhere in the timeline.
 */
export type ClipId = string;
/**
 * TypeID of an asset: the prefix "ast_" and a 26-character base32 UUIDv7.
 */
export type AssetId = string;
/**
 * Where the clip starts in the asset, in ms.
 */
export type InPoint = number;
/**
 * Where the clip ends in the asset, in ms. Exclusive.
 */
export type OutPoint = number;
/**
 * The position the new clip will have among the main clips, 0 for the start. Equal to the number of clips appends. Not together with after_id.
 */
export type Index = number;
/**
 * Put the new clip right after the clip with this id. Not together with index.
 */
export type After = string;
/**
 * Playback speed from 0.25 (slow motion) to 4 (fast). Default 1. A clip has either speed or speed_ramp, not both (checked by the Go layer).
 */
export type Speed = number;
/**
 * A speed that changes along the clip, as 2 to 16 points {at_ms, speed}. at_ms is a source offset: milliseconds into the clip's source range, counted from source.in_ms (0 to out_ms - in_ms), strictly increasing. The speed (0.25 to 4) changes linearly in source time between two points, is the first point's speed before the first and the last point's speed after the last. The clip's output duration is the integral of 1/speed over its source range. It replaces speed: give one of the two. The clip's own audio is muted (it cannot follow the ramp). Sorted points and the range are checked by the Go layer.
 *
 * @minItems 2
 * @maxItems 16
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
 * A fixed colour preset: none, punchy (more contrast and saturation), warm, cool, bw (black and white) or matte (lifted blacks).
 */
export type Preset1 = "none" | "punchy" | "warm" | "cool" | "bw" | "matte";
/**
 * How the clip begins. A cut is instant. The other types blend with the previous clip over duration_ms, and the clip then starts that long before the previous one ends. The first clip must have a cut.
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
 * True to mix the clip's own audio. Default false.
 */
export type UseSourceAudio = boolean;
/**
 * Volume change of the clip's audio in dB, from -30 to +6. Default 0.
 */
export type Gain = number;
/**
 * TypeID of a moment: the prefix "mom_" and a 26-character base32 UUIDv7.
 */
export type MomentId = string;
/**
 * Why the editor picked this clip, 280 characters at most. Shown to the creator in the UI and never rendered.
 */
export type Why = string;
/**
 * "remove_item".
 */
export type Op1 = "remove_item";
/**
 * The id of the main-track clip to remove.
 */
export type ClipId1 = string;
/**
 * Move an item. For a main-track clip, give index (the position it will have, 0 is the start) or after_id (right after that clip); the clips between shift and every at_ms is recomputed. For a text item or a sound, give at_ms, its new absolute start; nothing else moves. Give exactly one of at_ms, index and after_id. A clip that moves to the first position loses its transition_in.
 */
export type MoveItem = MoveItem1 & MoveItem2;
export type MoveItem1 =
  | {
      at_ms: unknown;
    }
  | {
      index: unknown;
    }
  | {
      after_id: unknown;
    };
/**
 * "move_item".
 */
export type Op2 = "move_item";
/**
 * The id of the clip, text item or sound to move.
 */
export type ItemId = string;
/**
 * New start in ms. Only for a text item or a sound.
 */
export type Start = number;
/**
 * The position the clip will have among the main clips, 0 for the start. Only for a clip.
 */
export type Index1 = number;
/**
 * Put the clip right after the clip with this id. Only for a clip.
 */
export type After1 = string;
/**
 * Trim an item. For a main-track clip, give in_ms and/or out_ms: the new source range in the asset's own time (absolute values, not changes). The clip's length changes and the clips after it ripple. For a text item or a sound, give duration_ms, its new length; it starts where it did. Do not mix the two kinds of fields.
 */
export type TrimItem = TrimItem1 & TrimItem2;
export type TrimItem1 =
  | {
      in_ms: unknown;
    }
  | {
      out_ms: unknown;
    }
  | {
      duration_ms: unknown;
    };
/**
 * "trim_item".
 */
export type Op3 = "trim_item";
/**
 * The id of the clip, text item or sound to trim.
 */
export type ItemId1 = string;
/**
 * New source in point of a clip, in ms. Must stay below the out point.
 */
export type InPoint1 = number;
/**
 * New source out point of a clip, in ms. Must stay within the asset's duration.
 */
export type OutPoint1 = number;
/**
 * New length of a text item (at least 300) or a sound (at least 1), in ms.
 */
export type Duration1 = number;
/**
 * "replace_source".
 */
export type Op4 = "replace_source";
/**
 * The id of the main-track clip to change.
 */
export type ClipId2 = string;
/**
 * "set_speed".
 */
export type Op5 = "set_speed";
/**
 * The id of the main-track clip.
 */
export type ClipId3 = string;
/**
 * "set_reframe".
 */
export type Op6 = "set_reframe";
/**
 * The id of the main-track clip.
 */
export type ClipId4 = string;
/**
 * cover fills the frame and crops around the focus point. contain fits the whole source and pads with the background colour. blur_fill fits the whole source over a blurred, scaled copy of itself.
 */
export type Mode1 = "cover" | "contain" | "blur_fill";
/**
 * 0 is the left edge of the source, 1 the right edge.
 */
export type FocusX1 = number;
/**
 * 0 is the top edge of the source, 1 the bottom edge.
 */
export type FocusY1 = number;
/**
 * Extra zoom into the centre of the framed clip, from 1 (none) to 3. Default 1.
 */
export type Zoom1 = number;
/**
 * Add a text overlay. It keeps its absolute time when clips move, and it must end inside the total duration of the timeline and inside the safe zone of the format. Give exactly one of anchor and position. Without id, the text gets the next free id such as "t3"; the result reports it. The text track is created when there is none.
 */
export type AddText = AddText1 & AddText2;
export type AddText1 =
  | {
      anchor: unknown;
    }
  | {
      position: unknown;
    };
/**
 * "add_text".
 */
export type Op7 = "add_text";
/**
 * Optional id for the new text item. Without it the next free "t<n>" is used.
 */
export type TextId = string;
/**
 * When the text appears, in ms.
 */
export type Start1 = number;
/**
 * How long the text stays, in ms. At least 300. It must end inside the total duration (checked by the Go layer).
 */
export type Duration2 = number;
/**
 * The words to show: 1 to 120 characters and at most 3 lines. Lines are separated by a newline (\n). No other control character is allowed, and neither are the line and paragraph separators U+2028 and U+2029.
 */
export type Text = string;
/**
 * The id of a text style from the style registry, such as "title_bold". The spec can't name a font directly.
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
 * Change fields of a text item; the fields you leave out stay as they are. Setting anchor removes the position and the other way round. Give at least one field.
 */
export type UpdateText = UpdateText1 & UpdateText2;
export type UpdateText1 =
  | {
      at_ms: unknown;
    }
  | {
      duration_ms: unknown;
    }
  | {
      text: unknown;
    }
  | {
      style: unknown;
    }
  | {
      anchor: unknown;
    }
  | {
      position: unknown;
    }
  | {
      animation: unknown;
    };
/**
 * "update_text".
 */
export type Op8 = "update_text";
/**
 * The id of the text item.
 */
export type TextId1 = string;
/**
 * New start in ms.
 */
export type Start2 = number;
/**
 * "remove_text".
 */
export type Op9 = "remove_text";
/**
 * The id of the text item to remove.
 */
export type TextId2 = string;
/**
 * Set how a clip begins. cut is instant. crossfade, dip_to_black, whip_left (whip is the same), whip_right, zoom_through and flash blend with the previous clip over duration_ms (100 to 1000) and the clip then starts that long before the previous one ends, so the clips after it ripple earlier. whip_left and whip_right are fast slides with motion blur, zoom_through zooms into the outgoing clip and out of the incoming one, flash dips to white. The first clip must have a cut, and a transition must be shorter than both clips it joins.
 */
export type SetTransition = SetTransition1 & SetTransition2;
export type SetTransition1 =
  | {
      type?: "cut";
    }
  | {
      type?: "crossfade" | "dip_to_black" | "whip" | "whip_left" | "whip_right" | "zoom_through" | "flash";
    };
/**
 * "set_transition".
 */
export type Op10 = "set_transition";
/**
 * The id of the main-track clip whose start gets the transition.
 */
export type ClipId5 = string;
/**
 * cut, crossfade, dip_to_black, whip (same as whip_left), whip_left, whip_right, zoom_through or flash.
 */
export type Type2 =
  "cut" | "crossfade" | "dip_to_black" | "whip" | "whip_left" | "whip_right" | "zoom_through" | "flash";
/**
 * Length of the transition in ms, 100 to 1000. It must be shorter than both clips it joins (checked by the Go layer).
 */
export type Duration3 = number;
/**
 * Add a sound, or replace the sound with the same id (a full replacement: fields you leave out go back to their defaults). export is set for you from the kind: a trend_sound is cue_only (a cue for the creator to add the sound in TikTok; we never reproduce it), the other two are licensed. Sounds keep their absolute time when clips move, must end inside the total duration, and the audio track is created when there is none. A licensed_track needs an audio asset with a licence, a creator_audio an audio asset with a rights attestation.
 */
export type SetSound = SetSoundTrendSound | SetSoundLicensedTrack | SetSoundCreatorAudio;
/**
 * "set_sound".
 */
export type Op11 = "set_sound";
/**
 * "trend_sound".
 */
export type Kind = "trend_sound";
/**
 * TypeID of a trend: the prefix "trd_" and a 26-character base32 UUIDv7.
 */
export type TrendId = string;
/**
 * Optional id. If a sound with this id exists, it is replaced entirely; if not, a new sound gets this id. Without id, a new sound gets the next free "s<n>".
 */
export type SoundId = string;
/**
 * When the sound starts in the output, in ms. Default 0.
 */
export type Start3 = number;
/**
 * Where in the sound to start playing, in ms. Default 0.
 */
export type SourceOffset1 = number;
/**
 * How long the sound plays, in ms, 1 or more. Without it, it plays to the end of the timeline. The sound must end inside the total duration, and source_offset_ms + this duration must fit inside the asset (both checked by the Go layer).
 */
export type Duration4 = number;
/**
 * Volume change in decibels, from -30 to +6. 0 leaves the level as it is.
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
 * "set_sound".
 */
export type Op12 = "set_sound";
/**
 * "licensed_track".
 */
export type Kind1 = "licensed_track";
/**
 * TypeID of a licence: the prefix "lic_" and a 26-character base32 UUIDv7.
 */
export type LicenceId = string;
/**
 * Optional id. If a sound with this id exists, it is replaced entirely; if not, a new sound gets this id. Without id, a new sound gets the next free "s<n>".
 */
export type SoundId1 = string;
/**
 * When the sound starts in the output, in ms. Default 0.
 */
export type Start4 = number;
/**
 * Where in the sound to start playing, in ms. Default 0.
 */
export type SourceOffset2 = number;
/**
 * "set_sound".
 */
export type Op13 = "set_sound";
/**
 * "creator_audio".
 */
export type Kind2 = "creator_audio";
/**
 * Optional id. If a sound with this id exists, it is replaced entirely; if not, a new sound gets this id. Without id, a new sound gets the next free "s<n>".
 */
export type SoundId2 = string;
/**
 * When the sound starts in the output, in ms. Default 0.
 */
export type Start5 = number;
/**
 * Where in the sound to start playing, in ms. Default 0.
 */
export type SourceOffset3 = number;
/**
 * "remove_sound".
 */
export type Op14 = "remove_sound";
/**
 * The id of the sound to remove.
 */
export type SoundId3 = string;
/**
 * "snap_cuts_to_beats".
 */
export type Op15 = "snap_cuts_to_beats";
/**
 * The beats of a sound, in the sound's own time (ms from its start) unless the op says otherwise. Either a constant tempo (bpm and first_beat_ms) or an explicit list of beat times (beats_ms) from beat tracking.
 */
export type BeatGrid = ConstantTempo | BeatList;
/**
 * Beats per minute, 40 to 240.
 */
export type Tempo = number;
/**
 * Time of the first beat in ms from the start of the sound. May have a fraction.
 */
export type FirstBeat = number;
/**
 * Beats in a bar, 1 to 16. Default 4.
 */
export type BeatsPerBar = number;
/**
 * Number of beats from the first beat to the first downbeat, less than beats_per_bar. Default 0.
 */
export type DownbeatOffset = number;
/**
 * Beat times in ms from the start of the sound: at least two, strictly increasing and at least 250 ms apart.
 *
 * @minItems 2
 * @maxItems 20000
 */
export type Beats = [number, number, ...number[]];
/**
 * Times of the downbeats; each must coincide with one of the beats. Without it, every beats_per_bar-th beat from the first is a downbeat.
 *
 * @maxItems 20000
 */
export type Downbeats = number[];
/**
 * Beats in a bar, 1 to 16. Default 4.
 */
export type BeatsPerBar1 = number;
/**
 * The id of the sound the grid belongs to. Without it the grid is in timeline time.
 */
export type SoundId4 = string;
/**
 * The furthest a cut may move to reach a beat, in ms. When both limits are given the smaller applies; without either it is half a beat.
 */
export type MaximumShift = number;
/**
 * The furthest a cut may move, in beats.
 */
export type MaximumShiftInBeats = number;
/**
 * Pick the nearest downbeat within the limit when there is one. A cut already on a beat stays there. Default false.
 */
export type PreferDownbeats = boolean;
/**
 * The shortest shot to keep, in ms. Default 300.
 */
export type MinimumShot = number;
/**
 * Also replace the beat and downbeat markers of the timeline with this grid's beats. Default false.
 */
export type WriteMarkers = boolean;
/**
 * "set_look".
 */
export type Op16 = "set_look";
/**
 * The id of the main-track clip. Leave it out for all clips.
 */
export type ClipId6 = string;
/**
 * A fixed colour preset: none, punchy (more contrast and saturation), warm, cool, bw (black and white) or matte (lifted blacks).
 */
export type Preset2 = "none" | "punchy" | "warm" | "cool" | "bw" | "matte";
/**
 * Create or change the captions track (burned-in captions from the transcript). The fields you leave out stay as they are; on a new captions track style defaults to word_highlight and enabled to true. Give at least one field.
 */
export type SetCaptions = SetCaptions1 & SetCaptions2;
export type SetCaptions1 =
  | {
      enabled: unknown;
    }
  | {
      style: unknown;
    }
  | {
      uppercase: unknown;
    }
  | {
      max_words: unknown;
    };
/**
 * "set_captions".
 */
export type Op17 = "set_captions";
/**
 * True to render the captions.
 */
export type Enabled = boolean;
/**
 * word_highlight shows a few words and highlights the one being said. chunk shows each group of words at once.
 */
export type Style = "word_highlight" | "chunk";
/**
 * True to write the captions in capitals. Default true.
 */
export type Uppercase = boolean;
/**
 * The most words on screen at once, 1 to 4. Default 2.
 */
export type MaximumWords = number;
/**
 * Set whether a clip's own sound is in the mix and how loud. The fields you leave out stay as they are; clips are silent by default.
 */
export type SetClipAudio = SetClipAudio1 & SetClipAudio2;
export type SetClipAudio1 =
  | {
      use_source: unknown;
    }
  | {
      gain_db: unknown;
    };
/**
 * "set_clip_audio".
 */
export type Op18 = "set_clip_audio";
/**
 * The id of the main-track clip.
 */
export type ClipId7 = string;
/**
 * True to mix the clip's own audio. Default false.
 */
export type UseSourceAudio1 = boolean;
/**
 * Volume change in decibels, from -30 to +6. 0 leaves the level as it is.
 */
export type GainInDB1 = number;
/**
 * "set_motion".
 */
export type Op19 = "set_motion";
/**
 * The id of the main-track clip.
 */
export type ClipId8 = string;
/**
 * none, punch_in, push_in, pull_back or shake.
 */
export type Preset3 = "none" | "punch_in" | "push_in" | "pull_back" | "shake";
/**
 * Only for punch_in (where the zoom has landed) and shake (where it starts): milliseconds from the clip's first frame, inside the clip. Without it punch_in zooms over the first frames of the clip and shake starts at its first frame.
 */
export type At1 = number;
/**
 * The peak scale increase, 0.05 (5 % bigger) to 0.35. Defaults: punch_in 0.15, push_in 0.1, pull_back 0.15, shake 0.08 (also the size of the shake).
 */
export type Strength1 = number;
/**
 * "set_speed_ramp".
 */
export type Op20 = "set_speed_ramp";
/**
 * The id of the main-track clip.
 */
export type ClipId9 = string;

/**
 * Add a clip from a source asset to the main track. Give the part of the asset to use as source.in_ms and source.out_ms (in the asset's own time; in_ms below out_ms, out_ms within the asset's duration). Say where it goes with index (the position it will have, 0 is the start) or after_id (right after that clip); without either it goes at the end. The clips after it move later (ripple) and their at_ms is recomputed. Without id, the clip gets the next free id such as "c7"; the result reports it. speed (or speed_ramp), reframe, motion, look, transition_in and audio are optional and default as in the timeline spec. A transition_in on a clip that ends up first is an error.
 */
export interface AddClip {
  op: Op;
  id?: ClipId;
  asset_id: AssetId;
  source: SourceRange;
  index?: Index;
  after_id?: After;
  speed?: Speed;
  speed_ramp?: SpeedRamp;
  reframe?: Reframe;
  motion?: Motion;
  look?: Look;
  transition_in?: TransitionIn;
  audio?: ClipAudio;
  moment_id?: MomentId;
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
 */
export interface Motion {
  preset: Preset;
  at_ms?: At;
  strength?: Strength;
}
/**
 * A colour treatment for the clip. Without it, the clip has no treatment.
 */
export interface Look {
  preset: Preset1;
}
/**
 * Whether the clip's own sound is in the mix. Without it, the clip is silent.
 */
export interface ClipAudio {
  use_source?: UseSourceAudio;
  gain_db?: Gain;
}
/**
 * Remove a clip from the main track. The clips after it move earlier (ripple). If the removed clip was first, the new first clip loses its transition_in (it becomes a cut). Text and sounds keep their times, so one that now runs past the end of the timeline is an error: shorten or move it in the same batch.
 */
export interface RemoveItem {
  op: Op1;
  id: ClipId1;
}
export interface MoveItem2 {
  op: Op2;
  id: ItemId;
  at_ms?: Start;
  index?: Index1;
  after_id?: After1;
}
export interface TrimItem2 {
  op: Op3;
  id: ItemId1;
  in_ms?: InPoint1;
  out_ms?: OutPoint1;
  duration_ms?: Duration1;
}
/**
 * Swap the asset and source range of a clip and keep everything else about it: its place, speed, reframe, look, transition and audio settings. The clips after it ripple if the new range has another length. The clip's moment_id is replaced by the given one, or removed when you give none, because the old moment no longer matches.
 */
export interface ReplaceSource {
  op: Op4;
  id: ClipId2;
  asset_id: AssetId;
  source: SourceRange;
  moment_id?: MomentId;
}
/**
 * Set the playback speed of a clip, from 0.25 (slow motion) to 4 (fast). A faster speed makes the clip shorter and the clips after it ripple earlier. It also removes a speed_ramp the clip had, since the ramp replaces the speed.
 */
export interface SetSpeed {
  op: Op5;
  id: ClipId3;
  speed: Speed;
}
/**
 * Set how a clip is fitted to the output frame: cover (fill and crop around focus), contain (fit and pad) or blur_fill (fit over a blurred copy). This replaces the whole reframe of the clip, so fields you leave out go back to their defaults (focus 0.5/0.5, zoom 1).
 */
export interface SetReframe {
  op: Op6;
  id: ClipId4;
  mode: Mode1;
  focus?: FocusPoint1;
  zoom?: Zoom1;
}
/**
 * The point of the source that the crop is centred on, as fractions of its width and height. Default 0.5/0.5. Only cover uses it.
 */
export interface FocusPoint1 {
  x: FocusX1;
  y: FocusY1;
}
export interface AddText2 {
  op: Op7;
  id?: TextId;
  at_ms: Start1;
  duration_ms: Duration2;
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
export interface UpdateText2 {
  op: Op8;
  id: TextId1;
  at_ms?: Start2;
  duration_ms?: Duration2;
  text?: Text;
  style?: StyleId;
  anchor?: Anchor;
  position?: Position;
  animation?: Animation;
}
/**
 * Remove a text item.
 */
export interface RemoveText {
  op: Op9;
  id: TextId2;
}
export interface SetTransition2 {
  op: Op10;
  id: ClipId5;
  type: Type2;
  duration_ms?: Duration3;
}
/**
 * Add a sound, or replace the sound with the same id (a full replacement: fields you leave out go back to their defaults). export is set for you from the kind: a trend_sound is cue_only (a cue for the creator to add the sound in TikTok; we never reproduce it), the other two are licensed. Sounds keep their absolute time when clips move, must end inside the total duration, and the audio track is created when there is none. A licensed_track needs an audio asset with a licence, a creator_audio an audio asset with a rights attestation. This variant: trend_sound.
 */
export interface SetSoundTrendSound {
  op: Op11;
  kind: Kind;
  trend_id: TrendId;
  id?: SoundId;
  at_ms?: Start3;
  source_offset_ms?: SourceOffset1;
  duration_ms?: Duration4;
  gain_db?: GainInDB;
  fade_in_ms?: FadeIn;
  fade_out_ms?: FadeOut;
}
/**
 * Add a sound, or replace the sound with the same id (a full replacement: fields you leave out go back to their defaults). export is set for you from the kind: a trend_sound is cue_only (a cue for the creator to add the sound in TikTok; we never reproduce it), the other two are licensed. Sounds keep their absolute time when clips move, must end inside the total duration, and the audio track is created when there is none. A licensed_track needs an audio asset with a licence, a creator_audio an audio asset with a rights attestation. This variant: licensed_track.
 */
export interface SetSoundLicensedTrack {
  op: Op12;
  kind: Kind1;
  asset_id: AssetId;
  license_id: LicenceId;
  id?: SoundId1;
  at_ms?: Start4;
  source_offset_ms?: SourceOffset2;
  duration_ms?: Duration4;
  gain_db?: GainInDB;
  fade_in_ms?: FadeIn;
  fade_out_ms?: FadeOut;
}
/**
 * Add a sound, or replace the sound with the same id (a full replacement: fields you leave out go back to their defaults). export is set for you from the kind: a trend_sound is cue_only (a cue for the creator to add the sound in TikTok; we never reproduce it), the other two are licensed. Sounds keep their absolute time when clips move, must end inside the total duration, and the audio track is created when there is none. A licensed_track needs an audio asset with a licence, a creator_audio an audio asset with a rights attestation. This variant: creator_audio.
 */
export interface SetSoundCreatorAudio {
  op: Op13;
  kind: Kind2;
  asset_id: AssetId;
  id?: SoundId2;
  at_ms?: Start5;
  source_offset_ms?: SourceOffset3;
  duration_ms?: Duration4;
  gain_db?: GainInDB;
  fade_in_ms?: FadeIn;
  fade_out_ms?: FadeOut;
}
/**
 * Remove a sound from the audio track.
 */
export interface RemoveSound {
  op: Op14;
  id: SoundId3;
}
/**
 * Move the cuts between main-track clips onto the beats of a grid, so the edit cuts on the music. Each cut (the at_ms of every clip after the first) moves to the nearest beat within the maximum shift (half a beat unless set), aligned to a frame, by trimming the end of the clip before it and the start of the clip after it; the total length does not change. A cut with no beat in range, or where moving would make a shot too short (under 300 ms) or run a clip past the end of its asset, stays where it is, and the result says so in a warning. If sound_id names a sound on the audio track, the grid is in that sound's time and is placed with the sound's at_ms and source_offset_ms; without it the grid is in timeline time.
 */
export interface SnapCutsToBeats {
  op: Op15;
  grid: BeatGrid;
  sound_id?: SoundId4;
  options?: SnapOptions;
}
/**
 * A grid of evenly spaced beats: beat n is at first_beat_ms + n * 60000 / bpm.
 */
export interface ConstantTempo {
  bpm: Tempo;
  first_beat_ms: FirstBeat;
  beats_per_bar?: BeatsPerBar;
  downbeat_offset?: DownbeatOffset;
}
/**
 * An explicit list of beat times, possibly irregular.
 */
export interface BeatList {
  beats_ms: Beats;
  downbeats_ms?: Downbeats;
  beats_per_bar?: BeatsPerBar1;
}
/**
 * How hard to snap.
 */
export interface SnapOptions {
  max_shift_ms?: MaximumShift;
  max_shift_beats?: MaximumShiftInBeats;
  prefer_downbeats?: PreferDownbeats;
  min_shot_ms?: MinimumShot;
  write_markers?: WriteMarkers;
}
/**
 * Set the colour look of a clip. Without id it applies to every clip on the main track.
 */
export interface SetLook {
  op: Op16;
  id?: ClipId6;
  preset: Preset2;
}
export interface SetCaptions2 {
  op: Op17;
  enabled?: Enabled;
  style?: Style;
  uppercase?: Uppercase;
  max_words?: MaximumWords;
}
export interface SetClipAudio2 {
  op: Op18;
  id: ClipId7;
  use_source?: UseSourceAudio1;
  gain_db?: GainInDB1;
}
/**
 * Set the camera move of a clip: a digital zoom or a shake on the already-reframed frame. punch_in is a fast zoom (about 5 frames) that lands at at_ms and holds: use it on a beat. push_in is a slow zoom over the whole clip, pull_back starts zoomed and pulls back to the full frame, shake is a short camera shake that starts at at_ms. This replaces the clip's whole motion, so fields you leave out go back to their defaults; none removes it. The clip must play for at least 400 ms, and at_ms (only for punch_in and shake) counts from the clip's own first frame, so it stays right when the clip moves.
 */
export interface SetMotion {
  op: Op19;
  id: ClipId8;
  preset: Preset3;
  at_ms?: At1;
  strength?: Strength1;
}
/**
 * Give a clip a speed that changes along it, for example fast into slow at the peak of a lift: points [{at_ms: 0, speed: 3}, {at_ms: 1500, speed: 3}, {at_ms: 2200, speed: 0.4}]. at_ms is a source offset: milliseconds from the clip's source.in_ms, strictly increasing and within the source range. Between points the speed changes linearly. The clip's length is the integral of the ramp, so it ripples the clips after it. It replaces speed (a set_speed later replaces the ramp), and the clip's own audio is muted while it has a ramp.
 */
export interface SetSpeedRamp {
  op: Op20;
  id: ClipId9;
  points: SpeedRamp;
}
