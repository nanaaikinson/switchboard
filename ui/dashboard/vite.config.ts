import { fileURLToPath, URL } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// The daemon embeds dist/ into the sb binary (see embed.go). In development,
// `npm run fake-api` serves a fake control API that /v1 is proxied to.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: { alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) } },
  build: { outDir: "dist", emptyOutDir: true, sourcemap: false, chunkSizeWarningLimit: 800 },
  server: { proxy: { "/v1": "http://127.0.0.1:5199" } },
});
