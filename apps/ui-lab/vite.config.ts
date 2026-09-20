import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { platformTestOptions } from "../../scripts/vitest-platform.mjs";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  test: {
    ...platformTestOptions,
    environment: "node",
    css: { include: [/tokens\.css/] },
    include: ["src/**/*.test.ts"],
  },
});
