import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import path from 'path'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { '@': path.resolve(__dirname, './src') },
  },
  server: {
    // Bind all interfaces (IPv4 + IPv6), not the default `localhost` which on
    // macOS resolves to ::1 and binds IPv6-only — so a browser that resolves
    // `localhost` to 127.0.0.1 (IPv4, listed first in /etc/hosts) hits a dead
    // address and the page hangs "pending". host:true makes 127.0.0.1, ::1,
    // and localhost all reach the dev server.
    host: true,
    port: 5173,
    proxy: {
      // Bounded + error-handled so a stalled/reset upstream surfaces as a 502
      // instead of leaving the browser request "pending" forever (the dev
      // infinite-spinner). timeout/proxyTimeout sit just above the backend's
      // 30s WriteTimeout so the backend's own response wins when it's merely
      // slow; the proxy only fires on a genuinely dead/wedged upstream.
      '/v1': {
        target: 'http://localhost:8080',
        changeOrigin: true,
        timeout: 35_000,
        proxyTimeout: 35_000,
        configure: (proxy) => {
          proxy.on('error', (err, _req, res) => {
            // res is a ServerResponse for normal requests (not WS upgrades).
            const sr = res as unknown as {
              writeHead?: (s: number, h: Record<string, string>) => void
              end?: (b?: string) => void
              headersSent?: boolean
            }
            if (sr && typeof sr.writeHead === 'function' && !sr.headersSent) {
              sr.writeHead(502, { 'Content-Type': 'application/json' })
              sr.end?.(JSON.stringify({ error: { message: `dev proxy: upstream error or timeout (${(err as NodeJS.ErrnoException).code ?? err.message})` } }))
            }
          })
        },
      },
    },
  },
})
