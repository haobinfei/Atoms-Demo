import { expect, test } from '@playwright/test'
import { registerVerifyAndLogin } from './auth'

// Ticket #50: light and dark, following the system by default, with a manual
// choice that is remembered.
test('the theme follows the system until the user chooses one', async ({ page, request }) => {
  await registerVerifyAndLogin(page, request)
  const theme = () => page.evaluate(() => document.documentElement.dataset.theme)

  // 跟随系统：模拟系统主题，页面随之切换。
  await page.emulateMedia({ colorScheme: 'light' })
  await page.reload()
  await expect.poll(theme).toBe('light')

  await page.emulateMedia({ colorScheme: 'dark' })
  await page.reload()
  await expect.poll(theme).toBe('dark')

  // 手动选择压过系统，并且被记住。
  await page.emulateMedia({ colorScheme: 'light' })
  await page.reload()
  await page.locator('.menu-trigger').click()
  await page.getByRole('menuitemradio', { name: '深色' }).click()
  await expect.poll(theme).toBe('dark')
  // Closing is the backdrop's job: it covers the trigger while the menu is open.
  await page.locator('.menu-backdrop').click()
  await page.reload()
  await expect.poll(theme).toBe('dark')

  // 回到跟随系统：选择被清掉，重新听系统的。
  await page.locator('.menu-trigger').click()
  await expect(page.getByRole('menuitemradio', { name: '深色' })).toHaveAttribute('aria-checked', 'true')
  await page.getByRole('menuitemradio', { name: '跟随系统' }).click()
  await expect.poll(theme).toBe('light')
  await page.locator('.menu-backdrop').click()
  await page.reload()
  await expect.poll(theme).toBe('light')
})

// "Usable in both themes" stated as something measurable: the page background
// and the text colour have to be on opposite sides of the luminance scale, and
// a real surface has to follow them.
test('both themes keep surfaces and text readable', async ({ page, request }) => {
  await registerVerifyAndLogin(page, request)
  await page.locator('.home-composer textarea').fill('一家咖啡店的电商网站')
  await page.getByRole('button', { name: '开始构建' }).click()
  await expect(page.locator('.tl-item.tl-done').first()).toBeVisible({ timeout: 90_000 })

  const surfaceLuminance = (selector: string, property: 'background-color' | 'color') =>
    page.evaluate(
      ([sel, prop]) => {
        const el = document.querySelector(sel as string)
        if (!el) return -1
        const parts = (getComputedStyle(el).getPropertyValue(prop as string) || '').match(
          /\d+(?:\.\d+)?/g,
        )
        if (!parts || parts.length < 3) return -1
        const [r, g, b] = parts.map(Number)
        return (0.2126 * r + 0.7152 * g + 0.0722 * b) / 255
      },
      [selector, property],
    )

  const token = (name: string) =>
    page.evaluate(
      (n) => getComputedStyle(document.documentElement).getPropertyValue(n).trim(),
      name,
    )

  const cases = [
    { choice: '深色', scheme: 'dark', backgroundIsLight: false },
    { choice: '浅色', scheme: 'light', backgroundIsLight: true },
  ] as const

  for (const item of cases) {
    await page.locator('.menu-trigger').click()
    await page.getByRole('menuitemradio', { name: item.choice }).click()
    await page.locator('.menu-backdrop').click()

    expect(await page.evaluate(() => getComputedStyle(document.documentElement).colorScheme)).toBe(
      item.scheme,
    )
    expect(await token('--c-bg')).not.toBe('')
    expect(await token('--c-text')).not.toBe('')

    const background = await surfaceLuminance('.ws-header', 'background-color')
    expect(background, `${item.choice}: 工作台头部背景`).toBeGreaterThanOrEqual(0)
    if (item.backgroundIsLight) {
      expect(background, `${item.choice}: 背景应当是浅色`).toBeGreaterThan(0.7)
      expect(await surfaceLuminance('.ws-prompt', 'color'), `${item.choice}: 文字应当是深色`).toBeLessThan(0.4)
    } else {
      expect(background, `${item.choice}: 背景应当是深色`).toBeLessThan(0.3)
      expect(await surfaceLuminance('.ws-prompt', 'color'), `${item.choice}: 文字应当是浅色`).toBeGreaterThan(0.6)
    }
  }
})
