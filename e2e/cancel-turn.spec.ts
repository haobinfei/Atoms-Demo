import { expect, test } from '@playwright/test'
import { registerVerifyAndLogin } from './auth'
import { stubSession } from './stub-session'

// Ticket #32: a 回合 can be cancelled while it runs. Cancelling stops the turn,
// returns the session to 就绪 (never 失败), records the 取消 on the timeline, and
// leaves the version built by the previous turn live (ADR-0013).
test('cancelling a running turn keeps the previous version live', async ({ page, request }) => {
  await registerVerifyAndLogin(page, request)
  await page.locator('.home-composer textarea').fill('一家咖啡店的电商网站')
  await page.getByRole('button', { name: '开始构建' }).click()
  await expect(page.locator('.tl-item.tl-done').first()).toBeVisible({ timeout: 60_000 })

  // The first turn produced a version. 预览 is member-only until the app is
  // published, so the request must carry the session cookie — hence
  // page.request rather than the bare request fixture.
  const iframe = page.locator('.preview-stage iframe')
  await expect(iframe).toBeVisible()
  const sessionId = ((await iframe.getAttribute('src')) ?? '').match(/\/app\/([^/]+)\//)?.[1] ?? ''
  expect(sessionId, 'session id from the preview address').not.toBe('')
  const appURL = `/app/${sessionId}/`
  const before = await page.request.get(appURL)
  expect(before.status()).toBe(200)
  const beforeBody = await before.text()

  // A follow-up starts a second 回合; cancel it while it is still running. The
  // E2E server slows the stages on purpose so the button cannot miss the turn.
  await page.locator('.ws-chat-form textarea').fill('把主题色换成绿色')
  await page.locator('.ws-chat-form textarea').press('Enter')
  await page.getByRole('button', { name: '取消本回合' }).click()

  // 就绪, not 失败: a cancel is the user's decision, not a broken build.
  await expect(page.locator('.ws-status')).toContainText('构建完成', { timeout: 60_000 })

  const turns = page.locator('.tl-turn')
  await expect(turns).toHaveCount(2)
  await expect(turns.nth(1).locator('.tl-item.tl-cancel')).toContainText('取消回合')
  await expect(turns.nth(1).locator('.tl-turn-result')).toContainText('本轮已取消')

  // The point of the whole feature: /app serves the very same bytes it did
  // before the turn was abandoned, and the preview is still mounted on them.
  const after = await page.request.get(appURL)
  expect(after.status()).toBe(200)
  expect(await after.text()).toBe(beforeBody)
  await expect(page.frameLocator('.frame').getByText('用心挑选的每一款')).toBeVisible()
})

// The other half of ADR-0013: queued follow-ups are not the turn's to take
// with it. They stay put until the user sends them deliberately.
test('a cancelled turn leaves queued follow-ups for the user to send', async ({ page, request }) => {
  await registerVerifyAndLogin(page, request)
  await page.locator('.home-composer textarea').fill('一家咖啡店的电商网站')
  await page.getByRole('button', { name: '开始构建' }).click()
  await expect(page.locator('.tl-item.tl-done').first()).toBeVisible({ timeout: 60_000 })

  // Hold the session in 构建中: a turn in flight is the state a queued
  // follow-up is defined against, and timing alone cannot reach it.
  const stub = await stubSession(page, { status: 'building', previewReady: true, events: [] })
  await page.reload()
  await expect(page.locator('.ws-status')).toContainText('构建中')

  const sent: string[] = []
  let canceled = false
  await page.route(/\/api\/sessions\/[^/]+\/chat$/, async (route) => {
    const body = JSON.parse(route.request().postData() ?? '{}') as { message?: string }
    sent.push(body.message ?? '')
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ status: 'building' }),
    })
  })
  await page.route(/\/api\/sessions\/[^/]+\/cancel$/, async (route) => {
    canceled = true
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ status: 'canceling' }),
    })
  })

  const box = page.locator('.ws-chat-form textarea')
  await box.fill('先把标题改短')
  await box.press('Enter')
  await expect(page.locator('.ws-queue')).toContainText('先把标题改短')

  await page.getByRole('button', { name: '取消本回合' }).click()
  await expect.poll(() => canceled, { message: '取消请求已发出' }).toBe(true)

  // The turn ends as cancelled, which is what the client learns from the
  // stream — no server here, so the events are delivered by hand.
  const at = new Date().toISOString()
  stub.setStatus('ready')
  stub.setEvents([
    { seq: 1, type: 'cancel', agent: 'user', payload: { detail: '用户取消了本回合' }, at },
    { seq: 2, type: 'done', agent: 'mike', payload: { status: 'ready', canceled: true }, at },
  ])
  stub.setStream([
    { seq: 1, type: 'cancel', agent: 'user', payload: { detail: '用户取消了本回合' }, at },
    { seq: 2, type: 'done', agent: 'mike', payload: { status: 'ready', canceled: true }, at },
  ])

  await expect(page.locator('.ws-status')).toContainText('构建完成', { timeout: 30_000 })
  await expect(page.locator('.tl-turn-result').last()).toContainText('本轮已取消')
  await expect(page.locator('.ws-queue')).toContainText('先把标题改短')
  expect(sent, 'a cancelled turn must not send the follow-ups queued behind it').toEqual([])

  // The user can still send it on purpose.
  await page.getByRole('button', { name: '发送排队消息：先把标题改短' }).click()
  await expect.poll(() => sent.length, { timeout: 30_000 }).toBe(1)
  expect(sent[0]).toBe('先把标题改短')
})
