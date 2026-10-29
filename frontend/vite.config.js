import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
export default defineConfig({
  plugins: [react()],
  build: { outDir: "dist", emptyOutDir: true },
  server: {
    host: "127.0.0.1",
    port: 18100,
    strictPort: true,
    watch: { ignored: ["**/.local/**"] },
    fs: { deny: [".env", ".env.*", "*.{crt,pem,key,pfx}", "**/.local/**", "**/.git/**"] },
    proxy: {
      "/api": {
        target: process.env.FRONTEND_API_ORIGIN || "https://worth.oopsbox.cn",
        changeOrigin: true,
      },
    },
  },
});
