import { expect, test } from '@playwright/test'
import { registerVerifyAndLogin } from './auth'

test('registration requires email verification before login, and logout revokes the session', async ({ page, request }) => {
  await registerVerifyAndLogin(page, request)
  await expect(page.getByRole('navigation', { name: '主导航' })).toBeVisible()
  await page.getByRole('button', { name: '退出' }).click()
  await expect(page.getByRole('heading', { name: '登录 Atoms' })).toBeVisible()
  await page.reload()
  await expect(page.getByRole('heading', { name: '登录 Atoms' })).toBeVisible()
  await expect(page.locator('.app-email')).toHaveCount(0)
})
