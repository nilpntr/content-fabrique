import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
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
