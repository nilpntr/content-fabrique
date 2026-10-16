import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

export function humanList(items: string[], max = 2): string {
  const names = items.filter(Boolean);
  if (names.length <= 1) return names[0] ?? "";
  if (names.length <= max) return `${names.slice(0, -1).join(", ")} and ${names.at(-1)}`;
  return `${names.slice(0, max).join(", ")} and ${names.length - max} more`;
}

export function initials(name: string | null): string {
  const words = (name ?? "").split(/\s+/).filter(Boolean);
  const first = words[0]?.charAt(0) ?? "";
  const last = words.length > 1 ? (words.at(-1)?.charAt(0) ?? "") : "";
  return (first + last).toUpperCase();
}

export function hue(seed: string): number {
  let value = 0;
  for (const char of seed) value = (value * 31 + (char.codePointAt(0) ?? 0)) % 360;
  return value;
}

export function plural(count: number, one: string, many = `${one}s`) {
  return `${count} ${count === 1 ? one : many}`;
}
