import { randomUUID } from 'node:crypto'
import { expect, type APIRequestContext, type Page } from '@playwright/test'

const WEB = 'http://127.0.0.1:5199'
const MAILPIT = 'http://127.0.0.1:58025'
// Exported so a case that signs out and back in (the 演示路径's "登录已有账户")
// uses the same credentials the registration path set.
export const E2E_PASSWORD = 'e2e-test-password-123'

interface MailpitList {
  messages: { ID: string; To: { Address: string }[] }[]
}

export async function mailLink(request: APIRequestContext, email: string, query: 'verify' | 'invite'): Promise<string> {
  let link = ''
  await expect.poll(async () => {
    const response = await request.get(`${MAILPIT}/api/v1/messages`)
    if (!response.ok()) throw new Error(`Mailpit API returned ${response.status()}`)
    const inbox = (await response.json()) as MailpitList
    const mail = inbox.messages?.find((message) => message.To.some((to) => to.Address === email))
    if (!mail) return false
    const detailResponse = await request.get(`${MAILPIT}/api/v1/message/${encodeURIComponent(mail.ID)}`)
    if (!detailResponse.ok()) throw new Error(`Mailpit message returned ${detailResponse.status()}`)
    const detail = (await detailResponse.json()) as { Text?: string }
    link = detail.Text?.split(/\s+/).find((part) => part.startsWith(`${WEB}/?${query}=`)) ?? ''
    return link !== ''
  }, { timeout: 20_000, message: `${query} email for ${email}` }).toBe(true)
  return link
}

export async function registerVerifyAndLogin(page: Page, request: APIRequestContext): Promise<{ email: string }> {
  const email = `e2e-${randomUUID()}@example.test`
  const clientIP = '2001:db8:' + randomUUID().replaceAll('-', '').slice(0, 24).match(/.{4}/g)!.join(':')
  await page.context().setExtraHTTPHeaders({ 'X-Real-IP': clientIP })
  await expect.poll(async () => {
    try {
      return (await request.get(`${MAILPIT}/api/v1/messages`)).ok()
    } catch {
      return false
    }
  }, { timeout: 20_000, message: 'Mailpit API readiness' }).toBe(true)
  await page.goto('/')
  await expect(page.getByRole('heading', { name: '登录 Atoms' })).toBeVisible()
  await page.getByRole('button', { name: '创建账户' }).click()
  await page.getByLabel('邮箱').fill(email)
  await page.getByLabel('密码（至少 12 位）').fill(E2E_PASSWORD)
  await page.getByLabel('确认密码').fill(E2E_PASSWORD)
  await page.getByRole('button', { name: '注册', exact: true }).click()
  await expect(page.getByRole('status')).toContainText('请查看邮箱')
  await page.getByRole('button', { name: '返回登录' }).click()
  await page.getByLabel('邮箱').fill(email)
  await page.getByLabel('密码', { exact: true }).fill(E2E_PASSWORD)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('401')

  await page.goto(await mailLink(request, email, 'verify'))
  await expect(page.getByRole('status')).toContainText('邮箱验证成功')
  await page.getByLabel('邮箱').fill(email)
  await page.getByLabel('密码', { exact: true }).fill(E2E_PASSWORD)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.locator('.app-email')).toHaveText(email)
  return { email }
}
