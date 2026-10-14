import { expect, test } from '@playwright/test'
import { registerVerifyAndLogin } from './auth'

// Ticket #56: the dashboard card's cover is a screenshot of the app, captured
// from the workspace preview (same-origin, so foreignObject/canvas can read
// it) and stored per session. A card without one says why instead of booting
// a whole application.
test('a finished build gives its dashboard card a screenshot cover', async ({ page, request }) => {
  await registerVerifyAndLogin(page, request)
  await page.locator('.home-composer textarea').fill('一家咖啡店的电商网站')
  await page.getByRole('button', { name: '开始构建' }).click()

  // The workspace opens on the new session; the preview frame only mounts
  // once the build is servable, and the capture runs on that frame's load.
  await expect(page.locator('.tl-item.tl-done').first()).toBeVisible({ timeout: 90_000 })
  const sessionId = new URL(page.url()).searchParams.get('s')
  expect(sessionId).toBeTruthy()

  // The upload is fire-and-forget after the frame paints, so poll for it
  // rather than assuming a fixed delay.
  await expect
    .poll(
      async () => {
        const resp = await request.get(`/api/sessions/${sessionId}/thumbnail`)
        return resp.status()
      },
      { timeout: 30_000, intervals: [500] },
    )
    .toBe(200)

  // What lands is a real PNG, not an error page that happens to render.
  const shot = await request.get(`/api/sessions/${sessionId}/thumbnail`)
  expect(shot.headers()['content-type']).toContain('image/png')
  expect(shot.headers()['cache-control']).toBe('no-store')
  const body = await shot.body()
  expect(body.subarray(0, 8).toString('hex')).toBe('89504e470d0a1a0a')
  expect(body.length).toBeGreaterThan(1000)

  // The dashboard card shows it: an actual decoded image, not a broken src.
  await page.goto('/')
  await page.getByRole('button', { name: '我的项目' }).click()
  const card = page.locator('.dash-card', { hasText: '一家咖啡店的电商网站' })
  const img = card.locator('img.dash-cover-img')
  await expect(img).toBeVisible()
  const dims = await img.evaluate((el: HTMLImageElement) => ({ w: el.naturalWidth, h: el.naturalHeight }))
  expect(dims.w).toBeGreaterThan(0)
  expect(dims.h).toBeGreaterThan(0)
  await expect(img).toHaveAttribute('alt', '一家咖啡店的电商网站 的界面截图')
})

// The other half of the contract: a project that has never been previewed
// has no cover, and the card says how one appears instead of showing a
// broken image.
test('a project that was never opened shows the cover fallback', async ({ page, request }) => {
  await registerVerifyAndLogin(page, request)

  // Build a second project and leave before its preview ever mounts — the
  // frame (and therefore the capture) only exists in the workspace.
  await page.locator('.home-composer textarea').fill('一个设计师的作品集网站')
  await page.getByRole('button', { name: '开始构建' }).click()
  await page.goto('/')
  await page.getByRole('button', { name: '我的项目' }).click()

  const card = page.locator('.dash-card', { hasText: '一个设计师的作品集网站' })
  await expect(card.locator('img.dash-cover-img')).toHaveCount(0)

  // The build finishes server-side; the dashboard does not poll, so reload
  // until the card reports it.
  await expect
    .poll(
      async () => {
        await page.reload()
        await page.getByRole('button', { name: '我的项目' }).click()
        return card.locator('.badge--success').count()
      },
      { timeout: 90_000, intervals: [2000] },
    )
    .toBe(1)

  await expect(card.locator('.dash-cover small')).toHaveText('打开项目后生成封面')
  await expect(card.locator('img.dash-cover-img')).toHaveCount(0)
})
