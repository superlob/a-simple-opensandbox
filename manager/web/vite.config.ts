import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// 构建产物输出到 Python 包内，由 FastAPI StaticFiles 托管（同源访问，无 CORS）。
export default defineConfig({
  plugins: [react()],
  build: { outDir: "../src/sandbox_manager/web", emptyOutDir: true },
  server: {
    // 开发时把 /api 与 /mcp 代理到 Manager；端口来自环境变量 MANAGER_PORT（见 Makefile）。
    proxy: {
      "/api": `http://127.0.0.1:${process.env.MANAGER_PORT ?? ""}`,
    },
  },
});
