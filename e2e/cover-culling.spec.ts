import { expect, test } from '@playwright/test'
import { registerVerifyAndLogin } from './auth'

const WEB = 'http://127.0.0.1:5199'
const PROJECTS = 8

// Ticket #31: a mounted cover used to be a running application, so the list
// could only keep the near-viewport ones alive. Covers are screenshots now
// (ticket #56) — one small image per card instead of one whole app — so the
// stronger property holds at any list length: the dashboard mounts no
// application frames at all, and scrolling changes nothing because there is
// nothing to mount.
//
// The sessions are created over the API: this case is about the list under
// many projects, and the create path is covered by the other suites.
test('the dashboard mounts no application frames, whatever the list length', async ({ page, request }) => {
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
  // outside the viewport — which used to be the whole problem.
  await page.setViewportSize({ width: 420, height: 320 })
  await page.goto('/')
  await page.getByRole('button', { name: '我的项目' }).click()

  const cards = page.locator('.dash-card')
  await expect(cards).toHaveCount(PROJECTS)

  // No application runs anywhere in the list: no frames, and no cover images
  // either — these projects were built over the API and never previewed, so
  // each card states how a cover appears.
  await expect(page.locator('.dash-cover iframe')).toHaveCount(0)
  await expect(page.locator('img.dash-cover-img')).toHaveCount(0)
  await expect(cards.first().locator('.dash-cover small')).toHaveText('打开项目后生成封面')

  // Scrolling a far card into view mounts nothing.
  const last = cards.nth(PROJECTS - 1)
  await last.scrollIntoViewIfNeeded()
  await expect(page.locator('.dash-cover iframe')).toHaveCount(0)
  await expect(last.locator('.dash-cover small')).toBeVisible()
})
