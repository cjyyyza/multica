import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import path from "path";
import { platformTestOptions } from "../../scripts/vitest-platform.mjs";

export default defineConfig({
  plugins: [react()],
  test: {
    ...platformTestOptions,
    environment: "jsdom",
    globals: true,
    setupFiles: ["./test/setup.ts"],
    include: ["**/*.test.{ts,tsx}"],
  },
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "."),
      "@core": path.resolve(__dirname, "core"),
    },
  },
});
