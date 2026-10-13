import { expect, test } from '@playwright/test'
import { registerVerifyAndLogin } from './auth'

const WEB = 'http://127.0.0.1:5199'
const PROJECTS = 8

// Ticket #31: a mounted cover is a running application, so the list must only
// keep alive the ones near the viewport.
//
// The sessions are created over the API: this case is about the list under
// many projects, and the create path is covered by the other suites.
test('only covers near the viewport keep a live preview', async ({ page, request }) => {
  await registerVerifyAndLogin(page, request)
  for (let i = 0; i < PROJECTS; i++) {
    const resp = await page.request.post(`${WEB}/api/sessions`, {
      data: { prompt: `第 ${i + 1} 个项目` },
      // Write requests carry a same-origin guard (origin header, or a
      // same-origin fetch metadata hint). A raw API client sends neither, so
      // state it explicitly rather than being rejected with 403.
      headers: { 'Sec-Fetch-Site': 'same-origin' },
    })
    expect(resp.status(), `create project ${i + 1}`).toBe(200)
  }
  // Every build must land before the covers can show anything.
  await expect
    .poll(
      async () => {
        const resp = await page.request.get(`${WEB}/api/sessions/list`)
        const body = (await resp.json()) as { sessions: { previewReady: boolean }[] }
        return body.sessions.filter((s) => s.previewReady).length
      },
      { timeout: 120_000 },
    )
    .toBe(PROJECTS)

  // A short, narrow viewport makes the grid one column, so most cards sit well
  // outside the viewport and must stay unmounted.
  await page.setViewportSize({ width: 420, height: 320 })
  await page.goto('/')
  await page.getByRole('button', { name: '我的项目' }).click()

  const cards = page.locator('.dash-card')
  await expect(cards).toHaveCount(PROJECTS)

  // The visible cards run their app; the rest do not.
  const mounted = page.locator('.dash-cover-frame')
  await expect.poll(async () => mounted.count()).toBeGreaterThan(0)
  expect(await mounted.count()).toBeLessThan(PROJECTS)

  // Scrolling a culled card into view mounts its frame.
  const last = cards.nth(PROJECTS - 1)
  await last.scrollIntoViewIfNeeded()
  await expect(last.locator('.dash-cover-frame')).toBeVisible()
})
