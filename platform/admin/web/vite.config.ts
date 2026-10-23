import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// `npm run dev` serves the dashboard on :5173 and forwards /api to the admin
// service (make up: http://localhost:8090). `npm run build` writes dist/,
// embedded by the admin service (web/web.go).
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: { "/api": process.env.ADMIN_API_URL ?? "http://localhost:8090" },
  },
  build: { outDir: "dist", emptyOutDir: false },
});
