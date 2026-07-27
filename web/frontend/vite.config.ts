import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Vite 配置：
// - 构建产物直接输出到 ../static（Go 后端托管该目录，同源无跨域）
// - 开发服务器把 /api 代理到本机 8080 的 Go 后端，方便 npm run dev 调试
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: '../static',
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://127.0.0.1:8080',
    },
  },
})
