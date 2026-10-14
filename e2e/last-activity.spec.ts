import { expect, test } from '@playwright/test'
import { registerVerifyAndLogin } from './auth'

// Ticket #56: project cards show when the session was last touched, in
// relative time ("2 分钟前", "昨天"), so users can tell at a glance which
// projects are still active.
test('cards show relative last-activity time', async ({ page, request }) => {
  await registerVerifyAndLogin(page, request)

  // Create two projects with a delay between them so their updatedAt values
  // differ enough to tell apart.
  await page.locator('.home-composer textarea').fill('一个待办应用')
  await page.getByRole('button', { name: '开始构建' }).click()
  await expect(page.locator('.tl-item.tl-done').first()).toBeVisible({ timeout: 90_000 })
  await page.getByRole('button', { name: '返回项目列表' }).click()

  await page.waitForTimeout(3000)

  await page.locator('.home-composer textarea').fill('一个电商网站')
  await page.getByRole('button', { name: '开始构建' }).click()
  await expect(page.locator('.tl-item.tl-done').first()).toBeVisible({ timeout: 90_000 })
  await page.getByRole('button', { name: '返回项目列表' }).click()

  // The list shows relative times rather than absolute timestamps.
  const cards = page.locator('.dash-card')
  await expect(cards).toHaveCount(2)

  // The first card (most recent) should say "刚刚" or "N 秒前" or "1 分钟前".
  const firstTime = cards.first().locator('.dash-time')
  await expect(firstTime).toBeVisible()
  const firstText = await firstTime.textContent()
  expect(firstText).toMatch(/^(刚刚|\d+ 秒前|1 分钟前)$/)

  // The second card (a few seconds older) should also be recent.
  const secondTime = cards.nth(1).locator('.dash-time')
  await expect(secondTime).toBeVisible()
  const secondText = await secondTime.textContent()
  expect(secondText).toMatch(/^(刚刚|\d+ 秒前|\d+ 分钟前)$/)

  // The time element must carry a machine-readable dateTime attribute for
  // accessibility and programmatic use.
  await expect(firstTime).toHaveAttribute('datetime', /.+/)
  await expect(secondTime).toHaveAttribute('datetime', /.+/)
})
