import { randomUUID } from 'node:crypto'
import { expect, test } from '@playwright/test'
import { E2E_PASSWORD, registerVerifyAndLogin } from './auth'

// Ticket #48: the 认证页 gives feedback while you type, and an expired session
// comes back to the 项目 you were in rather than the home screen.
test('the auth form states rules and mistakes where they belong', async ({ page }) => {
  await page.goto('/')
  await page.getByRole('button', { name: '创建账户' }).click()

  // A bad address and a too-short password are stated under their own fields,
  // and a client-side mistake is not dressed up as a form-wide error.
  await page.getByLabel('邮箱').fill('nope')
  await page.getByLabel('密码（至少 12 位）').fill('short')
  await page.getByRole('button', { name: '注册', exact: true }).click()
  await expect(page.locator('#auth-email-error')).toContainText('看起来不完整')
  await expect(page.locator('#auth-password-error')).toContainText('至少需要 12 个字符')
  await expect(page.getByRole('alert')).toHaveCount(0)

  // The live rule list tracks what has been typed so far.
  const rules = page.locator('.auth-rules li')
  await expect(rules.nth(0)).not.toHaveClass(/is-met/)
  await page.getByLabel('密码（至少 12 位）').fill(E2E_PASSWORD)
  await expect(rules.nth(0)).toHaveClass(/is-met/)
  await page.getByLabel('确认密码').fill('something-else-entirely')
  await expect(rules.nth(1)).not.toHaveClass(/is-met/)

  // Reveal keeps what was typed, and a mismatch is stated on the confirm field.
  const password = page.getByLabel('密码（至少 12 位）')
  await expect(password).toHaveAttribute('type', 'password')
  await page.getByRole('button', { name: '显示密码' }).click()
  await expect(password).toHaveAttribute('type', 'text')
  await expect(password).toHaveValue(E2E_PASSWORD)
  await page.getByRole('button', { name: '隐藏密码' }).click()
  await expect(password).toHaveAttribute('type', 'password')

  await page.getByRole('button', { name: '注册', exact: true }).click()
  await expect(page.locator('#auth-confirm-error')).toContainText('两次输入的密码不一致')
  await page.getByLabel('确认密码').fill(E2E_PASSWORD)
  await expect(rules.nth(1)).toHaveClass(/is-met/)

  // Registering moves to the verify step, and the resend button counts down
  // instead of letting the user fire it again immediately.
  await page.getByLabel('邮箱').fill(`e2e-${randomUUID()}@example.test`)
  await page.getByRole('button', { name: '注册', exact: true }).click()
  await expect(page.getByRole('status')).toContainText('请查看邮箱')
  await expect(page.getByRole('button', { name: /秒后可重发/ })).toBeDisabled()
})

test('an expired session returns to the 项目 it interrupted', async ({ page, request }) => {
  const { email } = await registerVerifyAndLogin(page, request)
  await page.locator('.home-composer textarea').fill('一家咖啡店的电商网站')
  await page.getByRole('button', { name: '开始构建' }).click()
  await expect(page.locator('.tl-item.tl-done').first()).toBeVisible({ timeout: 90_000 })

  const sessionId = new URL(page.url()).searchParams.get('s')
  expect(sessionId, 'the 项目 id is in the address').toBeTruthy()

  // Expire the session, then make the client notice by trying to send.
  await page.context().clearCookies()
  await page.locator('.ws-chat-form textarea').fill('把标题改短')
  await page.locator('.ws-chat-form textarea').press('Enter')

  await expect(page.getByRole('heading', { name: '登录 Atoms' })).toBeVisible()
  expect(new URL(page.url()).searchParams.get('s'), 'the 项目 survives the redirect').toBe(sessionId)

  // Logging back in lands in that 项目, not on the home screen.
  await page.getByLabel('邮箱').fill(email)
  await page.getByLabel('密码', { exact: true }).fill(E2E_PASSWORD)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.locator('.ws-prompt')).toHaveText('一家咖啡店的电商网站')
  await expect(page.locator('.tl-item.tl-plan').first()).toBeVisible()
})
