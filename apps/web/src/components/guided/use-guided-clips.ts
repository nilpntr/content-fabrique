/**
 * The clips (and the optional song) of one guided video: the files the creator handed over on the first screen, followed from
 * "uploading" to "ready to use". The upload queue of the app does the uploading (it carries on while the creator answers the
 * questions); the library's list says when each file has been prepared.
 *
 * A file is told apart by its name and size, which is all the queue shows of it (`addFiles` does not say which uploads it made:
 * a follow-up in docs/design/guided-flow.md). So a file that is already uploading from another page is followed, not uploaded
 * twice, and one that this browser finished uploading earlier is used as it is.
 */
import * as React from "react";
import { useAssets } from "@/lib/api/hooks";
import { contentTypeOf, rejectionOf } from "@/lib/upload/files";
import type { UploadSnapshot } from "@/lib/upload/manager";
import { useUploadCommands, useUploadSelector } from "@/lib/upload/provider";

/** A project takes 50 files at most (`POST /v1/projects`): 49 clips leave room for the song. */
export const MAX_CLIPS = 49;

export type ClipState =
  | { kind: "uploading"; percent: number }
  /** Uploaded; the server is still preparing it. */
  | { kind: "preparing" }
  | { kind: "ready" }
  /** `retry` when trying again can help. */
  | { kind: "problem"; message: string; retry: boolean };

export interface GuidedClip {
  key: string;
  name: string;
  /** The song, not footage. */
  song: boolean;
  state: ClipState;
  /** The library's id of the file, once the upload has one. */
  assetId?: string;
}

interface Picked {
  key: string;
  name: string;
  song: boolean;
}

/** What the flow needs to know of an upload in the queue. */
interface QueueEntry {
  id: string;
  key: string;
  status: UploadSnapshot["items"][number]["status"];
  percent: number;
  assetId: string | null;
}

const keyOf = (file: { name: string; size: number }): string => `${file.name}\u0000${file.size}`;

/** The uploads of footage; a video uploaded to learn the creator's style is not a clip, whatever its name. */
const selectQueue = (snapshot: UploadSnapshot): QueueEntry[] =>
  snapshot.items
    .filter((item) => item.purpose === undefined)
    .map((item) => ({ id: item.id, key: keyOf(item), status: item.status, percent: Math.round(item.progress * 100), assetId: item.assetId }));

/** The queue changes many times a second while bytes move; the flow renders again only when a percent or a status does. */
const sameQueue = (a: QueueEntry[], b: QueueEntry[]): boolean =>
  a.length === b.length &&
  a.every((entry, index) => {
    const other = b[index] as QueueEntry;
    return entry.id === other.id && entry.status === other.status && entry.percent === other.percent && entry.assetId === other.assetId;
  });

/** Why a file cannot be part of a video, in a sentence, or null when it can. */
export function fileProblem(file: Pick<File, "name" | "type" | "size">): string | null {
  const type = contentTypeOf(file);
  if (type?.startsWith("image/")) return "Photos can't be used yet. Add video clips.";
  if (file.size <= 0) return `“${file.name}” is empty. Add another clip.`;
  if (type === null) return "That file can't be used. Add a video (MP4 or MOV) or a song (MP3, M4A, WAV).";
  if (rejectionOf(file) !== null) return `“${file.name}” is too big. Add a shorter clip.`;
  return null;
}

const GONE: ClipState = { kind: "problem", message: "This one is no longer uploading. Remove it and add it again.", retry: false };

/** What the library's list says about the files: each one's status, and whether the list itself could be read. */
interface Library {
  statusOf: ReadonlyMap<string, string>;
  failed: boolean;
}

/**
 * Where a file is. `handedOver`: the queue has been given the file (so an upload that is not there has been removed, not "not
 * started yet"). `missing`: the library's list was read again after the upload ended, and the file is not in it.
 */
export function stateOf(
  entry: Pick<QueueEntry, "status" | "percent"> | undefined,
  assetId: string | undefined,
  library: Library,
  { handedOver, missing }: { handedOver: boolean; missing: boolean },
): ClipState {
  if (entry?.status === "failed") return { kind: "problem", message: "This one didn't upload. Try again.", retry: true };
  if (entry?.status === "needs-file") return { kind: "problem", message: "This one was interrupted. Remove it and add it again.", retry: false };
  if (entry && entry.status !== "done") return { kind: "uploading", percent: entry.percent };
  if (!assetId) return entry || handedOver ? GONE : { kind: "uploading", percent: 0 };
  const status = library.statusOf.get(assetId);
  if (status === "ready") return { kind: "ready" };
  if (status === "failed") return { kind: "problem", message: "I couldn't read this one. Remove it and add another.", retry: false };
  if (status === undefined && library.failed) return { kind: "problem", message: "I couldn't check on this one. Check your connection and try again.", retry: true };
  // Uploaded, and the library has no such file: it was deleted, or its upload was stopped from the uploads list.
  if (status === undefined && missing) return GONE;
  return { kind: "preparing" };
}

export interface GuidedClips {
  /** False outside a signed-in workspace: there is nowhere to upload to. */
  available: boolean;
  /** The footage, in the order it was added. */
  clips: GuidedClip[];
  song: GuidedClip | undefined;
  /** Why the last files handed over were left out, in a sentence. */
  notice: string | undefined;
  add(files: readonly File[]): void;
  remove(key: string): void;
  retry(key: string): void;
}

export function useGuidedClips(): GuidedClips {
  const commands = useUploadCommands();
  const queue = useUploadSelector(selectQueue, sameQueue);
  const assets = useAssets();
  const [picked, setPicked] = React.useState<Picked[]>([]);
  const [notice, setNotice] = React.useState<string | undefined>(undefined);
  // An upload leaves the queue when its row is dismissed in the uploads tray; the file's id is kept from when it was last seen.
  const [seen, setSeen] = React.useState<Readonly<Record<string, string>>>({});
  /** The files the queue has been given (or that needed no upload): from then on, a missing upload is a removed one. */
  const [handedOver, setHandedOver] = React.useState<ReadonlySet<string>>(new Set());

  const entryOf = React.useCallback((key: string) => queue.findLast((entry) => entry.key === key), [queue]);

  React.useEffect(() => {
    setSeen((previous) => {
      let next = previous;
      for (const { key } of picked) {
        const assetId = entryOf(key)?.assetId;
        if (assetId && next[key] !== assetId) next = { ...next, [key]: assetId };
      }
      return next;
    });
  }, [picked, entryOf]);

  const library = React.useMemo(
    (): Library => ({
      statusOf: new Map((assets.data ?? []).map((asset) => [asset.id as string, asset.status as string])),
      failed: assets.isError,
    }),
    [assets.data, assets.isError],
  );

  // An uploaded file that the list does not have: the list may simply be older than the upload, so it is read once more (once a
  // file) before the file counts as gone. `asked` holds when; a list read after that which still lacks the file settles it.
  const asked = React.useRef(new Map<string, number>());
  const settled = assets.isSuccess && !assets.isFetching;
  const { refetch, dataUpdatedAt } = assets;
  const unlisted = picked.flatMap((file) => {
    const entry = entryOf(file.key);
    const assetId = entry?.assetId ?? seen[file.key];
    return assetId && (!entry || entry.status === "done") && !library.statusOf.has(assetId) ? [assetId] : [];
  });
  const unlistedKey = unlisted.join(",");
  React.useEffect(() => {
    if (!settled || unlistedKey === "") return;
    const fresh = unlistedKey.split(",").filter((assetId) => !asked.current.has(assetId));
    if (fresh.length === 0) return;
    const now = Date.now();
    for (const assetId of fresh) asked.current.set(assetId, now);
    void refetch();
  }, [settled, unlistedKey, refetch]);

  const all = picked.map((file): GuidedClip => {
    const entry = entryOf(file.key);
    const assetId = entry?.assetId ?? seen[file.key];
    const askedAt = assetId ? asked.current.get(assetId) : undefined;
    const missing = settled && askedAt !== undefined && dataUpdatedAt > askedAt;
    return { ...file, state: stateOf(entry, assetId, library, { handedOver: handedOver.has(file.key), missing }), ...(assetId ? { assetId } : {}) };
  });
  const clips = all.filter((clip) => !clip.song);
  const song = all.find((clip) => clip.song);

  /** Stops the upload of a file that is no longer part of the video; a finished one stays in the library. */
  function stopUpload(key: string) {
    const entry = entryOf(key);
    if (entry && entry.status !== "done") commands.cancel(entry.id);
  }

  function forget(key: string) {
    setSeen(({ [key]: _gone, ...rest }) => rest);
    setHandedOver((previous) => new Set([...previous].filter((other) => other !== key)));
  }

  function add(files: readonly File[]) {
    if (!commands.available) {
      setNotice("Uploading isn't available right now. Reload the page and try again.");
      return;
    }
    const problems = files.map(fileProblem).filter((problem): problem is string => problem !== null);
    // One row a file: the same file twice in one go (or one that is already listed) is taken once.
    const taken = new Set(picked.map((file) => file.key));
    const fresh: { file: File; picked: Picked }[] = [];
    for (const file of files) {
      const key = keyOf(file);
      if (fileProblem(file) !== null || taken.has(key)) continue;
      taken.add(key);
      fresh.push({ file, picked: { key, name: file.name, song: contentTypeOf(file)?.startsWith("audio/") ?? false } });
    }
    // One song per video: the last one handed over takes the place of the others.
    const newSong = fresh.findLast((entry) => entry.picked.song);
    const room = Math.max(0, MAX_CLIPS - clips.length);
    const newClips = fresh.filter((entry) => !entry.picked.song);
    const accepted = [...newClips.slice(0, room), ...(newSong ? [newSong] : [])];
    setNotice(newClips.length > room ? `A video can use up to ${MAX_CLIPS} clips. I've left the rest out.` : problems[0]);
    if (accepted.length === 0) return;

    if (newSong && song) {
      stopUpload(song.key);
      forget(song.key);
    }
    setPicked((previous) => [...previous.filter((file) => !(newSong && file.song)), ...accepted.map((entry) => entry.picked)]);

    // A file this browser already finished uploading, and that is still in the library, is used as it is.
    const uploaded = (key: string): boolean => {
      const entry = entryOf(key);
      return entry?.status === "done" && entry.assetId !== null && library.statusOf.has(entry.assetId);
    };
    const toUpload = accepted.filter((entry) => !uploaded(entry.picked.key));
    const handed = (keys: string[]) => setHandedOver((previous) => new Set([...previous, ...keys]));
    handed(accepted.filter((entry) => !toUpload.includes(entry)).map((entry) => entry.picked.key));
    if (toUpload.length === 0) return;
    void commands.addFiles(toUpload.map((entry) => entry.file)).then((result) => {
      // The queue refuses nothing the check above lets through; if it ever does, the file must not stay listed as uploading.
      const refused = new Set(result?.rejected.map((file) => file.name) ?? []);
      handed(toUpload.filter((entry) => !refused.has(entry.picked.name)).map((entry) => entry.picked.key));
      if (refused.size === 0) return;
      setPicked((previous) => previous.filter((file) => !refused.has(file.name)));
      setNotice("That file can't be used. Add a video (MP4 or MOV) or a song (MP3, M4A, WAV).");
    });
  }

  function remove(key: string) {
    stopUpload(key);
    setPicked((previous) => previous.filter((file) => file.key !== key));
    forget(key);
  }

  function retry(key: string) {
    const entry = entryOf(key);
    if (entry?.status === "failed") commands.retry(entry.id);
    // The library's list could not be read: read it again.
    else void assets.refetch();
  }

  return { available: commands.available, clips, song, notice, add, remove, retry };
}
