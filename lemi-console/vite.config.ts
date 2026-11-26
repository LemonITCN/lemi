import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath, URL } from 'node:url'

// 开发时把接口转发到本机运行的 lemi serve（默认 127.0.0.1:7700）
const target = process.env.LEMI_API || 'http://127.0.0.1:7700'

export default defineConfig({
  plugins: [vue()],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  server: {
    port: 5373,
    proxy: {
      '/api/v1/ws': { target, ws: true, changeOrigin: false },
      '/api': { target, changeOrigin: false },
      '/mcp': { target, changeOrigin: false },
      '/oauth': { target, changeOrigin: false },
      '/.well-known': { target, changeOrigin: false },
      '/healthz': { target, changeOrigin: false },
    },
  },
  build: {
    outDir: 'dist',
    assetsDir: 'assets',
    chunkSizeWarningLimit: 4000,
    sourcemap: false,
  },
})
