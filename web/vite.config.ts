import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  server: {
    strictPort: true,
    proxy: Object.fromEntries(
      ["/api", "/bootstrap"].map((path) => [
        path,
        {
          target: "http://127.0.0.1:8741",
          changeOrigin: true,
          configure(proxy) {
            proxy.on("proxyReq", (request, incoming) => {
              if (incoming.headers.origin === "http://127.0.0.1:5173")
                request.setHeader("Origin", "http://127.0.0.1:8741");
            });
          },
        },
      ]),
    ),
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test-setup.ts"],
    exclude: ["e2e/**", "node_modules/**"],
  },
});
