import { expect, test } from '@playwright/test'
import { registerVerifyAndLogin } from './auth'
import { stubSession } from './stub-session'

// Ticket #29: the composer takes multiple lines, Enter sends, and a follow-up
// typed while a 回合 is running waits for it instead of interrupting it.
test('the composer takes multiple lines and Enter sends', async ({ page, request }) => {
  await registerVerifyAndLogin(page, request)
  await page.locator('.home-composer textarea').fill('一家咖啡店的电商网站')
  await page.getByRole('button', { name: '开始构建' }).click()
  await expect(page.locator('.tl-item.tl-done').first()).toBeVisible({ timeout: 60_000 })

  const box = page.locator('.ws-chat-form textarea')
  await box.fill('第一行')
  await box.press('Shift+Enter')
  await box.type('第二行')
  await expect(box).toHaveValue('第一行\n第二行')

  // Enter sends rather than inserting another newline.
  await box.press('Enter')
  await expect(box).toHaveValue('')
  // It really was sent: the follow-up opens the second 回合.
  await expect(page.locator('.tl-user-bubble', { hasText: '第一行' })).toBeVisible()
})

test('a follow-up typed during a turn waits for it and then goes out', async ({ page, request }) => {
  await registerVerifyAndLogin(page, request)
  await page.locator('.home-composer textarea').fill('一家咖啡店的电商网站')
  await page.getByRole('button', { name: '开始构建' }).click()
  await expect(page.locator('.tl-item.tl-done').first()).toBeVisible({ timeout: 60_000 })

  const sent: string[] = []
  await page.route(/\/api\/sessions\/[^/]+\/chat$/, async (route) => {
    const body = JSON.parse(route.request().postData() ?? '{}') as { message?: string }
    sent.push(body.message ?? '')
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ status: 'building' }),
    })
  })

  // Hold the session in 构建中: a turn in flight is the state a queued
  // follow-up is defined against, and timing alone cannot reach it.
  const stub = await stubSession(page, { status: 'building', previewReady: false, events: [] })
  await page.reload()
  await expect(page.locator('.ws-status')).toContainText('构建中')

  const box = page.locator('.ws-chat-form textarea')
  await box.fill('先把标题改短')
  await box.press('Enter')

  // Queued, not sent, and not interrupting the running turn.
  await expect(page.locator('.ws-queue-tag')).toHaveText('排队中')
  await expect(page.locator('.ws-queue')).toContainText('先把标题改短')
  expect(sent, 'a queued follow-up must not be sent while the turn runs').toEqual([])

  // The turn finishes; the queue drains on its own.
  stub.setStatus('ready')
  await expect.poll(() => sent.length, { timeout: 60_000 }).toBe(1)
  expect(sent[0]).toBe('先把标题改短')
  await expect(page.locator('.ws-queue')).toHaveCount(0)
})
