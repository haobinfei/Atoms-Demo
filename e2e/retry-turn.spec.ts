import { expect, test } from '@playwright/test'
import { registerVerifyAndLogin } from './auth'
import { stubSession } from './stub-session'

// Ticket #30: a failed 回合 must say where it broke and offer a retry.
//
// The deterministic template provider never fails, so the failed state is
// stubbed to exercise the client wiring. The server contract — stage and hint
// on the error event, 409 while building, re-enqueue on retry without
// appending a message — is covered by the HTTP-layer tests.
test('a failed turn states the stage and can be retried', async ({ page, request }) => {
  await registerVerifyAndLogin(page, request)
  await page.locator('.home-composer textarea').fill('一家咖啡店的电商网站')
  await page.getByRole('button', { name: '开始构建' }).click()
  await expect(page.locator('.tl-item.tl-done').first()).toBeVisible({ timeout: 60_000 })

  await stubSession(page, {
    status: 'failed',
    previewReady: false,
    events: [
      {
        seq: 1,
        type: 'error',
        agent: 'alex',
        at: new Date().toISOString(),
        payload: {
          message: 'model returned 500',
          stage: 'generate',
          hint: '重试会重新调用模型。',
        },
      },
    ],
  })

  let retried = false
  await page.route(/\/api\/sessions\/[^/]+\/retry$/, async (route) => {
    retried = true
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ status: 'building' }),
    })
  })

  await page.reload()

  // Where it broke, in words, plus something to do about it. Stated above the
  // split as well as inside the timeline, so the advice sits next to the retry
  // and stays visible whichever pane is maximised (ticket #34).
  await expect(page.locator('.tl-item.tl-error')).toContainText('失败阶段：代码生成')
  await expect(page.locator('.tl-hint')).toContainText('重试会重新调用模型')
  await expect(page.locator('.ws-state--failed')).toContainText('本轮构建失败：代码生成')
  await expect(page.locator('.ws-state--failed')).toContainText('重试会重新调用模型')

  await page.getByRole('button', { name: '重试本回合' }).click()
  await expect.poll(() => retried).toBe(true)
})
