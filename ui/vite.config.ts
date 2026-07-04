import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The Go server serves the built SPA from ui/dist. In dev, proxy the API and
// webhook routes to the backend running on :8080.
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      "/api": "http://localhost:8080",
      "/hooks": "http://localhost:8080",
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
});
