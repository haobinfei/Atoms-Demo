import { defineConfig, devices } from '@playwright/test'

// Start the isolated Postgres and Mailpit services in e2e/compose.yaml before
// running Playwright. Playwright boots the Go API with a deterministic template
// provider and the built SPA.
const API = 'http://127.0.0.1:4173'
const WEB = 'http://127.0.0.1:5199'
const DATABASE_DSN = 'postgres://atoms_e2e:local-e2e-only@127.0.0.1:55432/atoms_e2e?sslmode=disable'

export default defineConfig({
  testDir: '.',
  testMatch: '*.spec.ts',
  timeout: 180_000,
  workers: 1,
  reporter: [['list']],
  projects: [
    {
      name: 'desktop',
      use: { ...devices['Desktop Chrome'], viewport: { width: 1280, height: 800 } },
    },
    {
      name: 'mobile',
      use: { ...devices['Desktop Chrome'], viewport: { width: 390, height: 844 } },
    },
  ],
  webServer: [
    {
      command: 'go run ./e2e/server.go',
      cwd: '..',
      env: {
        DATABASE_DSN,
      },
      url: API + '/api/health',
      reuseExistingServer: false,
      timeout: 120_000,
    },
    {
      // --host 127.0.0.1: Node binds localhost to ::1 on macOS, and the
      // health check (plus baseURL) polls the IPv4 address.
      command: 'npm run build && npm run preview -- --host 127.0.0.1 --port 5199 --strictPort',
      cwd: '../web',
      env: { ATOMS_API: API },
      url: WEB,
      reuseExistingServer: false,
      timeout: 180_000,
    },
  ],
  use: { baseURL: WEB },
})
