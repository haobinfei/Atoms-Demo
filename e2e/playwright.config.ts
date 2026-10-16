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
  // 每个用例用 randomUUID 注册自己的账号，并设置独立的 X-Real-IP（见 auth.ts——
  // 那是为了不共享认证限流配额），因此用例之间彼此独立，可以并行。
  // CI 上固定 2：GitHub runner 是 4 核，正好两个浏览器 + Node 进程；写死是为了
  // runner 规格变化时并发度不会静默改变。本地保持串行，避免和本机开发抢资源。
  workers: process.env.CI ? 2 : 1,
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
      //
      // dist 命中 CI 缓存时跳过构建（见 ci.yml 的 web-dist 缓存）：构建产物不含
      // 环境相关的内容——ATOMS_API 只是 vite 的代理目标（process.env），不会被
      // 内联进 bundle。缓存 key 覆盖全部构建输入，源码一变就失效。
      command:
        '(test -f dist/index.html || npm run build) && npm run preview -- --host 127.0.0.1 --port 5199 --strictPort',
      cwd: '../web',
      env: { ATOMS_API: API },
      url: WEB,
      reuseExistingServer: false,
      timeout: 180_000,
    },
  ],
  use: { baseURL: WEB },
})
