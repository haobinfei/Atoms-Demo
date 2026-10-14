import { expect, test } from '@playwright/test'
import { registerVerifyAndLogin } from './auth'
import { stubSession } from './stub-session'

// Ticket #27 / ADR-0014: preview availability is decided server-side, so
// neither the project list nor the workspace ever mounts a frame that has
// nothing to serve (and therefore never shows an error page inside the
// preview).
//
// The cover is a screenshot now (ticket #56), and a screenshot can only exist
// for a servable build — so the same gate decides whether the card attempts a
// cover image at all. This case drives the list with stubbed readiness states
// and counts the attempts: a 构建中 project asks for nothing, a servable one
// asks exactly once.
test('the cover is only attempted for a servable build', async ({ page, request }) => {
  await registerVerifyAndLogin(page, request)
  let thumbAttempts = 0
  await page.route(/\/api\/sessions\/[^/]+\/thumbnail$/, async (route) => {
    thumbAttempts++
    // The honest answer for a project that was never previewed.
    await route.fulfill({ status: 404, contentType: 'application/json', body: '{"error":"no cover yet"}' })
  })
  // The newest registration wins, so each call re-arms the list.
  const armList = (previewReady: boolean, status: string) =>
    page.route('**/api/sessions/list', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          sessions: [
            {
              id: 'stub-cover',
              name: '封面项目',
              category: 'site',
              status,
              pinned: false,
              prompt: '封面项目',
              updatedAt: new Date().toISOString(),
              previewReady,
            },
          ],
        }),
      })
    })

  await armList(false, 'building')
  await page.goto('/')
  await page.getByRole('button', { name: '我的项目' }).click()
  const card = page.locator('.dash-card').first()
  await expect(card.locator('.dash-cover small')).toHaveText('正在构建')
  expect(thumbAttempts, 'a project without a servable build must not ask for a cover').toBe(0)

  await armList(true, 'ready')
  await page.reload()
  await page.getByRole('button', { name: '我的项目' }).click()
  await expect(card.locator('.dash-cover small')).toHaveText('打开项目后生成封面')
  expect(thumbAttempts, 'a servable build asks for its cover exactly once').toBe(1)
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

  // Ready, but the build output is gone: the placeholder replaces the frame.
  await stubSession(page, { status: 'ready', previewReady: false, events: [] })
  await page.reload()

  await expect(page.locator('.preview-idle')).toBeVisible()
  await expect(page.locator('.preview-stage .frame')).toHaveCount(0)
})
