import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  test: {
    include: ['src/**/*.{test,spec}.{ts,tsx}'],
  },
  build: {
    outDir: '../pkg/server/dist',
    emptyOutDir: true,
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (!id.includes('node_modules')) return

          if (id.includes('@xterm')) return 'xterm'
          if (/[\\/](react|react-dom|scheduler)[\\/]/.test(id)) return 'react'
        },
      },
    },
  },
  server: {
  	allowedHosts: ["devvm"],
    proxy: {
      '/api': 'http://localhost:7654',
      '/file': 'http://localhost:7654',
      '/ws': {
        target: 'http://localhost:7654',
        ws: true,
        rewriteWsOrigin: true,
      },
    },
  },
})
