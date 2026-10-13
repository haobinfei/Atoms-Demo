import { expect, test } from '@playwright/test'
import { registerVerifyAndLogin } from './auth'

// Ticket #28: the 事件流 reads as 回合 — what was asked, what it did, how it
// ended — rather than as one flat list of events.
test('the timeline groups a build and a revision into two turns', async ({ page, request }) => {
  await registerVerifyAndLogin(page, request)
  await page.locator('.home-composer textarea').fill('一家咖啡店的电商网站')
  await page.getByRole('button', { name: '开始构建' }).click()
  await expect(page.locator('.tl-item.tl-done').first()).toBeVisible({ timeout: 60_000 })

  // A follow-up opens the second turn.
  await page.locator('.ws-chat-form input').fill('把主题色换成绿色')
  await page.locator('.ws-chat-form button[type="submit"]').click()
  await expect(page.locator('.tl-item.tl-done').nth(1)).toBeVisible({ timeout: 60_000 })

  // Two asks, two turns.
  const turns = page.locator('.tl-turn')
  await expect(turns).toHaveCount(2)
  await expect(turns.nth(0)).toContainText('第 1 回合')
  await expect(turns.nth(1)).toContainText('第 2 回合')

  // Each turn states what was asked and how it ended.
  await expect(turns.nth(0).locator('.tl-user-bubble')).toHaveText('一家咖啡店的电商网站')
  await expect(turns.nth(1).locator('.tl-user-bubble')).toHaveText('把主题色换成绿色')
  await expect(turns.nth(0).locator('.tl-turn-result')).toContainText('本轮完成')
  await expect(turns.nth(1).locator('.tl-turn-result')).toContainText('本轮完成')
  await expect(turns.nth(1).locator('.tl-turn-result')).toContainText('修改了')

  // File writes collapse behind a count and expand on demand.
  const files = turns.nth(0).locator('.tl-files')
  await expect(files.locator('summary')).toContainText('修改了')
  await expect(files.locator('li').first()).not.toBeVisible()
  await files.locator('summary').click()
  await expect(files.locator('li').first()).toBeVisible()

  // Reloading keeps the grouping intact.
  await page.reload()
  await expect(page.locator('.tl-turn')).toHaveCount(2)
  await expect(page.locator('.tl-turn').nth(1).locator('.tl-user-bubble')).toHaveText(
    '把主题色换成绿色',
  )
})
