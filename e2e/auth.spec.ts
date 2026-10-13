import { expect, test } from '@playwright/test'
import { registerVerifyAndLogin } from './auth'

test('registration requires email verification before login, and logout revokes the session', async ({ page, request }) => {
  await registerVerifyAndLogin(page, request)
  await expect(page.getByRole('navigation', { name: '主导航' })).toBeVisible()
  // 退出入口在账户菜单里，不在顶栏直接暴露（P0 改造把邮箱与退出收进了下拉）。
  await page.locator('.menu-trigger').click()
  await page.getByRole('menuitem', { name: '退出登录' }).click()
  await expect(page.getByRole('heading', { name: '登录 Atoms' })).toBeVisible()
  await page.reload()
  await expect(page.getByRole('heading', { name: '登录 Atoms' })).toBeVisible()
  await expect(page.locator('.app-email')).toHaveCount(0)
})
