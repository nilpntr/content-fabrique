import * as React from "react";
import { type SpriteCue, type SpriteModel, parseSpriteVtt, spriteModel } from "./scrub";
import type { Sprite } from "./types";

const tracks = new Map<string, Promise<SpriteCue[]>>();

function loadTrack(url: string): Promise<SpriteCue[]> {
  let track = tracks.get(url);
  if (!track) {
    track = fetch(url).then((response) => {
      if (!response.ok) throw new Error(`sprite track ${response.status}`);
      return response.text().then(parseSpriteVtt);
    });
    // A failed track is not cached, so the next hover tries again.
    track.catch(() => tracks.delete(url));
    tracks.set(url, track);
  }
  return track;
}

/**
 * The scrub frames of a clip. A grid sprite is ready at once; a WebVTT track is fetched the first time `enabled`
 * turns true (the pointer is on the card), so a library of a hundred clips makes no sprite requests until someone hovers.
 */
export function useSpriteModel(sprite: Sprite | undefined, durationMs: number, enabled: boolean): SpriteModel | null {
  const vttUrl = sprite?.vttUrl;
  const [loaded, setLoaded] = React.useState<{ url: string; cues: SpriteCue[] } | null>(null);

  React.useEffect(() => {
    if (!vttUrl || !enabled) return;
    let alive = true;
    loadTrack(vttUrl).then(
      (cues) => {
        if (alive) setLoaded({ url: vttUrl, cues });
      },
      () => {},
    );
    return () => {
      alive = false;
    };
  }, [vttUrl, enabled]);

  const cues = loaded && loaded.url === vttUrl ? loaded.cues : null;
  return React.useMemo(() => (sprite ? spriteModel(sprite, durationMs, cues) : null), [sprite, durationMs, cues]);
}
