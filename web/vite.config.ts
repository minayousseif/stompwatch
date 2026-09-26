import path from "node:path"

import tailwindcss from "@tailwindcss/vite"
import react from "@vitejs/plugin-react"
import { defineConfig } from "vite"

// The Go binary embeds web/dist, so the build writes there with no hashed
// directory in front of it. The dev server proxies /api to the running
// collector, or to tools/devserver. Set STOMPWATCH_API when the collector
// listens somewhere other than 127.0.0.1:8080.
const api = process.env.STOMPWATCH_API ?? "http://127.0.0.1:8080"

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { "@": path.resolve(import.meta.dirname, "./src") },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  server: {
    proxy: {
      "/api": {
        target: api,
        changeOrigin: false,
        // The live meter is a server-sent event stream. Buffering it would
        // hold every reading until the connection closed.
        configure: (proxy) => {
          proxy.on("proxyRes", (res) => {
            if (res.headers["content-type"]?.includes("text/event-stream")) {
              res.headers["cache-control"] = "no-cache"
            }
          })
        },
      },
    },
  },
})
