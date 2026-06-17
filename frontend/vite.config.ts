import { defineConfig } from "vite";
import react, { reactCompilerPreset } from "@vitejs/plugin-react";
import babel from "@rolldown/plugin-babel";

// https://vite.dev/config/
export default defineConfig({
  plugins: [react(), babel({ presets: [reactCompilerPreset()] })],
  server: {
    host: true,
    port: 5173,
    proxy: {
      "/admin": {
        target: "http://pl-gateway:80",
        changeOrigin: true
      },
      "/wallets": {
        target: "http://pl-gateway:80",
        changeOrigin: true
      },
      "/health": {
        target: "http://pl-gateway:80",
        changeOrigin: true
      },
      "/metrics": {
        target: "http://pl-gateway:80",
        changeOrigin: true
      },
      "/reconcile": {
        target: "http://pl-gateway:80",
        changeOrigin: true
      },
      "/fx": {
        target: "http://pl-gateway:80",
        changeOrigin: true
      },
      "/webhooks": {
        target: "http://pl-gateway:80",
        changeOrigin: true
      },
      "/idempotency": {
        target: "http://pl-gateway:80",
        changeOrigin: true
      }
    }
  }
});