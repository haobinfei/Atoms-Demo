import { expect, test } from '@playwright/test'

// Fail fast with a clear message if the server picked up a real LLM key
// from the developer's shell: the happy path expects the deterministic
// template stub.
test.beforeEach(async ({ request }) => {
  const resp = await request.get('http://127.0.0.1:4173/api/providers')
  const providers = await resp.json()
  expect(
    providers.providers,
    'E2E needs the template-only provider chain (stub); check DEEPSEEK_API_KEY is unset for the webServer env',
  ).toEqual(['template'])
})

// Happy path per ticket #11: prompt → timeline renders → iframe preview
// non-blank AND interactive → modification turn → preview refreshed.
// Runs once per project (desktop 1280×800, mobile 390×844).
test('prompt → timeline → interactive preview → chat turn → refreshed preview', async ({
  page,
}) => {
  await page.goto('/')

  // 输入 prompt 并创建会话。
  await page.getByPlaceholder('例如：一家咖啡店的电商网站').fill('一家咖啡店的电商网站')
  await page.getByRole('button', { name: '开始构建' }).click()

  // 时间线开始渲染（计划事件出现）。
  await expect(page.locator('.tl-item.tl-plan').first()).toBeVisible()

  // 首个回合完成。
  await expect(page.locator('.tl-item.tl-done').first()).toBeVisible({ timeout: 60_000 })

  // iframe 预览非空白：生成的电商应用首屏文案可见。
  const frame = page.frameLocator('.frame')
  await expect(frame.getByText('用心挑选的每一款')).toBeVisible()

  // 可交互：加入购物车后角标变化。
  await frame.getByRole('button', { name: '加入购物车' }).first().click()
  await expect(frame.getByRole('button', { name: '购物车（1）' })).toBeVisible()

  // 预览「刷新」是 iframe 因 key 变化而重建：在 document 捕获阶段监听
  // 新 iframe 的 load 事件，直接观测重载发生（而非间接推断）。
  // 监听必须在点击发送之前挂上，回合结束的构建可能很快。
  const reloaded = page.evaluate(
    () =>
      new Promise<boolean>((resolve) => {
        const timer = setTimeout(() => resolve(false), 60_000)
        document.addEventListener(
          'load',
          (ev) => {
            if (ev.target instanceof HTMLIFrameElement) {
              clearTimeout(timer)
              resolve(true)
            }
          },
          true,
        )
      }),
  )

  // 发起修改回合。
  await page.locator('.ws-chat-form input').fill('把主题色换成绿色，加一句标语')
  await page.locator('.ws-chat-form button[type="submit"]').click()
  await expect(page.locator('.tl-user-bubble', { hasText: '把主题色换成绿色' })).toBeVisible()

  // 第二个完成事件 = 预览已重建刷新，且仍非空白。
  await expect(page.locator('.tl-item.tl-done').nth(1)).toBeVisible({ timeout: 60_000 })
  expect(await reloaded).toBe(true)
  await expect(frame.getByText('用心挑选的每一款')).toBeVisible()
})
