import { defineConfig, mergeConfig } from "vitest/config";
import { baseConfig } from "./vite.base";

export default mergeConfig(
  baseConfig,
  defineConfig({
    test: { environment: "jsdom", include: ["src/**/*.test.{ts,tsx}"], setupFiles: ["./src/test-setup.ts"] },
  }),
);
