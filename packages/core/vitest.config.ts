import { defineConfig } from "vitest/config";
import { platformTestOptions } from "../../scripts/vitest-platform.mjs";

export default defineConfig({
  test: {
    ...platformTestOptions,
    globals: true,
    setupFiles: ["@multica/vitest-setup/web-storage"],
    include: ["**/*.test.{ts,tsx}"],
    passWithNoTests: true,
  },
});
