/// <reference types="vitest/config" />
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// The build lands in ../dist, which is committed and embedded by the Go server,
// so that `go run ./contextual-ui` needs no Node toolchain.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "../dist",
    emptyOutDir: true,
    // One bundle with Material UI is about 550 kB (170 kB gzipped). That is fine
    // for a demo served from localhost, and splitting it would only add noise.
    chunkSizeWarningLimit: 700,
  },
  server: {
    // `npm run dev` proxies the APIs to `go run ./contextual-ui`.
    proxy: {
      "/v1": "http://127.0.0.1:8080",
      "/demo": "http://127.0.0.1:8080",
    },
  },
  test: {
    environment: "node",
    include: ["src/**/*.test.ts"],
    passWithNoTests: true,
  },
});
