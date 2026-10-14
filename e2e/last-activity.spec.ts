import { expect, test } from '@playwright/test'
import { registerVerifyAndLogin } from './auth'

// Ticket #67: project cards show when the session was last touched, in
// relative time ("2 分钟前", "昨天"), so users can tell at a glance which
// projects are still active.
test('cards show relative last-activity time', async ({ page, request }) => {
  await registerVerifyAndLogin(page, request)

  // 返回首页 lands on the 新建项目 tab, so the list is a second click.
  const backToList = async () => {
    await page.getByRole('button', { name: '返回首页' }).click()
    await page.getByRole('button', { name: '我的项目' }).click()
  }
  // The tab, not the dashboard's empty-state button of the same name.
  const newProjectTab = () => page.locator('.home-tabs .tab', { hasText: '新建项目' })

  await page.locator('.home-composer textarea').fill('一个待办应用')
  await page.getByRole('button', { name: '开始构建' }).click()
  await expect(page.locator('.tl-item.tl-done').first()).toBeVisible({ timeout: 90_000 })
  await backToList()

  // A gap so the two cards land in different relative buckets.
  await page.waitForTimeout(3000)

  await newProjectTab().click()
  await page.locator('.home-composer textarea').fill('一个电商网站')
  await page.getByRole('button', { name: '开始构建' }).click()
  await expect(page.locator('.tl-item.tl-done').first()).toBeVisible({ timeout: 90_000 })
  await backToList()

  // The list shows relative time rather than an absolute timestamp. Both are
  // fresh, so both must read as "just now" or seconds/minutes ago.
  const cards = page.locator('.dash-card')
  await expect(cards).toHaveCount(2)
  const recent = /^(刚刚|\d+ 秒前|\d+ 分钟前)$/
  for (let i = 0; i < 2; i++) {
    const time = cards.nth(i).locator('.dash-time')
    await expect(time).toBeVisible()
    await expect(time).toHaveText(recent)
    // The machine-readable form is kept for accessibility and programmatic use.
    await expect(time).toHaveAttribute('datetime', /.+/)
  }
})
