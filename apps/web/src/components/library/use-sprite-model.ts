import * as React from "react";
import { type SpriteCue, type SpriteModel, parseSpriteVtt, spriteModel } from "./scrub";
import type { Sprite } from "./types";

const tracks = new Map<string, Promise<SpriteCue[]>>();

function fetchTrack(url: string): Promise<string> {
  return fetch(url).then((response) => {
    if (!response.ok) throw new Error(`sprite track ${response.status}`);
    return response.text();
  });
}

function loadTrack(key: string, load: () => Promise<string>): Promise<SpriteCue[]> {
  let track = tracks.get(key);
  if (!track) {
    track = load().then(parseSpriteVtt);
    // A failed track is not cached, so the next hover tries again.
    track.catch(() => tracks.delete(key));
    tracks.set(key, track);
  }
  return track;
}

/**
 * The scrub frames of a clip. A grid sprite is ready at once; a WebVTT track is fetched the first time `enabled`
 * turns true (the pointer is on the card), so a library of a hundred clips makes no sprite requests until someone hovers.
 */
export function useSpriteModel(sprite: Sprite | undefined, durationMs: number, enabled: boolean): SpriteModel | null {
  // A track is told apart by its URL, or by the key of a track that is fetched with the API's token.
  const vttKey = sprite?.loadVtt ? sprite.vttKey : sprite?.vttUrl;
  const latest = React.useRef(sprite);
  latest.current = sprite;
  const [loaded, setLoaded] = React.useState<{ key: string; cues: SpriteCue[] } | null>(null);

  React.useEffect(() => {
    if (!vttKey || !enabled) return;
    let alive = true;
    const load = () => {
      const current = latest.current;
      if (current?.loadVtt) return current.loadVtt();
      return fetchTrack(current?.vttUrl ?? vttKey);
    };
    loadTrack(vttKey, load).then(
      (cues) => {
        if (alive) setLoaded({ key: vttKey, cues });
      },
      () => {},
    );
    return () => {
      alive = false;
    };
  }, [vttKey, enabled]);

  const cues = loaded && loaded.key === vttKey ? loaded.cues : null;
  return React.useMemo(() => (sprite ? spriteModel(sprite, durationMs, cues) : null), [sprite, durationMs, cues]);
}
