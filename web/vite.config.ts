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
  	allowedHosts: ["devvm"],
    proxy: {
      '/api': 'http://localhost:7654',
      '/ws': {
        target: 'http://localhost:7654',
        ws: true,
        rewriteWsOrigin: true,
      },
    },
  },
})
