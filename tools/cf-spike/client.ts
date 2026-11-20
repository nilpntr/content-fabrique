#!/usr/bin/env bun
// cf-spike client: submits the render manifests of manifests/*.json (made from the remote integration test's plan, see README)
// to a runner that serves the job contract (the gateway Worker, or `cf-job serve` anywhere), waits, downloads the result and
// prints one JSON line per job with its timings, frame count, duration and hashes.
//
//   bun client.ts sources                       render the synthetic source clips into the bucket (once)
//   bun client.ts run <case> [--parallel N]     run one case (s10-preview, s10-final, s30-preview, s30-final), N copies at once
//
// Environment: GATEWAY_URL (the Worker: object URLs are always signed for it), GATEWAY_TOKEN, and optionally RUNNER_URL and
// RUNNER_TOKEN to send the jobs somewhere else (a local cf-job serve) while the objects stay in the spike bucket. RUN names the
// result prefix (default: the time). Tokens are read from the environment only and never printed.

import { createHash, createHmac } from "node:crypto";
import { readFileSync } from "node:fs";
import { join } from "node:path";

const GATEWAY_URL = need("GATEWAY_URL").replace(/\/$/, "");
const GATEWAY_TOKEN = need("GATEWAY_TOKEN");
const RUNNER_URL = (process.env.RUNNER_URL ?? GATEWAY_URL).replace(/\/$/, "");
const RUNNER_TOKEN = process.env.RUNNER_TOKEN ?? GATEWAY_TOKEN;
const RUN = process.env.RUN ?? new Date().toISOString().replace(/[-:]/g, "").slice(0, 15);
const TARGET = RUNNER_URL === GATEWAY_URL ? "cloudflare" : "local";
const URL_TTL_S = 3600;
// INTERNAL=1: the job's object URLs go to http://objects.internal, which the gateway intercepts (container without internet).
const OBJECT_BASE = process.env.INTERNAL === "1" ? "http://objects.internal" : GATEWAY_URL;

function need(k: string): string {
	const v = process.env[k];
	if (!v) throw new Error(`${k} is not set`);
	return v;
}

function sign(method: "GET" | "PUT", key: string, base = OBJECT_BASE): string {
	const exp = Math.floor(Date.now() / 1000) + URL_TTL_S;
	const sig = createHmac("sha256", GATEWAY_TOKEN).update(`obj\n${method}\n${key}\n${exp}`).digest("hex");
	return `${base}/obj/${key.split("/").map(encodeURIComponent).join("/")}?m=${method}&exp=${exp}&sig=${sig}`;
}

async function put(key: string, body: string | Uint8Array, contentType: string) {
	const r = await fetch(sign("PUT", key, GATEWAY_URL), { method: "PUT", body, headers: { "content-type": contentType } });
	if (!r.ok) throw new Error(`PUT ${key}: ${r.status} ${await r.text()}`);
}

async function get(key: string): Promise<Uint8Array> {
	const r = await fetch(sign("GET", key, GATEWAY_URL));
	if (!r.ok) throw new Error(`GET ${key}: ${r.status}`);
	return new Uint8Array(await r.arrayBuffer());
}

interface Manifest {
	inputs: { url: string; path: string }[];
	steps: { args: string[] }[];
	outputs: { path: string; url: string; content_type?: string; max_bytes?: number }[];
	limits: { cpu: number; memory_mb: number; timeout_ms: number; pids: number };
	labels?: Record<string, string>;
}

interface Status {
	state: string;
	error?: string;
	exit_code: number;
	duration_ms: number;
	steps: { exit_code: number; stdout: string; stderr: string; duration_ms: number }[];
	outputs?: { path: string; bytes: number }[];
}

const TERMINAL = new Set(["succeeded", "failed", "timed_out", "error", "cancelled"]);

async function submit(id: string, m: Manifest) {
	const auth = { authorization: `Bearer ${RUNNER_TOKEN}` };
	const t0 = performance.now();
	let r: Response;
	for (;;) {
		r = await fetch(`${RUNNER_URL}/jobs/${id}`, { method: "PUT", headers: { ...auth, "content-type": "application/json" }, body: JSON.stringify(m) });
		if (r.status !== 503) break;
		await Bun.sleep(1000 * Number(r.headers.get("retry-after") ?? 2));
	}
	const accepted = performance.now();
	if (r.status >= 300) throw new Error(`PUT /jobs/${id}: ${r.status} ${await r.text()}`);
	const coldHeader = r.headers.get("x-cold-start-ms");
	let st: Status;
	let last: Response;
	for (;;) {
		last = await fetch(`${RUNNER_URL}/jobs/${id}`, { headers: auth });
		if (last.status === 503) {
			await Bun.sleep(1000);
			continue;
		}
		if (!last.ok) throw new Error(`GET /jobs/${id}: ${last.status} ${await last.text()}`);
		st = (await last.json()) as Status;
		if (TERMINAL.has(st.state)) break;
		await Bun.sleep(500);
	}
	const done = performance.now();
	return {
		st,
		timing: {
			put_ms: Math.round(accepted - t0), // includes the container start on Cloudflare
			cold_start_ms: coldHeader ? Number(coldHeader) : null,
			job_ms: st.duration_ms, // cf-job: downloads, steps, uploads
			container_ms: last.headers.get("x-container-ms") ? Number(last.headers.get("x-container-ms")) : null,
			wall_ms: Math.round(done - t0), // submit to terminal status seen by the client
		},
	};
}

// THREADS=n pins the encoder's thread count (inserted before the output's -y): x264's output depends on it, and by default it
// follows the CPUs the container sees (4 on a standard-4, all of the host's under rootless Docker without a cpuset).
function withThreads(args: string[]): string[] {
	const n = process.env.THREADS;
	const flags = process.env.CPUFLAGS; // e.g. "-avx512-avx512icl": ffmpeg's own SIMD paths (not libx264's) without AVX-512
	let out = args;
	if (flags) out = [out[0], "-cpuflags", flags, ...out.slice(1)];
	const extra = [...(n ? ["-threads", n] : []), ...(process.env.X264_ASM ? ["-x264-params", `asm=${process.env.X264_ASM}`] : [])];
	if (!extra.length) return out;
	const i = out.lastIndexOf("-y");
	return [...out.slice(0, i), ...extra, ...out.slice(i)];
}

const b64 = (s: string) => Buffer.from(s ?? "", "base64").toString("utf8");

async function sources() {
	const steps: string[][] = [];
	const outputs: Manifest["outputs"] = [];
	// The integration test's sources (640x360) and portrait 1080x1920 ones for the final-size cases; 16 s each.
	const kinds = [
		{ dir: "sd", size: "640x360", preset: "ultrafast" },
		{ dir: "hd", size: "1080x1920", preset: "veryfast" },
	];
	const tones = [220, 330];
	for (const k of kinds) {
		for (let n = 0; n < 2; n++) {
			const name = `${k.dir}${n}.mp4`;
			steps.push(["ffmpeg", "-nostdin", "-v", "error", "-y", "-f", "lavfi", "-i", `testsrc2=size=${k.size}:rate=30:duration=16`, "-f", "lavfi", "-i", `sine=frequency=${tones[n]}:sample_rate=48000:duration=16`,
				"-c:v", "libx264", "-preset", k.preset, "-pix_fmt", "yuv420p", "-c:a", "aac", `/work/${name}`]);
			outputs.push({ path: `/work/${name}`, url: sign("PUT", `src/${k.dir}/${n}.mp4`), content_type: "video/mp4", max_bytes: 90 << 20 });
		}
	}
	const m: Manifest = { inputs: [], steps: steps.map((args) => ({ args })), outputs, limits: { cpu: 4, memory_mb: 4096, timeout_ms: 600_000, pids: 1024 } };
	const { st, timing } = await submit(`sources-${RUN}-${TARGET}`, m);
	console.log(JSON.stringify({ job: "sources", target: TARGET, state: st.state, error: st.error, timing, steps_ms: st.steps.map((s) => s.duration_ms), outputs: st.outputs, stderr: st.state === "succeeded" ? undefined : st.steps.map((s) => b64(s.stderr).slice(-400)) }));
}

interface Case {
	case: string;
	duration_ms: number;
	total_frames: number;
	uploads?: Record<string, string>;
	inputs: { path: string; key: string }[];
	steps: string[][];
	outputs: { path: string; key: string; content_type: string; max_bytes?: number }[];
	limits: Manifest["limits"];
}

async function runCase(name: string, n: number) {
	const c = JSON.parse(readFileSync(join(import.meta.dir, "manifests", `${name}.json`), "utf8")) as Case;
	for (const [key, body] of Object.entries(c.uploads ?? {})) await put(key, body, "text/plain");
	const one = async (i: number) => {
		const prefix = `${RUN}-${TARGET}-${i}`;
		const outKeys = c.outputs.map((o) => o.key.replace("{run}", prefix));
		const m: Manifest = {
			inputs: c.inputs.map((x) => ({ url: sign("GET", x.key), path: x.path })),
			steps: [
				...c.steps.map((args, k) => ({ args: k === 0 ? withThreads(args) : args })),
				{ args: ["ffmpeg", "-nostdin", "-v", "error", "-i", "/work/out.mp4", "-map", "0:v", "-f", "md5", "-"] },
				{ args: ["ffmpeg", "-nostdin", "-v", "error", "-i", "/work/out.mp4", "-map", "0:a", "-f", "md5", "-"] },
			],
			outputs: c.outputs.map((o, k) => ({ path: o.path, url: sign("PUT", outKeys[k]), content_type: o.content_type, max_bytes: o.max_bytes })),
			limits: c.limits,
			labels: { spike: "cf-spike", case: name },
		};
		const { st, timing } = await submit(`${name}-${prefix}`, m);
		const row: Record<string, unknown> = { job: name, copy: i, target: TARGET, state: st.state, error: st.error, timing, steps_ms: st.steps.map((s) => s.duration_ms) };
		if (st.state === "succeeded") {
			const probe = JSON.parse(b64(st.steps[1].stdout)) as { streams: { codec_type: string; width?: number; height?: number; nb_read_frames?: string }[]; format: { duration: string } };
			const v = probe.streams.find((s) => s.codec_type === "video");
			const file = await get(outKeys[0]);
			Object.assign(row, {
				want: { frames: c.total_frames, duration_ms: c.duration_ms },
				got: { frames: Number(v?.nb_read_frames), width: v?.width, height: v?.height, duration_s: Number(probe.format.duration), bytes: file.byteLength },
				sha256: createHash("sha256").update(file).digest("hex"),
				video_md5: b64(st.steps[3].stdout).trim(),
				audio_md5: b64(st.steps[4].stdout).trim(),
			});
		} else {
			row.stderr = st.steps.map((s) => b64(s.stderr).slice(-400));
		}
		console.log(JSON.stringify(row));
		return row;
	};
	const t0 = performance.now();
	await Promise.all(Array.from({ length: n }, (_, i) => one(i)));
	if (n > 1) console.log(JSON.stringify({ job: name, target: TARGET, parallel: n, batch_wall_ms: Math.round(performance.now() - t0) }));
}

const [cmd, name] = process.argv.slice(2);
const pi = process.argv.indexOf("--parallel");
const parallel = pi > 0 ? Number(process.argv[pi + 1]) : 1;
if (cmd === "sources") await sources();
else if (cmd === "run" && name) await runCase(name, parallel);
else {
	console.error("usage: bun client.ts sources | run <case> [--parallel N]");
	process.exit(2);
}
