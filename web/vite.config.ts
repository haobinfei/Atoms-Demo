import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The Go server owns /api, /app and /pay; vite (dev or preview) proxies
// them so the SPA and the generated apps share one origin in local runs.
const api = process.env.ATOMS_API ?? 'http://localhost:4173'
// Preserve the browser-facing Host so the API can validate Origin for CSRF.
const apiProxy = { target: api, changeOrigin: false }
const proxy = {
  '/api': apiProxy,
  '/app': apiProxy,
  '/pay': apiProxy,
}

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy,
  },
  preview: { proxy },
})
