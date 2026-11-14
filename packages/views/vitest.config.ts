import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import { platformTestOptions } from "../../scripts/vitest-platform.mjs";
import BalancedSequencer from "./test/balanced-sequencer";

export default defineConfig({
  plugins: [react()],
  test: {
    ...platformTestOptions,
    environment: "jsdom",
    globals: true,
    setupFiles: ["./test/setup.ts"],
    include: ["**/*.test.{ts,tsx}"],
    sequence: { sequencer: BalancedSequencer },
  },
});
