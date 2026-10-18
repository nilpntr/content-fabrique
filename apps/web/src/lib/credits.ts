/**
 * Credits (spend control, docs/design/spend-control-v1.md; the API side is PR #138, which adds `credits` to `GET /v1/me`).
 *
 * The OpenAPI types on main do not have the field yet, so the app reads it from the answer with a guard instead of a type:
 * {@link creditsOf} returns the balance when `me` carries a well-formed `credits` and nothing otherwise. Every screen that shows
 * credits (the header chip, the Settings card) is behind that, so they stay out of the way until the API says, and light up
 * on their own when it does. The shape below is #138's `Credits` schema; when it lands in `packages/schemas`, this becomes
 * `components["schemas"]["Credits"]` and the guard can go.
 *
 * `402 credits_exhausted` is handled by its error code, wherever a call is made: see {@link announceCreditsExhausted}.
 */
import * as React from "react";
import { ApiError } from "@/lib/api/client";

export interface CreditTopUp {
  credits: number;
  expires_at: string;
}

export interface Credits {
  /** What a video costs in credits (a first edit and two rounds of changes), from the price list: the only place the app learns it. */
  credits_per_video: number;
  /** What a new action can use now, allowance and top-ups together. Credits held by a running edit are already out of it. */
  available: number;
  /** The size of the current period's allowance; 0 when there is none. */
  allowance: number;
  /** When the allowance renews; null when there is none or it does not end. */
  period_end: string | null;
  /** At least 80 % of the allowance is used. */
  low: boolean;
  top_ups: CreditTopUp[];
}

const isRecord = (value: unknown): value is Record<string, unknown> => typeof value === "object" && value !== null && !Array.isArray(value);
const count = (value: unknown): value is number => typeof value === "number" && Number.isFinite(value) && value >= 0;

/** The workspace's credits from a `/v1/me` answer, or undefined when the answer has none (the API does not offer them yet, or could not read the balance). */
export function creditsOf(me: unknown): Credits | undefined {
  const raw = isRecord(me) ? me.credits : undefined;
  // `credits_per_video` is part of the contract (GET /v1/me); credits without it cannot be shown as videos, so they are not shown.
  if (!isRecord(raw) || !count(raw.available) || !Number.isInteger(raw.credits_per_video) || (raw.credits_per_video as number) < 1) return undefined;
  const topUps = Array.isArray(raw.top_ups)
    ? raw.top_ups.flatMap((entry): CreditTopUp[] => (isRecord(entry) && count(entry.credits) && typeof entry.expires_at === "string" ? [{ credits: entry.credits, expires_at: entry.expires_at }] : []))
    : [];
  return {
    credits_per_video: raw.credits_per_video as number,
    available: raw.available,
    allowance: count(raw.allowance) ? raw.allowance : 0,
    period_end: typeof raw.period_end === "string" ? raw.period_end : null,
    low: raw.low === true,
    top_ups: topUps,
  };
}

export const creditsLabel = (credits: number): string => `${credits} ${credits === 1 ? "credit" : "credits"}`;

/** "14 Nov", or "14 Nov 2027" for a date in another year. */
export function formatDay(iso: string, now: Date = new Date()): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "";
  const sameYear = date.getFullYear() === now.getFullYear();
  return date.toLocaleDateString("en-GB", { day: "numeric", month: "short", ...(sameYear ? {} : { year: "numeric" }) });
}

/** Whether an error is the API's "not enough credits" (402 `credits_exhausted`). */
export function isCreditsExhausted(error: unknown): boolean {
  return error instanceof ApiError && error.status === 402 && error.code === CREDITS_EXHAUSTED_CODE;
}

export const CREDITS_EXHAUSTED_CODE = "credits_exhausted";

// ---- The "out of credits" announcement -------------------------------------------------------------------------------
// A small store outside React: the query client (which sees every failed call) says it, the dialog in the shell shows it.

let exhausted = false;
const listeners = new Set<() => void>();

const emit = () => {
  for (const listener of listeners) listener();
};

/** Opens the "out of credits" dialog. Called for any call the API refuses with 402 `credits_exhausted`. */
export function announceCreditsExhausted(): void {
  // A server render must never set what the next visitor's page would read; only a browser announces.
  if (typeof window === "undefined" || exhausted) return;
  exhausted = true;
  emit();
}

export function dismissCreditsExhausted(): void {
  if (!exhausted) return;
  exhausted = false;
  emit();
}

/** True while the dialog should be open. */
export function useCreditsExhausted(): boolean {
  return React.useSyncExternalStore(
    (listener) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    () => exhausted,
    () => false,
  );
}
