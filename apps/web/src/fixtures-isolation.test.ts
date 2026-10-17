import { readdirSync, readFileSync, statSync } from "node:fs";
import path from "node:path";
import { expect, test } from "vitest";

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
