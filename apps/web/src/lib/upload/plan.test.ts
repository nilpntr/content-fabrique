import { describe, expect, test } from "vitest";
import { checkPlan, MAX_FILE_BYTES, partCountOf, partRange, partSizeOf } from "./plan";
import { backoffMs } from "./timing";
import { formatBytes, formatEta, formatRate } from "./format";
import { contentTypeOf, fileKey, rejectionOf } from "./files";

const MiB = 1024 * 1024;
const PART = 16 * MiB; // internal/assets DefaultPartSize

describe("the part plan (internal/assets PartPlan and PartSizeOf)", () => {
  test("every part but the last has the part size, the last one has the rest", () => {
    const size = 50 * MiB;
    const count = partCountOf(size, PART);
    expect(count).toBe(4);
    expect([1, 2, 3, 4].map((n) => partSizeOf(size, PART, count, n))).toEqual([PART, PART, PART, 2 * MiB]);
  });

  test("a file that is an exact multiple has a full last part", () => {
    expect(partCountOf(32 * MiB, PART)).toBe(2);
    expect(partSizeOf(32 * MiB, PART, 2, 2)).toBe(PART);
  });

  test("one byte is one part of one byte", () => {
    expect(partCountOf(1, PART)).toBe(1);
    expect(partSizeOf(1, PART, 1, 1)).toBe(1);
  });

  test("the biggest file (20 GiB) has 1,280 parts, within S3's 10,000", () => {
    const count = partCountOf(MAX_FILE_BYTES, PART);
    expect(count).toBe(1280);
    expect(partSizeOf(MAX_FILE_BYTES, PART, count, count)).toBe(PART);
  });

  test("the ranges are contiguous and cover the file exactly", () => {
    for (const size of [1, PART - 1, PART, PART + 1, 5 * PART + 123]) {
      const count = partCountOf(size, PART);
      let expectedStart = 0;
      for (let n = 1; n <= count; n++) {
        const { start, end } = partRange(size, PART, count, n);
        expect(start).toBe(expectedStart);
        expect(end).toBeGreaterThan(start);
        expectedStart = end;
      }
      expect(expectedStart).toBe(size);
    }
  });

  test("a plan that does not fit the file is refused", () => {
    expect(checkPlan(50 * MiB, PART, 4)).toBe(true);
    expect(checkPlan(50 * MiB, PART, 5)).toBe(false); // an empty last part
    expect(checkPlan(50 * MiB, PART, 3)).toBe(false); // bytes left over
    expect(checkPlan(50 * MiB, 0, 4)).toBe(false);
  });
});

describe("backoffMs", () => {
  test("grows exponentially, with jitter between half and all of it, up to the cap", () => {
    expect(backoffMs(0, 0)).toBe(250);
    expect(backoffMs(0, 1)).toBe(500);
    expect(backoffMs(3, 0)).toBe(2000);
    expect(backoffMs(3, 1)).toBe(4000);
    expect(backoffMs(20, 1)).toBe(30_000);
    expect(backoffMs(20, 0)).toBe(15_000);
    for (let attempt = 0; attempt < 10; attempt++) {
      expect(backoffMs(attempt + 1, 0.5)).toBeGreaterThanOrEqual(backoffMs(attempt, 0.5));
    }
  });
});

describe("files", () => {
  test("the media type is the browser's when the API takes it, else told by the extension", () => {
    expect(contentTypeOf({ name: "a.mp4", type: "video/mp4" })).toBe("video/mp4");
    expect(contentTypeOf({ name: "a.MOV", type: "" })).toBe("video/quicktime");
    expect(contentTypeOf({ name: "IMG.heic", type: "" })).toBe("image/heic");
    expect(contentTypeOf({ name: "song.mp3", type: "audio/mpeg" })).toBe("audio/mpeg");
    expect(contentTypeOf({ name: "a.mkv", type: "video/x-matroska" })).toBeNull();
    expect(contentTypeOf({ name: "a.txt", type: "text/plain" })).toBeNull();
    expect(contentTypeOf({ name: "noextension", type: "" })).toBeNull();
  });

  test("empty and oversized files are refused with a reason", () => {
    expect(rejectionOf({ name: "a.mp4", type: "video/mp4", size: 0 })).toMatch(/empty/);
    expect(rejectionOf({ name: "a.mp4", type: "video/mp4", size: MAX_FILE_BYTES + 1 })).toMatch(/20 GB/);
    expect(rejectionOf({ name: "a.mp4", type: "video/mp4", size: MAX_FILE_BYTES })).toBeNull();
  });

  test("a file's identity is its name, size and date", () => {
    const a = { name: "a.mp4", size: 1, lastModified: 2 };
    expect(fileKey(a)).toBe(fileKey({ ...a }));
    expect(fileKey(a)).not.toBe(fileKey({ ...a, lastModified: 3 }));
    expect(fileKey(a)).not.toBe(fileKey({ ...a, size: 2 }));
    expect(fileKey(a)).not.toBe(fileKey({ ...a, name: "b.mp4" }));
  });
});

describe("format", () => {
  test("bytes, rates and times", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(1536)).toBe("1.50 KB");
    expect(formatBytes(50 * MiB)).toBe("50.0 MB");
    expect(formatBytes(3 * 1024 ** 3)).toBe("3.00 GB");
    expect(formatRate(5 * MiB)).toBe("5.00 MB/s");
    expect(formatEta(null)).toBe("");
    expect(formatEta(0.2)).toBe("1 s");
    expect(formatEta(59)).toBe("59 s");
    expect(formatEta(61)).toBe("2 min");
    expect(formatEta(3600 + 61)).toBe("1 h 02 min");
  });
});
