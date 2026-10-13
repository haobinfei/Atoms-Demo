import { expect, test } from '@playwright/test'
import { registerVerifyAndLogin } from './auth'
import { stubSession } from './stub-session'

// Ticket #33: a 发布 (Publish) rebuilds on a Worker, so a slow one must read as
// progress — not as a failure that invites the user to publish a duplicate.
test('a slow 发布 shows progress instead of a failure', async ({ page, request }) => {
  await registerVerifyAndLogin(page, request)
  await page.locator('.home-composer textarea').fill('一家咖啡店的电商网站')
  await page.getByRole('button', { name: '开始构建' }).click()
  await expect(page.locator('.tl-item.tl-done').first()).toBeVisible({ timeout: 60_000 })

  // The server accepts the 发布, but the version is held back — the slow build
  // this ticket is about.
  await page.route(/\/api\/sessions\/[^/]+\/publish$/, async (route) => {
    if (route.request().method() !== 'POST') {
      await route.fallback()
      return
    }
    await route.fulfill({
      status: 202,
      contentType: 'application/json',
      body: JSON.stringify({ status: 'publishing' }),
    })
  })

  const stub = await stubSession(page, {
    status: 'ready',
    kind: '',
    previewReady: true,
    events: [],
    publishes: [],
  })
  await page.reload()

  await page.getByRole('button', { name: '发布', exact: true }).click()

  // From here the 会话 reports a 发布 in flight and no new version.
  stub.setStatus('building')
  stub.setKind('publish')
  await page.getByRole('button', { name: '发布 v1', exact: true }).click()

  // 进行中 while it runs...
  await expect(page.locator('.pub-progress')).toContainText('正在重新构建并发布')
  await expect(page.getByRole('button', { name: '发布中…' })).toBeDisabled()

  // ...and still 进行中 well past the 15s deadline the client used to treat as
  // a failure. This wait is the assertion: there is nothing to poll for.
  await page.waitForTimeout(17_000)
  await expect(page.locator('.ws-chat-error')).toHaveCount(0)
  await expect(page.getByRole('button', { name: '发布中…' })).toBeDisabled()

  // The version lands: the share link and the QR appear, and the button frees
  // up for a deliberate next version.
  stub.setKind('')
  stub.setStatus('ready')
  stub.setPublishes([{ version: 1, url: '/app/x/', at: new Date().toISOString() }])

  await expect(page.getByAltText('应用二维码')).toBeVisible({ timeout: 30_000 })
  await expect(page.locator('.pub-link')).toContainText('/app/x/')
  await expect(page.getByRole('button', { name: '再次发布（下一版 v2）' })).toBeVisible()
  await expect(page.locator('.pub-progress')).toHaveCount(0)
})

// The frame always shows the draft build. The badge is what stops that from
// being ambiguous once a version has been published.
test('the preview says whether it is the draft or the published version', async ({
  page,
  request,
}) => {
  await registerVerifyAndLogin(page, request)
  await page.locator('.home-composer textarea').fill('一家咖啡店的电商网站')
  await page.getByRole('button', { name: '开始构建' }).click()
  await expect(page.locator('.tl-item.tl-done').first()).toBeVisible({ timeout: 60_000 })

  await expect(page.locator('.preview-badge')).toHaveText('草稿版')

  await page.getByRole('button', { name: '发布', exact: true }).click()
  await page.getByRole('button', { name: '发布 v1', exact: true }).click()
  await expect(page.getByAltText('应用二维码')).toBeVisible({ timeout: 30_000 })
  await page.getByRole('button', { name: '关闭' }).click()

  // The 发布 event lands on the 事件流, which is what flips the badge.
  await expect(page.locator('.preview-badge')).toHaveText('已发布版', { timeout: 30_000 })
})
