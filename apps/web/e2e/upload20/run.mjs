// The browser half of the acceptance proof of P1-02: 20 files uploaded at once through apps/web/src/lib/upload, in real
// Chrome, on a throttled connection (CDP Network.emulateNetworkConditions), with a network blip and a reload half way,
// then resumed. The server half is apps/server/internal/auth/upload20_test.go (build tag upload20), which verifies every
// object in MinIO. See that file for how to start it; then:
//
//   [BLIP_MS=6000] node apps/web/e2e/upload20/run.mjs <dir> [count=20] [sizeMiB=50] [rateMiBps=5]
//
// <dir> is the UPLOAD20_DIR of the Go test. Needs playwright-core (install it outside the repo: `npm i playwright-core`
// in a scratch directory and set PW_DIR to that directory) and Google Chrome.
import { createHash, randomBytes } from "node:crypto";
import { createWriteStream, existsSync, mkdirSync, readFileSync, utimesSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join, resolve } from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { once } from "node:events";

const here = dirname(fileURLToPath(import.meta.url));
const [dirArg, countArg = "20", sizeArg = "50", rateArg = "5"] = process.argv.slice(2);
if (!dirArg) throw new Error("usage: run.mjs <dir> [count] [sizeMiB] [rateMiBps]");
const dir = resolve(dirArg);
const COUNT = Number(countArg);
const SIZE = Number(sizeArg) * 1024 * 1024;
const RATE = Number(rateArg) * 1024 * 1024;
const { chromium } = createRequire(join(process.env.PW_DIR ?? "/tmp/cf-tools", "package.json"))("playwright-core");
const log = (...a) => console.log(`[${((Date.now() - t0) / 1000).toFixed(1).padStart(6)}s]`, ...a);
const t0 = Date.now();
const MiB = 1024 * 1024;

// 1. The files: pseudo-random, each different, with a size that is not a multiple of the part size.
mkdirSync(join(dir, "files"), { recursive: true });
const expected = [];
for (let i = 0; i < COUNT; i++) {
  const name = `clip-${String(i + 1).padStart(2, "0")}.mp4`;
  const path = join(dir, "files", name);
  const size = SIZE + i * 1237;
  const hash = createHash("sha256");
  const out = createWriteStream(path);
  for (let written = 0; written < size; ) {
    const chunk = randomBytes(Math.min(4 * MiB, size - written));
    hash.update(chunk);
    if (!out.write(chunk)) await once(out, "drain");
    written += chunk.length;
  }
  out.end();
  await once(out, "finish");
  const mtime = new Date(1_700_000_000_000 + i * 60_000);
  utimesSync(path, mtime, mtime);
  expected.push({ name, size, sha256: hash.digest("hex"), path });
}
writeFileSync(join(dir, "expected.json"), JSON.stringify(expected.map(({ path: _p, ...e }) => e)));
const totalBytes = expected.reduce((n, f) => n + f.size, 0);
log(`generated ${COUNT} files, ${(totalBytes / MiB).toFixed(0)} MiB in total`);

// 2. The page: harness.ts bundled for the browser.
const bundle = spawnSync("bun", ["build", join(here, "harness.ts"), "--outfile", join(dir, "harness.js"), "--target", "browser"], { cwd: resolve(here, "../.."), encoding: "utf8" });
if (bundle.status !== 0) throw new Error(`bun build failed: ${bundle.stderr}${bundle.stdout}`);
const js = readFileSync(join(dir, "harness.js"));
const html = '<!doctype html><meta charset="utf-8"><title>upload20</title><input id="files" type="file" multiple><script type="module" src="/harness.js"></script>';

// 3. The API and MinIO of the Go test.
while (!existsSync(join(dir, "env.json"))) await new Promise((r) => setTimeout(r, 500));
const env = JSON.parse(readFileSync(join(dir, "env.json"), "utf8"));
log(`API ${env.api}, page origin ${env.origin}`);

const browser = await chromium.launch({ executablePath: "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", headless: true,
  // The page is served from a fake origin by Playwright and talks to 127.0.0.1: switch off Chrome's local network access checks.
  args: ["--disable-features=LocalNetworkAccessChecks,LocalNetworkAccess,PrivateNetworkAccessSendPreflights,BlockInsecurePrivateNetworkRequests"],
});
const context = await browser.newContext();
const page = await context.newPage();
page.on("pageerror", (e) => log("PAGE ERROR", e.message));
const seen = new Set();
page.on("console", (m) => {
  const text = m.text().slice(0, 200);
  if ((m.type() === "error" || m.type() === "warning") && !seen.has(text)) {
    seen.add(text);
    log(`console.${m.type()}:`, text);
  }
});
await page.addInitScript((cfg) => {
  window.__CFG__ = cfg;
}, { api: env.api, token: env.token });
await page.route(`${env.origin}/**`, (route) => {
  const { pathname } = new URL(route.request().url());
  if (pathname === "/harness.js") return route.fulfill({ contentType: "text/javascript", body: js });
  if (pathname === "/") return route.fulfill({ contentType: "text/html", body: html });
  return route.fulfill({ status: 404, body: "" });
});

// Count what the page asks of the API.
const calls = { create: 0, state: 0, presign: 0, complete: 0, abort: 0, list: 0, other: 0 };
const apiStatuses = {};
page.on("response", (res) => {
  const url = new URL(res.url());
  if (res.url().startsWith(env.api)) {
    const m = res.request().method();
    const p = url.pathname;
    if (m === "OPTIONS") return;
    const kind = m === "POST" && p === "/v1/assets" ? "create" : p.endsWith("/parts") ? "presign" : p.endsWith("/complete") ? "complete" : p.endsWith("/upload") ? (m === "DELETE" ? "abort" : "state") : p === "/v1/assets" ? "list" : "other";
    calls[kind]++;
    const key = `${kind} ${res.status()}`;
    apiStatuses[key] = (apiStatuses[key] ?? 0) + 1;
  }
});

const cdp = await context.newCDPSession(page);
await cdp.send("Network.enable");
const throttle = (offline) => cdp.send("Network.emulateNetworkConditions", { offline, latency: 40, downloadThroughput: RATE, uploadThroughput: RATE });
await throttle(false);
log(`throttled to ${(RATE / MiB).toFixed(1)} MiB/s (${((RATE * 8) / 1e6).toFixed(0)} Mbit/s) up and down, 40 ms latency`);

const snapshot = () => page.evaluate(() => window.cf.snapshot());
const stats = () => page.evaluate(() => ({ ...window.cf.stats }));
const open = async () => {
  await page.goto(`${env.origin}/`);
  await page.waitForFunction(() => window.cfReady === true);
};
await open();

const paths = expected.map((f) => f.path);
await page.setInputFiles("#files", paths);
await page.waitForFunction(() => window.lastAdd !== undefined);
log("added", JSON.stringify(await page.evaluate(() => window.lastAdd)));

// 4. Run, with a blip at 25% and a reload at 45%.
const series = [];
let blipped = false;
let reloaded = false;
let statsBeforeReload = null;
let resumeInfo = null;
let finishedBeforeReload = 0; // uploads that completed before the reload: no longer in the queue afterwards
const deadline = Date.now() + 50 * 60_000;
for (;;) {
  const { summary, items } = await snapshot();
  const pct = summary.bytesDone / summary.bytesTotal;
  series.push({ t: (Date.now() - t0) / 1000, bytesDone: summary.bytesDone, speed: summary.speed, active: summary.active, queued: summary.queued, done: summary.done, failed: summary.failed });
  const line = `${(pct * 100).toFixed(1).padStart(5)}%  done ${summary.done}/${summary.total}  active ${summary.active} queued ${summary.queued} failed ${summary.failed}  ${(summary.speed / MiB).toFixed(2)} MiB/s`;
  if (series.length % 5 === 1) log(line);
  if (summary.failed > 0) {
    log("FAILED items:", JSON.stringify(items.filter((i) => i.status === "failed").map((i) => [i.name, i.error])));
  }
  if (summary.done + finishedBeforeReload === COUNT && summary.total === summary.done) break;
  if (Date.now() > deadline) throw new Error("timed out");

  if (!blipped && pct >= 0.25) {
    blipped = true;
    const BLIP_MS = Number(process.env.BLIP_MS ?? 6000);
    log(`network blip: offline for ${BLIP_MS / 1000} s`);
    await throttle(true);
    await new Promise((r) => setTimeout(r, BLIP_MS));
    await throttle(false);
    log("network back");
  }
  if (!reloaded && pct >= 0.45) {
    reloaded = true;
    statsBeforeReload = await stats();
    log(`reloading the page at ${(pct * 100).toFixed(1)}%: ${summary.done} done, ${summary.active} uploading, ${summary.queued} queued; ${statsBeforeReload.inflight} PUTs in flight die with the page`);
    await open();
    const after = await snapshot();
    log(`after the reload: ${JSON.stringify(after.summary)}`);
    if (after.summary.needsFile + after.summary.done < 1) throw new Error("the stored queue did not come back");
    await page.evaluate(() => {
      window.lastAdd = undefined;
    });
    // The user picks the same files again (the done ones are no longer in the queue: they would upload twice, so they are not picked).
    const pending = new Set(after.items.filter((i) => i.status === "needs-file").map((i) => i.name));
    await page.setInputFiles("#files", expected.filter((f) => pending.has(f.name)).map((f) => f.path));
    await page.waitForFunction(() => window.lastAdd !== undefined);
    finishedBeforeReload = COUNT - after.summary.total;
    resumeInfo = { needsFileAfterReload: after.summary.needsFile, alreadyFinishedBeforeReload: COUNT - after.summary.total, reselected: pending.size, addResult: await page.evaluate(() => window.lastAdd) };
    log("re-added the files:", JSON.stringify(resumeInfo));
  }
  await new Promise((r) => setTimeout(r, 1000));
}
const wall = (Date.now() - t0) / 1000;
const finalStats = await stats();
log(`all ${COUNT} uploads report done; writing browser-done`);
writeFileSync(join(dir, "browser-done"), "1");

// 5. The numbers.
const putsBytes = (statsBeforeReload?.bytesOk ?? 0) + finalStats.bytesOk;
const summary = {
  files: COUNT,
  total_mib: +(totalBytes / MiB).toFixed(1),
  throttle_mibps: RATE / MiB,
  wall_seconds_incl_generation_and_reload: +wall.toFixed(1),
  mean_throughput_mibps_over_run: +(totalBytes / MiB / ((series.at(-1).t - series.find((s) => s.bytesDone > 0).t) || 1)).toFixed(2),
  peak_inflight_puts_before_reload: statsBeforeReload?.peakInflight,
  peak_inflight_puts_after_reload: finalStats.peakInflight,
  put_ok_before_reload: statsBeforeReload?.putOk,
  put_failed_before_reload: statsBeforeReload?.putFailed,
  put_failures_before_reload: statsBeforeReload?.failures,
  put_ok_after_reload: finalStats.putOk,
  put_failed_after_reload: finalStats.putFailed,
  put_failures_after_reload: finalStats.failures,
  bytes_in_completed_puts_mib: +(putsBytes / MiB).toFixed(1),
  reupload_overhead_percent: +(((putsBytes - totalBytes) / totalBytes) * 100).toFixed(2),
  api_calls: calls,
  api_statuses: apiStatuses,
  resume: resumeInfo,
};
writeFileSync(join(dir, "browser-report.json"), JSON.stringify({ summary, series }, null, 1));
console.log(JSON.stringify(summary, null, 2));
await browser.close();
