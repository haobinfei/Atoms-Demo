import { expect, test } from '@playwright/test'
import { registerVerifyAndLogin } from './auth'

// Ticket #57: the workspace states how long a turn has been waiting and how
// long it has been running, so a slow queue looks different from a slow build.
test('the status badge distinguishes queued from building', async ({ page, request }) => {
  await registerVerifyAndLogin(page, request)
  await page.locator('.home-composer textarea').fill('一家咖啡店的电商网站')
  await page.getByRole('button', { name: '开始构建' }).click()

  // While the turn is in flight the badge carries a live clock. It may say
  // 排队中 (still queued) or 构建中 (a Worker claimed it) depending on timing.
  await expect(page.locator('.ws-status')).toHaveText(/^(排队中|构建中) \d+s$/, { timeout: 30_000 })

  // The clock advances: sample it twice and require the number to grow.
  const readSeconds = async () => {
    const text = (await page.locator('.ws-status').textContent()) ?? ''
    return Number(text.match(/(\d+)s/)?.[1] ?? '0')
  }
  const first = await readSeconds()
  await expect.poll(readSeconds, { timeout: 15_000 }).toBeGreaterThan(first)

  // When the turn finishes the badge states the outcome, not a clock.
  await expect(page.locator('.ws-status')).toHaveText('构建完成', { timeout: 90_000 })
})

// A queued turn must be distinguishable from a running one: with the queue
// held, the badge stays on 排队中 rather than claiming work has started.
test('a turn waiting in the queue says so', async ({ page, request }) => {
  await registerVerifyAndLogin(page, request)

  // Hold every turn Job so the session is queued but never claimed. The stub
  // reports a session with queuedAt set and startedAt absent.
  await page.route(/\/api\/sessions\/[^/]+$/, async (route) => {
    const url = route.request().url()
    if (url.endsWith('/list') || route.request().method() !== 'GET') {
      await route.fallback()
      return
    }
    const id = url.split('?')[0].split('/').pop() ?? ''
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        id,
        prompt: '一家咖啡店的电商网站',
        status: 'building',
        kind: 'turn',
        createdAt: new Date(Date.now() - 60_000).toISOString(),
        updatedAt: new Date().toISOString(),
        messages: [],
        events: [],
        publishes: [],
        previewReady: false,
        // Queued 5s ago, never claimed.
        queuedAt: new Date(Date.now() - 5000).toISOString(),
      }),
    })
  })
  await page.route(/\/api\/sessions\/[^/]+\/events/, async (route) => {
    await route.fulfill({ status: 200, contentType: 'text/event-stream', body: '' })
  })

  await page.locator('.home-composer textarea').fill('一家咖啡店的电商网站')
  await page.getByRole('button', { name: '开始构建' }).click()

  await expect(page.locator('.ws-status')).toHaveText(/^排队中 \d+s$/, { timeout: 30_000 })
})
