import { fileURLToPath, URL } from "node:url";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import wasm from "vite-plugin-wasm";
import topLevelAwait from "vite-plugin-top-level-await";

// @ts-expect-error process is a nodejs global
const host = process.env.TAURI_DEV_HOST;

// https://vite.dev/config/
export default defineConfig(async () => ({
  // wasm() + topLevelAwait(): Vite's substitute for webpack's `experiments.asyncWebAssembly`
  // (what mallow-client's next.config.mjs uses) — needed to import the wasm-pack
  // --target bundler output in `alm-wasm` (see src/lib/almanac-wasm.ts).
  plugins: [react(), tailwindcss(), wasm(), topLevelAwait()],
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },

  // esnext: vite-plugin-top-level-await needs a build target that actually supports top-level
  // await (the wasm-pack `--target bundler` module init uses it) — Vite's default target
  // (chrome87/safari14/...) predates that. Safe for Tauri: the bundled WebView (WebView2/
  // WKWebView/WebKitGTK) is always modern, not a public browser-compat target.
  build: { target: "esnext" },
  esbuild: { target: "esnext" },

  // Vite options tailored for Tauri development and only applied in `tauri dev` or `tauri build`
  //
  // 1. prevent Vite from obscuring rust errors
  clearScreen: false,
  // 2. tauri expects a fixed port, fail if that port is not available
  server: {
    port: 1420,
    strictPort: true,
    host: host || false,
    hmr: host
      ? {
          protocol: "ws",
          host,
          port: 1421,
        }
      : undefined,
    watch: {
      // 3. tell Vite to ignore watching `src-tauri`
      ignored: ["**/src-tauri/**"],
    },
  },
}));
