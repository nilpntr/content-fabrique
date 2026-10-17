import { mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { expect, test } from "vitest";
import { findLeaks } from "../scripts/check-dist-clean";

/**
 * The fixtures viewer (`bun run fixtures`, the dev-fixtures folder) signs in with a stand-in and answers the API with canned data. It
 * must never be part of what ships. It is a separate Vite app: the product's build (vite.config.ts, bundling `src` from the routes)
 * only includes what the routes import, so these tests check that nothing in the product reaches the viewer, that the viewer refuses to
 * run outside `vite dev`, and that it keeps off the ports of the real stack.
 */
const web = path.resolve(import.meta.dirname, "..");
const read = (file: string) => readFileSync(path.join(web, file), "utf8");

function sources(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const full = path.join(dir, name);
    return statSync(full).isDirectory() ? sources(full) : /\.(ts|tsx|css)$/.test(name) ? [full] : [];
  });
}

test("nothing in the product imports or names the fixtures viewer", () => {
  const offenders = sources(path.join(web, "src"))
    // This file names the viewer to check for it, and the tests' own kit (test-clerk, test-fixtures) is what the viewer borrows, not the other way round.
    .filter((file) => !file.endsWith("fixtures-isolation.test.ts"))
    .filter((file) => /dev-fixtures|vite\.fixtures/.test(readFileSync(file, "utf8")));
  expect(offenders).toEqual([]);
});

test("no file of the product imports the test kit (the Clerk stand-in, the fixtures, the fake API)", () => {
  // The kit's own files, the test setup and the tests may use it; nothing else under src may.
  const kit = new Set(["test-api.tsx", "test-clerk.tsx", "test-fixtures.ts", "test-setup.ts"]);
  const offenders = sources(path.join(web, "src"))
    .filter((file) => !kit.has(path.basename(file)) && !/\.test\.[tj]sx?$/.test(file))
    .filter((file) => /["'][^"'\n]*\btest-(clerk|fixtures|api)["']/.test(readFileSync(file, "utf8")))
    .map((file) => path.relative(web, file));
  expect(offenders).toEqual([]);
});

test("the leak check of the build finds the kit's names in any text file and refuses an empty build", () => {
  const dir = mkdtempSync(path.join(tmpdir(), "cf-dist-"));
  try {
    expect(() => findLeaks(dir)).toThrow(/no files to check/);
    mkdirSync(path.join(dir, "assets"));
    writeFileSync(path.join(dir, "assets", "app.js"), "export const a = 1;");
    expect(findLeaks(dir)).toEqual([]);
    writeFileSync(path.join(dir, "assets", "leak.js"), 'const t = "test-token"; window.clerkMock = 1; fetch("http://api.fixtures/v1/me");');
    expect(findLeaks(dir).map((leak) => leak.needle).sort()).toEqual(["api.fixtures", "clerkMock", "test-token"]);
    // The binary files of a build are not read.
    writeFileSync(path.join(dir, "font.woff2"), "clerkMock");
    expect(findLeaks(dir).some((leak) => leak.file.endsWith("font.woff2"))).toBe(false);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("CI runs the leak check after the build", () => {
  const ci = readFileSync(path.resolve(web, "../../.github/workflows/ci.yml"), "utf8");
  expect(ci.indexOf("@cf/web build")).toBeGreaterThan(-1);
  expect(ci.indexOf("@cf/web check:dist")).toBeGreaterThan(ci.indexOf("@cf/web build"));
  expect((JSON.parse(read("package.json")) as { scripts: Record<string, string> }).scripts["check:dist"]).toBe("bun scripts/check-dist-clean.ts");
});

test("the product's build config, its routes, its deploy config and its scripts do not know the viewer", () => {
  for (const file of ["vite.config.ts", "vite.base.ts", "wrangler.jsonc", "src/routeTree.gen.ts", "src/router.tsx", "src/routes/__root.tsx"]) {
    expect(read(file), file).not.toMatch(/fixtures/i);
  }
  const scripts = (JSON.parse(read("package.json")) as { scripts: Record<string, string> }).scripts;
  for (const name of ["dev", "build", "preview", "check"]) expect(scripts[name], name).not.toMatch(/fixtures/);
  expect(scripts.fixtures).toBe("vite --config vite.fixtures.config.ts");
});

test("the viewer's entry refuses to run outside the dev server", () => {
  expect(read("dev-fixtures/main.tsx")).toMatch(/if \(!import\.meta\.env\.DEV\) throw new Error/);
});

test("the viewer stays off the ports of the real stack", () => {
  const config = read("vite.fixtures.config.ts");
  expect(config).toMatch(/port: 3100, strictPort: true/);
  for (const port of ["3000", "8080", "8081", "8082"]) expect(config).not.toContain(`port: ${port}`);
});

test("the viewer's stand-ins hold no secret and read no environment", () => {
  for (const file of sources(path.join(web, "dev-fixtures"))) {
    const text = readFileSync(file, "utf8");
    expect(text, file).not.toMatch(/process\.env|\.dev\.vars|SECRET|VITE_[A-Z_]*KEY/);
  }
});
