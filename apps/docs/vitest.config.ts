import { defineConfig } from "vitest/config";
import path from "path";
import { platformTestOptions } from "../../scripts/vitest-platform.mjs";

export default defineConfig({
  oxc: {
    jsx: { runtime: "automatic" },
  },
  test: {
    ...platformTestOptions,
    environment: "node",
    globals: true,
    include: ["**/*.test.{ts,tsx}"],
    exclude: ["node_modules/**", ".next/**", ".source/**"],
  },
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "."),
    },
  },
});
