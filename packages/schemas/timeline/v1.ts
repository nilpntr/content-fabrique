/* eslint-disable */
/**
 * This file was generated from v1.schema.json by json-schema-to-typescript (`bun run gen` in packages/schemas).
 * Do not edit it by hand: change the schema and regenerate. CI fails when this file is out of date.
 */

/**
 * Free text for humans. Allowed at the top level only. The invalid fixtures use it to name the issue code they must raise.
 */
export type Comment = string;
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
 * Playback speed from 0.25 (slow motion) to 4 (fast). Default 1.
 */
export type Speed = number;
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
 * A fixed colour preset: none, punchy (more contrast and saturation), warm, cool, bw (black and white) or matte (lifted blacks).
 */
export type Preset = "none" | "punchy" | "warm" | "cool" | "bw" | "matte";
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
 * crossfade blends the two clips. dip_to_black fades out to black and in again. whip is a fast sideways slide with motion blur.
 */
export type Type1 = "crossfade" | "dip_to_black" | "whip";
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
 * The words to show: 1 to 120 characters and at most 3 lines (lines are separated by a newline).
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
 * A sound in the mix, told apart by its "kind": trend_sound (cue-only, never reproduced by us), licensed_track (from the licensed library) or creator_audio (the creator's own upload).
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "audioItem".
 */
export type AudioItem = TrendSound | LicensedTrack | CreatorAudio;
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
export type SourceOffset = number;
/**
 * Always "cue_only" for a trend sound: it is never reproduced by us.
 */
export type ExportPolicy = "cue_only";
/**
 * How long the sound plays, in ms, 1 or more. Without it, it plays to the end of the timeline.
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
 * Length of the fade in at the start, in ms. Without it, there is no fade.
 */
export type FadeIn = number;
/**
 * Length of the fade out at the end, in ms. Without it, there is no fade.
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
export type SourceOffset1 = number;
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
export type SourceOffset2 = number;
/**
 * Always "licensed": the audio is mixed into the render.
 */
export type ExportPolicy2 = "licensed";
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
export type Kind3 = "beat" | "downbeat" | "drop" | "section";
/**
 * How strong the marker is, from 0 to 1. Default 1.
 */
export type Strength = number;
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
 * A time or duration as a whole number of milliseconds, 0 or more. No floats.
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
 * Length of the fade in at the start, in ms. Without it, there is no fade.
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "fadeIn".
 */
export type FadeIn1 = number;
/**
 * Length of the fade out at the end, in ms. Without it, there is no fade.
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "fadeOut".
 */
export type FadeOut1 = number;

/**
 * A Content Fabrique edit: the declarative timeline (an EDL in JSON) that agents and users change through edit ops and that the Go compiler turns into one ffmpeg invocation. Every time is an integer number of milliseconds.
 */
export interface Timeline {
  $comment?: Comment;
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
 * A cut from a source asset placed on the main track. Its output duration is round((source.out_ms - source.in_ms) / speed).
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
  reframe?: Reframe;
  look?: Look;
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
 * A colour treatment for the clip. Without it, the clip has no treatment.
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "look".
 */
export interface Look {
  preset: Preset;
}
/**
 * Whether the clip's own sound is in the mix. Without it, the clip is silent.
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "videoAudio".
 */
export interface ClipAudio {
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
  source_offset_ms: SourceOffset;
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
  source_offset_ms: SourceOffset1;
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
  source_offset_ms: SourceOffset2;
  export: ExportPolicy2;
  duration_ms?: Duration2;
  gain_db?: GainInDB;
  fade_in_ms?: FadeIn;
  fade_out_ms?: FadeOut;
}
/**
 * A point on the beat grid or a section boundary, in output time.
 *
 * This interface was referenced by `Timeline`'s JSON-Schema
 * via the `definition` "marker".
 */
export interface Marker {
  at_ms: Time;
  kind: Kind3;
  strength?: Strength;
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
