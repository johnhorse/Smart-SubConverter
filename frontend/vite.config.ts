/// <reference types="vitest/config" />
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  server: {
    // 本地联调：dev 模式下 /api 转发到宿主机 sidecar（端口见仓库 nginx/sidecar 部署文档）
    proxy: {
      '/api': 'http://127.0.0.1:18081',
    },
  },
  test: {
    environment: 'jsdom',
    globals: true,
  },
})
