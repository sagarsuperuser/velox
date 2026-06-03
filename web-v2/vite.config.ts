import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import path from 'path'
import http from 'node:http'

// Keep-alive agent for the /v1 proxy. Without it, http-proxy opens a fresh
// (non-pooled) connection to the backend per request and stamps the CLIENT
// response with `Connection: close` — so the browser can't pool API
// connections at all. With a keep-alive agent the proxy reuses backend
// sockets and preserves keep-alive end-to-end.
const backendKeepAlive = new http.Agent({ keepAlive: true, maxSockets: 64 })

// THE dev-hang fix (verified). Node's default Server.keepAliveTimeout is 5s:
// it closes an idle keep-alive connection after 5s, but browsers hold pooled
// connections for minutes. So if you sit on a page >5s (e.g. filling the login
// form) and then make a request, the browser reuses a socket Vite already
// closed → the request is sent into a dead connection and HANGS with no
// response (or ECONNRESET) until a long browser timeout. Reproduced directly:
// reuse-after-6s-idle → ECONNRESET at 5s, OK with keepAliveTimeout=0. This is
// the actual cause of the recurring "pending forever" — NOT the network stack,
// dep optimization, or transforms (Vite sits at 0.1% CPU while it happens).
//
// 0 disables the idle close so dev connections stay alive as long as the
// browser wants them. headersTimeout=0 disables the companion guard so it
// can't trip either. Both are safe for a local single-user dev server.
function keepConnectionsAlive() {
  return {
    name: 'velox-dev-keepalive',
    configureServer(server: { httpServer: http.Server | null }) {
      if (server.httpServer) {
        server.httpServer.keepAliveTimeout = 0
        server.httpServer.headersTimeout = 0
      }
    },
  }
}

export default defineConfig({
  plugins: [react(), tailwindcss(), keepConnectionsAlive()],
  resolve: {
    alias: { '@': path.resolve(__dirname, './src') },
  },
  // Pre-bundle ALL app deps up front so Vite never re-optimizes mid-session.
  //
  // Root cause of the recurring "pending forever" hang (confirmed by timing:
  // `/` static = 39ms, but `/src/main.tsx` = 25s on a cold hit, then ~1ms —
  // and stale `node_modules/.vite/deps_temp_*` dirs from interrupted runs):
  // Vite HOLDS every module + API request while esbuild (re)optimizes deps. It
  // re-triggers whenever a dep is discovered that wasn't in the initial entry
  // scan — e.g. a dependency reachable only through a lazily-imported route
  // (main.tsx uses React.lazy). Each discovery → re-optimize → 25s pipeline
  // freeze + full reload. Listing the deps here forces a single complete
  // optimize at startup, so nothing is discovered late and the freeze never
  // recurs while you're using the app. (Keep this list in sync with the deps
  // that show up in `.vite/deps/_metadata.json`.)
  optimizeDeps: {
    include: [
      'react', 'react-dom', 'react-dom/client',
      'react/jsx-runtime', 'react/jsx-dev-runtime',
      'react-router-dom',
      '@tanstack/react-query',
      'react-hook-form', '@hookform/resolvers/zod', 'zod',
      'recharts',
      '@vvo/tzdb', 'date-fns', 'date-fns-tz',
      'cmdk', 'lucide-react', 'react-day-picker', 'sonner',
      'class-variance-authority', 'clsx', 'tailwind-merge', '@radix-ui/react-slot',
      '@base-ui/react/alert-dialog', '@base-ui/react/button', '@base-ui/react/checkbox',
      '@base-ui/react/dialog', '@base-ui/react/input', '@base-ui/react/menu',
      '@base-ui/react/merge-props', '@base-ui/react/select', '@base-ui/react/separator',
      '@base-ui/react/switch', '@base-ui/react/tabs', '@base-ui/react/tooltip',
      '@base-ui/react/use-render',
    ],
  },
  server: {
    // PIN IPv4 end-to-end — do NOT use `localhost` anywhere in the dev path.
    //
    // Root cause of the recurring "pending forever" hang (diagnosed live via
    // lsof: TCP handshakes stuck in SYN_SENT to BOTH 127.0.0.1 and ::1):
    // `localhost` resolves to two stacks (127.0.0.1 + ::1), so the browser AND
    // Node's proxy run happy-eyeballs, racing both. On macOS the IPv4-mapped
    // path to a dual-stack socket is ~40x slower (0.9ms on ::1 vs 37ms on
    // 127.0.0.1), and under any load the lagging stack's handshake stalls in
    // SYN_SENT — the request that got assigned to it hangs. The earlier
    // `host: true` (dual-stack bind) made BOTH stacks listen, which is exactly
    // what lets the race happen; it didn't stop the hang, it enabled it.
    //
    // Binding to a single literal IPv4 address removes the second stack from
    // the path entirely: Vite prints http://127.0.0.1:5173 (a literal IP — no
    // DNS, no happy-eyeballs), any stray ::1 attempt fails fast (refused, not a
    // SYN_SENT stall), and the proxy target below is likewise a literal IPv4.
    // No `localhost`, no dual-stack race, deterministic. (LAN access — e.g.
    // phone testing — is dropped; set VITE_HOST to override if you need it.)
    host: process.env.VITE_HOST || '127.0.0.1',
    port: 5173,
    proxy: {
      // Bounded + error-handled so a stalled/reset upstream surfaces as a 502
      // instead of leaving the browser request "pending" forever (the dev
      // infinite-spinner). timeout/proxyTimeout sit just above the backend's
      // 30s WriteTimeout so the backend's own response wins when it's merely
      // slow; the proxy only fires on a genuinely dead/wedged upstream.
      '/v1': {
        // Literal IPv4, NOT `localhost` — see the host comment above. A
        // `localhost` target makes Node race both stacks per request (~33ms
        // happy-eyeballs tax, and the SYN_SENT stalls). Measured: localhost
        // target 39ms vs 127.0.0.1 6ms.
        target: 'http://127.0.0.1:8080',
        changeOrigin: true,
        // Keep-alive to the backend so the proxy stops stamping the client
        // response with `Connection: close` (which prevents the browser from
        // pooling /v1 connections at all). The backend keep-alives by default
        // (IdleTimeout 60s); without this agent the proxy threw that away.
        agent: backendKeepAlive,
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
