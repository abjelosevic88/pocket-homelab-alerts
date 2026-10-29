import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: 'dist', emptyOutDir: true, sourcemap: false,
    rollupOptions: { output: { manualChunks: { react: ['react', 'react-dom', 'react-router-dom'] } } },
  },
  server: {
    port: 5173,
    proxy: { '/api': process.env.API_URL || 'http://localhost:8080', '/healthz': process.env.API_URL || 'http://localhost:8080', '/pocket-ask.js': process.env.API_URL || 'http://localhost:8080' },
  },
})
