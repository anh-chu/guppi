import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: '../pkg/server/dist',
    emptyOutDir: true,
    rollupOptions: {
      output: {
        manualChunks: {
          xterm: ['@xterm/xterm', '@xterm/addon-fit', '@xterm/addon-web-links', '@xterm/addon-clipboard'],
        },
      },
    },
  },
  server: {
    proxy: {
      '/api': 'http://localhost:7654',
      '/ws': {
        target: 'ws://localhost:7654',
        ws: true,
      },
    },
  },
})
