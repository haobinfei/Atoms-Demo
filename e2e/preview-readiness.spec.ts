import { expect, test } from '@playwright/test'
import { registerVerifyAndLogin } from './auth'

// Ticket #27: preview availability is decided server-side, so neither the
// project list nor the workspace ever mounts a frame that has nothing to
// serve (and therefore never shows an error page inside the preview).
test('the dashboard cover renders the built app', async ({ page, request }) => {
  await registerVerifyAndLogin(page, request)
  await page.locator('.home-composer textarea').fill('一家咖啡店的电商网站')
  await page.getByRole('button', { name: '开始构建' }).click()
  await expect(page.locator('.tl-item.tl-done').first()).toBeVisible({ timeout: 60_000 })

  // 返回首页 lands on the 新建项目 tab, so switch to the project list.
  await page.getByRole('button', { name: '返回首页' }).click()
  await page.getByRole('button', { name: '我的项目' }).click()

  const card = page.locator('.dash-card').first()
  await expect(card.locator('.dash-cover-frame')).toBeVisible()
  // The cover really is the built app, not an error page.
  await expect(card.frameLocator('iframe').getByText('用心挑选的每一款')).toBeVisible()
})

test('the workspace states why there is no preview instead of loading an error page', async ({
  page,
  request,
}) => {
  await registerVerifyAndLogin(page, request)
  await page.locator('.home-composer textarea').fill('一家咖啡店的电商网站')
  await page.getByRole('button', { name: '开始构建' }).click()
  await expect(page.locator('.tl-item.tl-done').first()).toBeVisible({ timeout: 60_000 })

  // A servable build: the frame is mounted and the placeholder is absent.
  await expect(page.locator('.preview-stage .frame')).toBeVisible()
  await expect(page.locator('.preview-idle')).toHaveCount(0)

  // Force the "no servable build" branch. The deterministic template provider
  // always succeeds, so that path cannot be reached by timing alone; the flag
  // is the server's word, and this drives the client with it directly.
  // The body is fetched over the API context rather than route.fetch(),
  // because reloading the page disposes responses still in flight.
  await page.route(/\/api\/sessions\/[^/]+$/, async (route) => {
    const url = route.request().url()
    if (url.endsWith('/list')) {
      await route.fallback()
      return
    }
    const upstream = await page.request.get(url)
    const body = (await upstream.json()) as Record<string, unknown>
    await route.fulfill({
      status: upstream.status(),
      contentType: 'application/json',
      body: JSON.stringify({ ...body, previewReady: false }),
    })
  })
  await page.reload()

  await expect(page.locator('.preview-idle')).toBeVisible()
  await expect(page.locator('.preview-stage .frame')).toHaveCount(0)
})
