import { expect, test } from '@playwright/test'
import { registerVerifyAndLogin } from './auth'
import { stubSession } from './stub-session'

// Ticket #34: the 工作台 splits at a fixed ratio and either pane can take the
// whole body in one click, on desktop and on a narrow screen alike.
test('either pane can take the whole body and give it back', async ({ page, request }) => {
  await registerVerifyAndLogin(page, request)
  await page.locator('.home-composer textarea').fill('一家咖啡店的电商网站')
  await page.getByRole('button', { name: '开始构建' }).click()
  await expect(page.locator('.tl-item.tl-done').first()).toBeVisible({ timeout: 60_000 })

  const body = page.locator('.ws-body')
  const timeline = page.locator('.ws-timeline')
  const preview = page.locator('.ws-preview')
  // "窄屏下不破版", measured rather than eyeballed: the body must never end up
  // wider than the box it was given.
  const fitsWidth = () =>
    page.evaluate(() => {
      const el = document.querySelector('.ws-body')
      return el === null || el.scrollWidth <= el.clientWidth + 1
    })

  // 分栏: both panes are on screen.
  await expect(body).toHaveClass(/ws-body--split/)
  await expect(timeline).toBeVisible()
  await expect(preview).toBeVisible()
  expect(await fitsWidth(), '分栏 overflows').toBe(true)

  // 一键放大预览: the conversation folds away and the preview takes the body.
  await page.getByRole('button', { name: '放大预览' }).click()
  await expect(body).toHaveClass(/ws-body--preview/)
  await expect(page.getByRole('button', { name: '放大预览' })).toHaveAttribute('aria-pressed', 'true')
  await expect(timeline).toBeHidden()
  await expect(preview).toBeVisible()
  expect(await fitsWidth(), '放大预览 overflows').toBe(true)

  // 再切回分栏.
  await page.getByRole('button', { name: '分栏' }).click()
  await expect(body).toHaveClass(/ws-body--split/)
  await expect(timeline).toBeVisible()

  // 一键放大对话, and back again.
  await page.getByRole('button', { name: '放大对话' }).click()
  await expect(body).toHaveClass(/ws-body--chat/)
  await expect(preview).toBeHidden()
  await expect(timeline).toBeVisible()
  expect(await fitsWidth(), '放大对话 overflows').toBe(true)

  await page.getByRole('button', { name: '分栏' }).click()
  await expect(preview).toBeVisible()
  await expect(timeline).toBeVisible()
})

// 加载 and 空 are the two states that look like "nothing here" if the
// workspace stays silent about them.
test('the workspace says when it is loading and when nothing has happened yet', async ({
  page,
  request,
}) => {
  await registerVerifyAndLogin(page, request)
  await page.locator('.home-composer textarea').fill('一家咖啡店的电商网站')
  await page.getByRole('button', { name: '开始构建' }).click()
  await expect(page.locator('.tl-item.tl-done').first()).toBeVisible({ timeout: 60_000 })

  // 空: the 会话 is read, but no 回合 has produced an event yet.
  await stubSession(page, { status: 'building', previewReady: false, events: [] })
  await page.reload()
  await expect(page.locator('.ws-state--empty')).toContainText('还没有任何进展')

  // 加载: hold the 会话 read open. The workspace must say it is reading rather
  // than render an ambiguous blank with an empty timeline and no preview.
  await page.route(/\/api\/sessions\/[^/]+$/, async (route) => {
    if (route.request().method() !== 'GET' || route.request().url().endsWith('/list')) {
      await route.fallback()
      return
    }
    await new Promise((resolve) => setTimeout(resolve, 2500))
    await route.fallback()
  })
  await page.reload()

  await expect(page.locator('.ws-state--loading')).toContainText('正在载入会话')
  // The read lands and the placeholder gives way on its own.
  await expect(page.locator('.ws-state--empty')).toBeVisible({ timeout: 30_000 })
  await expect(page.locator('.ws-state--loading')).toHaveCount(0)
})

// 断线: a dropped 事件流 means the timeline has stopped moving, and silence
// reads as "the build is stuck". The notice must appear and clear itself once
// the stream is back.
test('the workspace says when the event stream is down, and clears when it returns', async ({
  page,
  request,
}) => {
  await registerVerifyAndLogin(page, request)
  await page.locator('.home-composer textarea').fill('一家咖啡店的电商网站')
  await page.getByRole('button', { name: '开始构建' }).click()
  await expect(page.locator('.tl-item.tl-done').first()).toBeVisible({ timeout: 60_000 })

  // Only the stream is intercepted: the 会话 itself comes from the real server,
  // so clearing the interception really does restore a live stream.
  const events = /\/api\/sessions\/[^/]+\/events/
  const dropStream = async (route: import('@playwright/test').Route) => {
    await route.abort('failed')
  }
  await page.route(events, dropStream)
  await page.reload()

  await expect(page.locator('.ws-state--offline')).toContainText('连接已断开')

  // 恢复: EventSource retries on its own, and the notice clears without a
  // reload — a stale "disconnected" banner would be its own bug.
  await page.unroute(events, dropStream)
  await expect(page.locator('.ws-state--offline')).toHaveCount(0, { timeout: 45_000 })
})
