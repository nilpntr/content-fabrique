// cf-spike gateway: the job contract of docs/design/render-runtime.md (section 3) on Cloudflare, for the spike only.
//
//   PUT    /jobs/{id}   start a container for the job and hand it the manifest (idempotent per id)
//   GET    /jobs/{id}   the job's status (cf-job's Status JSON); a finished job's container is destroyed
//   DELETE /jobs/{id}   stop the job and forget it
//   GET|PUT /obj/{key}?m=&exp=&sig=   signed object URLs over the R2 binding (stand-in for R2 S3 presigned URLs, see README)
//   DELETE /objects     delete every object in the bucket (teardown: a bucket must be empty to be deleted)
//   GET    /healthz
//
// One Durable Object per job id (EU jurisdiction) supervises one container: it starts it, waits for cf-job's health endpoint,
// forwards the manifest with a per-job token, keeps the final status, destroys the container when the job ends, and an alarm
// destroys it at the deadline whatever the client does.

import { DurableObject, WorkerEntrypoint } from "cloudflare:workers";

interface Env {
	MEDIA_JOB: DurableObjectNamespace<MediaJob>;
	BUCKET: R2Bucket;
	GATEWAY_TOKEN: string;
}

const ID_RE = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;
const PORT = 8080;
// The spike's own ceiling on a job, whatever the manifest asks: the alarm destroys the container after it.
const MAX_JOB_MS = 10 * 60 * 1000;
// The host of object URLs a container reaches through the outbound intercept instead of the internet.
const INTERNAL_HOST = "objects.internal";
const TERMINAL = new Set(["succeeded", "failed", "timed_out", "error", "cancelled"]);

export default {
	async fetch(req: Request, env: Env): Promise<Response> {
		const url = new URL(req.url);
		if (url.pathname === "/healthz") return new Response("ok\n");
		if (url.pathname.startsWith("/obj/")) return objects(req, env, url);
		if (url.pathname === "/objects" && req.method === "DELETE") {
			if (!(await authorized(req, env))) return json({ error: "unauthorized" }, 401);
			return purge(env);
		}
		const m = url.pathname.match(/^\/jobs\/([^/]+)$/);
		if (!m) return json({ error: "not found" }, 404);
		if (!(await authorized(req, env))) return json({ error: "unauthorized" }, 401);
		const id = decodeURIComponent(m[1]);
		if (!ID_RE.test(id)) return json({ error: "invalid job id" }, 400);
		const ns = env.MEDIA_JOB.jurisdiction("eu");
		return ns.get(ns.idFromName(id)).fetch(req);
	},
} satisfies ExportedHandler<Env>;

// ObjectProxy serves the intercepted object requests of a container started without internet (see MediaJob.put).
export class ObjectProxy extends WorkerEntrypoint<Env> {
	async fetch(req: Request): Promise<Response> {
		const url = new URL(req.url);
		if (!url.pathname.startsWith("/obj/")) return json({ error: "forbidden" }, 403);
		return objects(req, this.env, url);
	}
}

interface Stored {
	hash: string;
	token: string;
	requestedAt: number; // PUT received
	healthyAt: number; // cf-job answered /healthz
	finishedAt?: number; // container destroyed
	final?: string; // the terminal Status JSON
}

export class MediaJob extends DurableObject<Env> {
	async fetch(req: Request): Promise<Response> {
		const id = decodeURIComponent(new URL(req.url).pathname.split("/")[2]);
		switch (req.method) {
			case "PUT":
				return this.put(id, await req.text());
			case "GET":
				return this.get(id);
			case "DELETE":
				return this.delete(id);
		}
		return json({ error: "method not allowed" }, 405);
	}

	private async put(id: string, manifest: string): Promise<Response> {
		const hash = await sha256(manifest);
		const stored = await this.ctx.storage.get<Stored>("job");
		if (stored) {
			if (stored.hash !== hash) return json({ error: `job ${id} exists with a different manifest` }, 409);
			return this.get(id);
		}
		let timeoutMs = MAX_JOB_MS;
		try {
			const t = Number(JSON.parse(manifest)?.limits?.timeout_ms);
			if (t > 0) timeoutMs = Math.min(t, MAX_JOB_MS);
		} catch {
			return json({ error: "manifest: not JSON" }, 400);
		}
		const token = randomHex(32);
		const requestedAt = Date.now();
		// The deadline first: if anything below throws, the alarm still cleans up.
		await this.ctx.storage.setAlarm(requestedAt + timeoutMs + 30_000);
		const c = this.ctx.container!;
		if (!c.running) {
			// Egress: with object URLs on http://objects.internal the container gets no internet at all and its object requests
			// are handed to ObjectProxy (inside Cloudflare, plain HTTP, no CA needed); otherwise it reaches the Worker's public URL.
			const internal = manifest.includes(`http://${INTERNAL_HOST}/`);
			if (internal) await c.interceptOutboundHttp(INTERNAL_HOST, (this.ctx as unknown as { exports: { ObjectProxy: Fetcher } }).exports.ObjectProxy);
			c.start({ env: { CF_JOB_TOKEN: token }, enableInternet: !internal });
		}
		try {
			await waitHealthy(c, 90_000);
		} catch (e) {
			await this.destroy();
			return json({ error: `the container did not become healthy: ${String(e)}` }, 503, { "Retry-After": "5" });
		}
		const healthyAt = Date.now();
		await this.ctx.storage.put<Stored>("job", { hash, token, requestedAt, healthyAt });
		const res = await c.getTcpPort(PORT).fetch(
			new Request(`http://container/jobs/${encodeURIComponent(id)}`, {
				method: "PUT",
				headers: { authorization: `Bearer ${token}`, "content-type": "application/json" },
				body: manifest,
			}),
		);
		const body = await res.text();
		if (res.status >= 400) {
			await this.destroy();
			await this.ctx.storage.deleteAll();
		}
		return new Response(body, {
			status: res.status,
			headers: { "content-type": "application/json", "x-cold-start-ms": String(healthyAt - requestedAt) },
		});
	}

	private async get(id: string): Promise<Response> {
		const s = await this.ctx.storage.get<Stored>("job");
		if (!s) return json({ error: "no such job" }, 404);
		if (s.final) return this.answer(s.final, s);
		const c = this.ctx.container!;
		if (!c.running) {
			const lost = JSON.stringify({ id, state: "error", error: "the container is gone", error_kind: "sandbox", exit_code: -1, duration_ms: 0, timed_out: false, steps: [] });
			return this.finish(s, lost);
		}
		let res: Response;
		try {
			res = await c.getTcpPort(PORT).fetch(new Request(`http://container/jobs/${encodeURIComponent(id)}`, { headers: { authorization: `Bearer ${s.token}` } }));
		} catch (e) {
			return json({ error: `the container did not answer: ${String(e)}` }, 503, { "Retry-After": "2" });
		}
		const body = await res.text();
		if (res.ok) {
			const st = JSON.parse(body) as { state?: string };
			if (st.state && TERMINAL.has(st.state)) return this.finish(s, body);
		}
		return new Response(body, { status: res.status, headers: { "content-type": "application/json" } });
	}

	private async delete(id: string): Promise<Response> {
		const s = await this.ctx.storage.get<Stored>("job");
		if (!s) return json({ error: "no such job" }, 404);
		const c = this.ctx.container!;
		if (c.running && !s.final) {
			try {
				await c.getTcpPort(PORT).fetch(new Request(`http://container/jobs/${encodeURIComponent(id)}`, { method: "DELETE", headers: { authorization: `Bearer ${s.token}` } }));
			} catch {
				// destroyed below either way
			}
		}
		await this.destroy();
		await this.ctx.storage.deleteAll();
		return new Response(null, { status: 204 });
	}

	async alarm(): Promise<void> {
		const s = await this.ctx.storage.get<Stored>("job");
		if (s && !s.final) {
			const timedOut = JSON.stringify({ state: "timed_out", error: "the gateway deadline passed", exit_code: -1, duration_ms: Date.now() - s.requestedAt, timed_out: true, steps: [] });
			await this.finish(s, timedOut);
			return;
		}
		await this.destroy();
	}

	private async finish(s: Stored, final: string): Promise<Response> {
		await this.destroy();
		s.final = final;
		s.finishedAt = Date.now();
		await this.ctx.storage.put("job", s);
		await this.ctx.storage.deleteAlarm();
		return this.answer(final, s);
	}

	private answer(final: string, s: Stored): Response {
		return new Response(final, {
			headers: {
				"content-type": "application/json",
				"x-cold-start-ms": String(s.healthyAt - s.requestedAt),
				// From the start request to the destroy: the time memory and disk were provisioned (an upper bound of the bill).
				"x-container-ms": String((s.finishedAt ?? Date.now()) - s.requestedAt),
			},
		});
	}

	private async destroy(): Promise<void> {
		const c = this.ctx.container;
		if (c?.running) {
			try {
				await c.destroy();
			} catch {
				// already stopping
			}
		}
	}
}

async function waitHealthy(c: Container, timeoutMs: number): Promise<void> {
	const deadline = Date.now() + timeoutMs;
	let last = "";
	while (Date.now() < deadline) {
		try {
			const r = await c.getTcpPort(PORT).fetch(new Request("http://container/healthz"));
			if (r.ok) return;
			last = `healthz ${r.status}`;
		} catch (e) {
			last = String(e);
		}
		await new Promise((r) => setTimeout(r, 100));
	}
	throw new Error(last || "timeout");
}

// Signed object URLs: /obj/{key}?m=GET|PUT&exp={unix seconds}&sig=hex(HMAC-SHA256(token, "obj\n" + m + "\n" + key + "\n" + exp)).
async function objects(req: Request, env: Env, url: URL): Promise<Response> {
	const key = decodeURIComponent(url.pathname.slice("/obj/".length));
	const m = url.searchParams.get("m") ?? "";
	const exp = Number(url.searchParams.get("exp"));
	const sig = url.searchParams.get("sig") ?? "";
	if (!key || key.includes("..") || m !== req.method || !(exp > Date.now() / 1000)) return json({ error: "forbidden" }, 403);
	const want = await hmacHex(env.GATEWAY_TOKEN, `obj\n${m}\n${key}\n${exp}`);
	if (!timingSafeEqual(want, sig)) return json({ error: "forbidden" }, 403);
	if (m === "GET") {
		const obj = await env.BUCKET.get(key);
		if (!obj) return json({ error: "no such object" }, 404);
		const h = new Headers();
		obj.writeHttpMetadata(h);
		h.set("content-length", String(obj.size));
		return new Response(obj.body, { headers: h });
	}
	if (m === "PUT") {
		const len = Number(req.headers.get("content-length"));
		if (!(len >= 0)) return json({ error: "content-length required" }, 411);
		const body = len === 0 ? "" : req.body!;
		await env.BUCKET.put(key, body, { httpMetadata: { contentType: req.headers.get("content-type") ?? "application/octet-stream" } });
		return new Response(null, { status: 200 });
	}
	return json({ error: "method not allowed" }, 405);
}

async function purge(env: Env): Promise<Response> {
	let deleted = 0;
	let cursor: string | undefined;
	do {
		const page = await env.BUCKET.list({ cursor, limit: 1000 });
		if (page.objects.length) await env.BUCKET.delete(page.objects.map((o) => o.key));
		deleted += page.objects.length;
		cursor = page.truncated ? page.cursor : undefined;
	} while (cursor);
	const left = await env.BUCKET.list({ limit: 1 });
	return json({ deleted, left: left.objects.length });
}

async function authorized(req: Request, env: Env): Promise<boolean> {
	const h = req.headers.get("authorization") ?? "";
	if (!h.startsWith("Bearer ") || !env.GATEWAY_TOKEN) return false;
	// Compare digests so the lengths match.
	return timingSafeEqual(await sha256(h.slice(7)), await sha256(env.GATEWAY_TOKEN));
}

function timingSafeEqual(a: string, b: string): boolean {
	const ea = new TextEncoder().encode(a);
	const eb = new TextEncoder().encode(b);
	if (ea.byteLength !== eb.byteLength) return false;
	return crypto.subtle.timingSafeEqual(ea, eb);
}

async function sha256(s: string): Promise<string> {
	return hex(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(s)));
}

async function hmacHex(key: string, msg: string): Promise<string> {
	const k = await crypto.subtle.importKey("raw", new TextEncoder().encode(key), { name: "HMAC", hash: "SHA-256" }, false, ["sign"]);
	return hex(await crypto.subtle.sign("HMAC", k, new TextEncoder().encode(msg)));
}

function hex(buf: ArrayBuffer): string {
	return [...new Uint8Array(buf)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

function randomHex(n: number): string {
	const b = new Uint8Array(n);
	crypto.getRandomValues(b);
	return hex(b.buffer);
}

function json(v: unknown, status = 200, headers: Record<string, string> = {}): Response {
	return new Response(JSON.stringify(v), { status, headers: { "content-type": "application/json", ...headers } });
}
