import { expect, test } from '@playwright/test'
import { registerVerifyAndLogin } from './auth'

// Ticket #26: a ready build must not be force-reloaded by the reconcile poll,
// and "在新标签页打开" must open the version being previewed.
test('a ready build is not reloaded by the reconcile poll', async ({ page, request }) => {
  await registerVerifyAndLogin(page, request)
  await page.locator('.home-composer textarea').fill('一家咖啡店的电商网站')
  await page.getByRole('button', { name: '开始构建' }).click()

  const frame = page.frameLocator('.frame')
  await expect(page.locator('.tl-item.tl-done').first()).toBeVisible({ timeout: 60_000 })
  await expect(frame.getByText('用心挑选的每一款')).toBeVisible()

  // Count iframe loads from here on. The reconcile poll ticks every 15s, so
  // 35s covers two ticks: a ready session must produce none of them.
  await page.evaluate(() => {
    const w = window as unknown as { __frameLoads: number }
    w.__frameLoads = 0
    document.addEventListener(
      'load',
      (ev) => {
        if (ev.target instanceof HTMLIFrameElement) w.__frameLoads += 1
      },
      true,
    )
  })
  // Observing that an event never arrives needs a real wall-clock window;
  // there is no condition to poll on.
  await page.waitForTimeout(35_000)
  const loads = await page.evaluate(
    () => (window as unknown as { __frameLoads: number }).__frameLoads,
  )
  expect(loads, 'the reconcile poll must not reload a ready preview').toBe(0)

  // The frame is still live and interactive after the wait.
  await frame.getByRole('button', { name: '加入购物车' }).first().click()
  await expect(frame.getByRole('button', { name: '购物车（1）' })).toBeVisible()
})

test('open in new tab targets the version being previewed', async ({ page, request }) => {
  await registerVerifyAndLogin(page, request)
  await page.locator('.home-composer textarea').fill('一家咖啡店的电商网站')
  await page.getByRole('button', { name: '开始构建' }).click()
  await expect(page.locator('.tl-item.tl-done').first()).toBeVisible({ timeout: 60_000 })

  const href = await page.locator('.preview-actions a').getAttribute('href')
  const src = await page.locator('.preview-stage .frame').getAttribute('src')
  expect(href, 'the open link must have an address').toBeTruthy()
  // The tab and the frame must resolve to the same app path; the frame only
  // appends the build marker as a query string. A contest candidate carries its
  // own archived path, which the same rule keeps in sync.
  expect(src!.split('?')[0]).toBe(href)
})
