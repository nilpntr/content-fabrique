import { MAX_FILE_BYTES } from "./plan";

/**
 * Which files the library takes, how a file is told from the same file again after a reload, and how the browser hands
 * files over: a picker (with file handles where the browser has them) or a drop.
 */

/** The `accept` attribute of the file input: what the API takes (video/mp4, quicktime, webm, audio/*, jpeg, png, heic). */
export const ACCEPT = "video/mp4,video/quicktime,video/webm,audio/*,image/jpeg,image/png,image/heic,.mp4,.mov,.webm,.m4a,.mp3,.wav,.aac,.ogg,.flac,.jpg,.jpeg,.png,.heic";

const BY_EXTENSION: Readonly<Record<string, string>> = {
  mp4: "video/mp4",
  mov: "video/quicktime",
  webm: "video/webm",
  m4a: "audio/mp4",
  mp3: "audio/mpeg",
  wav: "audio/wav",
  aac: "audio/aac",
  ogg: "audio/ogg",
  flac: "audio/flac",
  jpg: "image/jpeg",
  jpeg: "image/jpeg",
  png: "image/png",
  heic: "image/heic",
};
const ACCEPTED_EXACT = new Set(["video/mp4", "video/quicktime", "video/webm", "image/jpeg", "image/png", "image/heic"]);
const AUDIO = /^audio\/[a-z0-9][a-z0-9.+_-]{0,100}$/;

/** The media type to upload a file as, or null when the API would refuse it. Browsers leave `type` empty for some files (HEIC, MOV), so the extension is the fallback. */
export function contentTypeOf(file: Pick<File, "name" | "type">): string | null {
  const declared = file.type.split(";")[0]?.trim().toLowerCase() ?? "";
  if (ACCEPTED_EXACT.has(declared) || AUDIO.test(declared)) return declared;
  if (declared !== "" && declared !== "application/octet-stream") return null;
  const extension = file.name.slice(file.name.lastIndexOf(".") + 1).toLowerCase();
  return file.name.includes(".") ? (BY_EXTENSION[extension] ?? null) : null;
}

/** Why a file cannot be uploaded, or null when it can. */
export function rejectionOf(file: Pick<File, "name" | "type" | "size">): string | null {
  if (file.size <= 0) return "The file is empty.";
  if (file.size > MAX_FILE_BYTES) return "Files can be 20 GB at most.";
  if (contentTypeOf(file) === null) return "Only video, audio and image files (MP4, MOV, WebM, MP3, M4A, WAV, JPEG, PNG, HEIC) can be uploaded.";
  return null;
}

/** What tells a file from another, and the same file from itself after a reload. Browsers give no stable handle, so this is it. */
export function fileKey(file: { name: string; size: number; lastModified: number }): string {
  return `${file.name}\u0000${file.size}\u0000${file.lastModified}`;
}

/** A file and, where the browser gave one, its File System Access handle (which survives a reload in IndexedDB). */
export interface PickedFile {
  file: File;
  handle?: unknown;
}

interface FileHandleLike {
  getFile(): Promise<File>;
  queryPermission?(descriptor: { mode: "read" }): Promise<"granted" | "denied" | "prompt">;
}

/** The file behind a stored handle, when the browser still lets the page read it without asking. */
export async function readHandle(handle: unknown): Promise<File | null> {
  const h = handle as FileHandleLike | undefined;
  if (!h || typeof h.getFile !== "function") return null;
  try {
    if (h.queryPermission && (await h.queryPermission({ mode: "read" })) !== "granted") return null;
    return await h.getFile();
  } catch {
    return null;
  }
}

type OpenFilePicker = (options: { multiple: boolean; types?: { description: string; accept: Record<string, string[]> }[]; excludeAcceptAllOption?: boolean }) => Promise<FileHandleLike[]>;

/** Whether the browser has `showOpenFilePicker` (Chromium), which returns handles that outlive a reload. */
export function hasFilePicker(): boolean {
  return typeof window !== "undefined" && typeof (window as unknown as { showOpenFilePicker?: unknown }).showOpenFilePicker === "function";
}

/**
 * Opens the browser's file picker with handles. Returns null when the picker is not available (use an `<input>`), and an
 * empty list when the user closed it. Must run inside a click, like any picker.
 */
export async function pickWithHandles(): Promise<PickedFile[] | null> {
  if (!hasFilePicker()) return null;
  const open = (window as unknown as { showOpenFilePicker: OpenFilePicker }).showOpenFilePicker;
  let handles: FileHandleLike[];
  try {
    handles = await open.call(window, {
      multiple: true,
      types: [
        {
          description: "Video, audio and images",
          accept: {
            "video/mp4": [".mp4"],
            "video/quicktime": [".mov"],
            "video/webm": [".webm"],
            "audio/*": [".m4a", ".mp3", ".wav", ".aac", ".ogg", ".flac"],
            "image/jpeg": [".jpg", ".jpeg"],
            "image/png": [".png"],
            "image/heic": [".heic"],
          },
        },
      ],
    });
  } catch (error) {
    if (error instanceof DOMException && error.name === "AbortError") return [];
    return null; // blocked (an iframe, a policy): fall back to the input
  }
  return Promise.all(handles.map(async (handle) => ({ file: await handle.getFile(), handle })));
}

/** The files of a drop, with their handles where the browser can give them. Folders are skipped. */
export async function filesFromDrop(data: DataTransfer): Promise<PickedFile[]> {
  const items = [...data.items].filter((item) => item.kind === "file");
  if (items.length === 0) return [...data.files].map((file) => ({ file }));
  const picked = await Promise.all(
    items.map(async (item): Promise<PickedFile | null> => {
      // getAsFileSystemHandle and getAsFile are both read before the first await in this tick is over for every item.
      const withHandle = (item as unknown as { getAsFileSystemHandle?: () => Promise<{ kind: string; getFile?: () => Promise<File> } | null> }).getAsFileSystemHandle;
      const file = item.getAsFile();
      if (withHandle) {
        try {
          const handle = await withHandle.call(item);
          if (handle?.kind === "directory") return null;
          if (handle?.kind === "file" && file) return { file, handle };
        } catch {
          // no handle: the plain file will do
        }
      }
      return file ? { file } : null;
    }),
  );
  return picked.filter((p): p is PickedFile => p !== null);
}
