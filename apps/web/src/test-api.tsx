/**
 * A fake API for component tests: `fetch` is replaced by a router over the paths of the contract, so the real client, hooks and
 * query cache run against canned answers. Handlers see the parsed JSON body; a handler returns a body (status 200), or
 * `json(status, body)`. A path with no handler answers `404 not_found`, like an API that does not have the endpoint.
 */
import { QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import type { ReactElement } from "react";
import { vi } from "vitest";
import { ApiClientProvider } from "@/lib/api/provider";
import { createQueryClient } from "@/lib/query-client";

export interface ApiCall {
  method: string;
  path: string;
  query: URLSearchParams;
  body: unknown;
}

export class Reply {
  constructor(
    readonly status: number,
    readonly body: unknown,
  ) {}
}

export const json = (status: number, body: unknown) => new Reply(status, body);
/** The API's error body. */
export const apiError = (status: number, code: string, message = code) => new Reply(status, { error: { code, message } });

type Handler = (call: ApiCall) => unknown | Promise<unknown>;
/** A canned body (or {@link Reply}), or a function of the call. */
type Route = Handler | object;

export interface FakeApi {
  calls: ApiCall[];
  /** The calls that matched `METHOD /path` (a `{param}` in the path matches one segment). */
  callsTo(route: string): ApiCall[];
  /** Replaces or adds a handler. */
  on(route: string, handler: Route): void;
}

function matches(pattern: string, method: string, path: string): boolean {
  const [m, p] = pattern.split(" ");
  if (m !== method) return false;
  const re = new RegExp(`^${(p as string).replace(/\{[^}]+\}/g, "[^/]+")}$`);
  return re.test(path);
}

/** Installs the fake as the global `fetch` (test-setup.ts undoes it after each test). */
export function installApi(routes: Record<string, Route> = {}): FakeApi {
  const handlers = new Map<string, Route>(Object.entries(routes));
  const calls: ApiCall[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: Request | string) => {
      const request = typeof input === "string" ? new Request(input) : input;
      const url = new URL(request.url);
      const text = request.method === "GET" ? "" : await request.text();
      const call: ApiCall = { method: request.method, path: url.pathname, query: url.searchParams, body: text ? JSON.parse(text) : undefined };
      calls.push(call);
      // The most specific pattern first: a path with fewer wildcards.
      const key = [...handlers.keys()].filter((pattern) => matches(pattern, call.method, call.path)).sort((a, b) => (a.match(/\{/g)?.length ?? 0) - (b.match(/\{/g)?.length ?? 0))[0];
      if (!key) return new Response(JSON.stringify({ error: { code: "not_found", message: "not found" } }), { status: 404, headers: { "Content-Type": "application/json" } });
      const handler = handlers.get(key);
      const answer = typeof handler === "function" ? await (handler as Handler)(call) : handler;
      const reply = answer instanceof Reply ? answer : new Reply(200, answer);
      return new Response(reply.status === 204 ? null : JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
  return {
    calls,
    callsTo: (route) => calls.filter((call) => matches(route, call.method, call.path)),
    on: (route, handler) => void handlers.set(route, handler),
  };
}

/** Renders with the query cache and the API client that the app has. */
export function renderApp(ui: ReactElement) {
  const queryClient = createQueryClient();
  const result = render(
    <QueryClientProvider client={queryClient}>
      <ApiClientProvider>{ui}</ApiClientProvider>
    </QueryClientProvider>,
  );
  return { ...result, queryClient };
}
