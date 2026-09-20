import { defineConfig } from "vitest/config";
import { platformTestOptions } from "../../scripts/vitest-platform.mjs";

export default defineConfig({
  test: {
    ...platformTestOptions,
    globals: true,
    include: ["**/*.test.{ts,tsx}"],
    passWithNoTests: true,
  },
});
