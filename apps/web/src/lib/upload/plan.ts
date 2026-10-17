/**
 * The part plan of an upload. The server decides it (`upload.part_size`, `upload.part_count`) and signs every part URL for
 * an exact Content-Length, so the client must cut the file exactly like this: parts 1..count-1 hold `partSize` bytes each
 * and the last one holds the rest (internal/assets PartSizeOf). {@link checkPlan} refuses a plan that does not fit the
 * file, which would otherwise upload for minutes and then fail to complete.
 */

/** The most bytes a file may have (the API's limit: 20 GiB). */
export const MAX_FILE_BYTES = 20 * 1024 ** 3;

/** The exact size of part `n` (1-based) of a file of `size` bytes. */
export function partSizeOf(size: number, partSize: number, partCount: number, n: number): number {
  return n < partCount ? partSize : size - (partCount - 1) * partSize;
}

/** The byte range `[start, end)` of part `n` (1-based). */
export function partRange(size: number, partSize: number, partCount: number, n: number): { start: number; end: number } {
  const start = (n - 1) * partSize;
  return { start, end: start + partSizeOf(size, partSize, partCount, n) };
}

/** How many parts a file of `size` bytes has when cut every `partSize` bytes. */
export function partCountOf(size: number, partSize: number): number {
  return Math.ceil(size / partSize);
}

/** Whether (partSize, partCount) is a valid cut of a file of `size` bytes: every part has at least one byte. */
export function checkPlan(size: number, partSize: number, partCount: number): boolean {
  return Number.isInteger(partSize) && partSize > 0 && partCount === partCountOf(size, partSize) && partCount >= 1;
}
