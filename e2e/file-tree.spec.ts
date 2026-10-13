import { expect, test } from '@playwright/test'
import { registerVerifyAndLogin } from './auth'

// Ticket #49: the code drawer can find a file by name, and shows numbered,
// coloured source instead of one plain block.
test('the code drawer searches, numbers and colours', async ({ page, request }) => {
  await registerVerifyAndLogin(page, request)
  await page.locator('.home-composer textarea').fill('一家咖啡店的电商网站')
  await page.getByRole('button', { name: '开始构建' }).click()
  await expect(page.locator('.tl-item.tl-done').first()).toBeVisible({ timeout: 90_000 })

  const sessionId = new URL(page.url()).searchParams.get('s') ?? ''
  expect(sessionId).not.toBe('')
  await page.getByRole('button', { name: '代码' }).click()

  const search = page.getByLabel('按文件名搜索')
  await expect(search).toBeVisible()

  const entries = await page.locator('.fd-list button').allInnerTexts()
  expect(entries.length, 'the app has several files').toBeGreaterThanOrEqual(3)

  // Line numbers must line up with the real content for every file: a
  // multi-line comment or template literal that swallowed a newline would
  // show up here as a missing line.
  for (const path of entries) {
    await page.locator('.fd-list button', { hasText: path }).first().click()
    const resp = await page.request.get(
      `/api/sessions/${sessionId}/file/${encodeURIComponent(path)}`,
    )
    expect(resp.status(), `read ${path}`).toBe(200)
    const { content } = (await resp.json()) as { content: string }
    const lineCount = content.split('\n').length

    await expect(page.locator('.fd-line-no').first()).toHaveText('1')
    await expect.poll(async () => page.locator('.fd-line').count()).toBe(lineCount)
    await expect(page.locator('.fd-line-no').last()).toHaveText(String(lineCount))

    // The injected platform SDK opens with a `//` comment, so it is the one
    // file guaranteed to exercise comment colouring.
    if (path.endsWith('.ts') || path.endsWith('.tsx')) {
      await expect(page.locator('.tok-keyword').first(), `keywords in ${path}`).toBeVisible()
      await expect(page.locator('.tok-string').first(), `strings in ${path}`).toBeVisible()
    }
    if (path.endsWith('atomspay.ts')) {
      await expect(page.locator('.tok-comment').first(), 'comments in the SDK').toBeVisible()
    }
  }

  // Searching filters by name, and says so when nothing matches.
  await search.fill('App')
  await expect(page.locator('.fd-list button', { hasText: 'App.tsx' })).toHaveCount(1)
  await expect(page.locator('.fd-list button', { hasText: 'index.html' })).toHaveCount(0)
  await search.fill('zzz-nothing-matches')
  await expect(page.locator('.fd-no-match')).toContainText('没有匹配')
  await expect(page.locator('.fd-list button')).toHaveCount(0)
  await search.fill('')
  await expect(page.locator('.fd-list button', { hasText: 'index.html' })).toHaveCount(1)
})

// A file that cannot be read must say so rather than render the failure as if
// it were source.
test('a file that cannot be read says so', async ({ page, request }) => {
  await registerVerifyAndLogin(page, request)
  await page.locator('.home-composer textarea').fill('一家咖啡店的电商网站')
  await page.getByRole('button', { name: '开始构建' }).click()
  await expect(page.locator('.tl-item.tl-done').first()).toBeVisible({ timeout: 90_000 })

  await page.getByRole('button', { name: '代码' }).click()
  await page.route(/\/api\/sessions\/[^/]+\/file\//, async (route) => {
    await route.fulfill({ status: 500, contentType: 'application/json', body: '{"error":"boom"}' })
  })
  await page.locator('.fd-list button').first().click()

  await expect(page.locator('.fd-error')).toContainText('读不出来')
  await expect(page.locator('.fd-line')).toHaveCount(0)
})
