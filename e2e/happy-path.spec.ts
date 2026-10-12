import { expect, test } from '@playwright/test'
import { registerVerifyAndLogin } from './auth'

// Happy path per ticket #11: prompt → timeline renders → iframe preview
// non-blank AND interactive → modification turn → preview refreshed.
// Runs once per project (desktop 1280×800, mobile 390×844).
test('prompt → interactive preview → chat turn → public app → unpublish', async ({
  page,
  request,
}) => {
  await registerVerifyAndLogin(page, request)
  const providersResponse = await page.context().request.get('http://127.0.0.1:5199/api/providers')
  expect(providersResponse.ok()).toBe(true)
  const providers = await providersResponse.json()
  expect(providers.providers, 'E2E requires the deterministic template provider').toEqual(['template'])

  // 输入 prompt 并创建会话。
  await page.locator('.home-composer textarea').fill('一家咖啡店的电商网站')
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

  await page.getByRole('button', { name: '发布', exact: true }).click()
  await page.getByRole('button', { name: '发布 v1', exact: true }).click()
  await expect(page.getByAltText('应用二维码')).toBeVisible()
  const publicURL = await page.locator('.pub-link').innerText()
  const guestContext = await page.context().browser()!.newContext({ viewport: page.viewportSize()! })
  try {
    const guest = await guestContext.newPage()
    await guest.goto(publicURL)
    await expect(guest.getByText('用心挑选的每一款')).toBeVisible()
    page.once('dialog', (dialog) => void dialog.accept())
    await page.getByRole('button', { name: '取消公开', exact: true }).click()
    await expect(page.getByText('当前应用未公开，历史发布链接暂不可访问。')).toBeVisible()
    const response = await guest.reload()
    expect(response?.status()).toBe(404)
  } finally {
    await guestContext.close()
  }
})
