/**
 * Fails when the production build holds anything from the test kit or the fixtures viewer: the Clerk stand-in, its token, the
 * viewer's fake API. Run it after `bun run build` (the web CI job does): `bun apps/web/scripts/check-dist-clean.ts [dist]`.
 *
 * It reads every text file of the build, the server bundle included, because a stand-in that reached the Worker is as bad as one
 * that reached the browser.
 */
import { readdirSync, readFileSync, statSync } from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";

/** Names that exist only in the test kit and the fixtures viewer. */
export const FORBIDDEN = ["clerkMock", "test-token", "api.fixtures", "dev-fixtures", "installFakeApi", "installWindowClerk"] as const;

const TEXT = /\.(js|mjs|cjs|css|html|json|map|txt|svg)$/;

function files(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const full = path.join(dir, name);
    return statSync(full).isDirectory() ? files(full) : TEXT.test(name) ? [full] : [];
  });
}

/** The files under `dir` that contain a forbidden name, with the name. Throws when `dir` has no files: an empty build checks nothing. */
export function findLeaks(dir: string, needles: readonly string[] = FORBIDDEN): { file: string; needle: string }[] {
  const all = files(dir);
  if (all.length === 0) throw new Error(`${dir} has no files to check: run the build first`);
  return all.flatMap((file) => {
    const text = readFileSync(file, "utf8");
    return needles.filter((needle) => text.includes(needle)).map((needle) => ({ file, needle }));
  });
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const dist = path.resolve(process.argv[2] ?? path.join(import.meta.dirname, "../dist"));
  const leaks = findLeaks(dist);
  if (leaks.length > 0) {
    for (const leak of leaks) console.error(`${path.relative(dist, leak.file)} contains "${leak.needle}"`);
    process.exit(1);
  }
  console.log(`${dist}: no test kit or fixtures viewer code (${files(dist).length} files checked).`);
}
