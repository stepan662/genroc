import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    globalSetup: ["./helpers/server.ts"],
    include: ["integration/**/*_test.ts"],
    testTimeout: 30_000,
  },
});
