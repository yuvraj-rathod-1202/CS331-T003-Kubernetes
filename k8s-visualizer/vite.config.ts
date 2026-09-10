import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'
import { faultInjectorPlugin } from './vite-plugin-fault-api.ts'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react(), faultInjectorPlugin()],
  server: {
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8001',
        changeOrigin: true,
      },
      '/apis': {
        target: 'http://127.0.0.1:8001',
        changeOrigin: true,
      },
    },
  },
})
