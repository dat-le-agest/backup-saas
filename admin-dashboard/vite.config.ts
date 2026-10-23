import path from "node:path";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// `npm run dev` serves the dashboard on :5173 and forwards /api to the admin
// service (make up: http://localhost:8090). `npm run build` writes into
// platform/admin/web/dist, embedded in the admin service binary (web/web.go).
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: { alias: { "@": path.resolve(import.meta.dirname, "src") } },
  server: {
    port: 5173,
    proxy: { "/api": process.env.ADMIN_API_URL ?? "http://localhost:8090" },
  },
  // keep dist/.gitkeep: go:embed needs the directory even before a build
  // an internal tool: one bundle is fine
  build: { outDir: "../platform/admin/web/dist", emptyOutDir: false, chunkSizeWarningLimit: 800 },
});
