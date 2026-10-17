const UNITS: readonly [limit: number, size: number, unit: Intl.RelativeTimeFormatUnit][] = [
  [60_000, 1000, "second"],
  [3_600_000, 60_000, "minute"],
  [86_400_000, 3_600_000, "hour"],
  [30 * 86_400_000, 86_400_000, "day"],
  [365 * 86_400_000, 30 * 86_400_000, "month"],
];

const formatter = new Intl.RelativeTimeFormat("en", { numeric: "auto" });

/** "2 minutes ago", "yesterday", "3 months ago". `now` is a parameter so that it can be tested. An unreadable date is "". */
export function relativeTime(iso: string, now: number = Date.now()): string {
  const then = Date.parse(iso);
  if (!Number.isFinite(then)) return "";
  const diff = then - now;
  const ago = Math.abs(diff);
  if (ago < 5000) return "just now";
  for (const [limit, size, unit] of UNITS) {
    if (ago < limit) return formatter.format(Math.round(diff / size), unit);
  }
  return formatter.format(Math.round(diff / (365 * 86_400_000)), "year");
}
