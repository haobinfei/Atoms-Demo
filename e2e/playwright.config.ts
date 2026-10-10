import { defineConfig, devices } from '@playwright/test'

// The E2E stack is fully self-contained: Playwright boots the real Go
// server (生成引擎 stubbed to the deterministic template provider by
// pointing the local-LLM probe at a dead port and leaving the DeepSeek
// key empty) and a vite preview of the built SPA, then drives a browser
// through the happy path in both desktop and mobile viewports.
const API = 'http://127.0.0.1:4173'
const WEB = 'http://127.0.0.1:5199'

export default defineConfig({
  testDir: '.',
  testMatch: '*.spec.ts',
  timeout: 90_000,
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
      command: 'go run ./cmd/server',
      cwd: '..',
      env: {
        PORT: '4173',
        WORKSPACE_DIR: 'e2e/.workspace',
        AUTH_MODE: 'dev',
        DEV_USER: 'e2e-user',
        LOCAL_LLM_BASE_URL: 'http://127.0.0.1:9/v1',
        // Force the deterministic stub even if the developer's shell
        // exports a real key.
        DEEPSEEK_API_KEY: '',
        DEEPSEEK_BASE_URL: '',
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
