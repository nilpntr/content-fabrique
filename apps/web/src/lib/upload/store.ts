/**
 * What survives a reload: one record per upload that is not finished, in IndexedDB (a few hundred bytes each, never the
 * file). A browser cannot keep the file itself, so after a reload the record waits for the user to hand the same file
 * over again (or, where the browser allows it, a stored file handle gives it back). The server is the truth about which
 * parts are uploaded; `bytesDone` is only what the tray shows while it waits.
 */

export interface UploadRecord {
  /** Local id (a UUID). */
  id: string;
  /** `userId:orgId`: uploads of one workspace never show up in another. */
  workspace: string;
  /** The server's asset id, once the upload was created. */
  assetId: string | null;
  /** Whether a create call may have reached the server without us hearing back: look for its asset before creating again. */
  createStarted: boolean;
  name: string;
  size: number;
  lastModified: number;
  type: string;
  partSize: number | null;
  partCount: number | null;
  /** Bytes known to be uploaded when the record was last saved. */
  bytesDone: number;
  /** Why the upload failed (it stays in the list until retried or dismissed). */
  error: { code: string; message: string } | null;
  createdAt: number;
  /** Chrome's File System Access handle of the file, to read it again after a reload without asking. */
  handle?: unknown;
}

export interface UploadStore {
  list(workspace: string): Promise<UploadRecord[]>;
  put(record: UploadRecord): Promise<void>;
  delete(id: string): Promise<void>;
}

export function createMemoryStore(): UploadStore & { records: Map<string, UploadRecord> } {
  const records = new Map<string, UploadRecord>();
  return {
    records,
    async list(workspace) {
      return [...records.values()].filter((record) => record.workspace === workspace).map((record) => ({ ...record }));
    },
    async put(record) {
      records.set(record.id, { ...record });
    },
    async delete(id) {
      records.delete(id);
    },
  };
}

const DB_NAME = "cf-uploads";
const STORE_NAME = "uploads";

function request<T>(req: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
}

/** IndexedDB, hand-rolled: one object store keyed by `id`, indexed by workspace. */
export function createIndexedDbStore(factory: IDBFactory = indexedDB): UploadStore {
  let opened: Promise<IDBDatabase> | undefined;
  const db = (): Promise<IDBDatabase> => {
    opened ??= new Promise<IDBDatabase>((resolve, reject) => {
      const open = factory.open(DB_NAME, 1);
      open.onupgradeneeded = () => {
        const store = open.result.createObjectStore(STORE_NAME, { keyPath: "id" });
        store.createIndex("workspace", "workspace");
      };
      open.onsuccess = () => resolve(open.result);
      open.onerror = () => {
        opened = undefined;
        reject(open.error);
      };
    });
    return opened;
  };
  const store = async (mode: IDBTransactionMode) => (await db()).transaction(STORE_NAME, mode).objectStore(STORE_NAME);
  return {
    async list(workspace) {
      return (await request((await store("readonly")).index("workspace").getAll(workspace))) as UploadRecord[];
    },
    async put(record) {
      try {
        await request((await store("readwrite")).put(record));
      } catch (error) {
        // A handle that cannot be cloned must not cost the record.
        if (record.handle !== undefined && error instanceof DOMException && error.name === "DataCloneError") {
          await request((await store("readwrite")).put({ ...record, handle: undefined }));
          return;
        }
        throw error;
      }
    },
    async delete(id) {
      await request((await store("readwrite")).delete(id));
    },
  };
}

/** IndexedDB where there is one, else memory (private windows of some browsers, tests): uploads then do not survive a reload. */
export function createUploadStore(): UploadStore {
  try {
    if (typeof indexedDB !== "undefined") return createIndexedDbStore();
  } catch {
    // fall through
  }
  return createMemoryStore();
}
