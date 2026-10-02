import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import path from 'node:path'

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: { '@': path.resolve(__dirname, 'src') },
  },
  server: {
    port: 5173,
    proxy: {
      // 开发态把 /api 转发给 Go 进程。必须关闭代理层的响应缓冲，
      // 否则 SSE 会被攒在代理里，页面上看不到逐字输出。
      // In development /api is forwarded to the Go process. Response buffering in the proxy must
      // be off, otherwise SSE piles up inside it and the page shows no token-by-token output.
      '/api': {
        target: 'http://127.0.0.1:8090',
        changeOrigin: true,
        ws: false,
      },
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
})
