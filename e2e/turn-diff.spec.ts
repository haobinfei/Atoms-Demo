import { expect, test } from '@playwright/test'
import { registerVerifyAndLogin } from './auth'

// Ticket #54: every 文件事件 in the timeline is a 改动记录. The list says who
// wrote each file — an Agent 改动 or a 用户手改 — how many lines moved, and
// serves the diff only when a row is opened.
test('turn files carry author badges, line counts and an on-demand diff', async ({ page, request }) => {
  await registerVerifyAndLogin(page, request)
  await page.locator('.home-composer textarea').fill('一家咖啡店的电商网站')
  await page.getByRole('button', { name: '开始构建' }).click()
  await expect(page.locator('.tl-item.tl-done').first()).toBeVisible({ timeout: 90_000 })

  const files1 = page.locator('.tl-turn').first().locator('.tl-files')

  // The first build is four brand-new files, every one of them the Agent's.
  await expect(files1.locator('summary')).toContainText('修改了 4 个文件')
  await expect(files1.locator('.tl-files-note')).toHaveCount(0)
  await files1.locator('summary').click()

  const appRow = files1.locator('.tl-file-row', { hasText: 'src/App.tsx' })
  await expect(appRow.locator('.tl-author')).toHaveText('Agent 改动')
  // The minus is U+2212, the same glyph the row renders.
  await expect(appRow.locator('.tl-file-stat')).toHaveText(/^\+\d+ −\d+$/)

  // Opening a row fetches that file's diff — path header, hunk header, and
  // the lines themselves. Nothing is fetched until the row is opened. A file
  // the build created has "--- /dev/null" as its first header line, so the
  // path is asserted on the diff as a whole.
  await appRow.locator('.tl-file-toggle').click()
  const diff = appRow.locator('.fd-diff')
  await expect(diff).toBeVisible()
  await expect(diff).toContainText('src/App.tsx')
  await expect(diff.locator('.fd-diff-hunk').first()).toContainText('@@')
  await expect(diff.locator('.fd-diff-add').filter({ hasText: 'export default function App' })).toHaveCount(1)

  // Collapsing puts the diff away again.
  await appRow.locator('.tl-file-toggle').click()
  await expect(appRow.locator('.fd-diff')).toHaveCount(0)

  // A follow-up turn rewrites one file and leaves the others byte-identical.
  // The unchanged ones are counted, not listed as changes.
  await page.locator('.ws-chat-form textarea').fill('把主题色换成绿色')
  await page.locator('.ws-chat-form button[type="submit"]').click()
  await expect(page.locator('.tl-item.tl-done').nth(1)).toBeVisible({ timeout: 90_000 })

  const files2 = page.locator('.tl-turn').nth(1).locator('.tl-files')
  await expect(files2.locator('summary')).toContainText('修改了 1 个文件')
  await expect(files2.locator('.tl-files-note')).toContainText('3 个内容未变')
  await files2.locator('summary').click()
  await expect(files2.locator('.tl-file-row')).toHaveCount(1)
  await expect(files2.locator('.tl-file-row').locator('.tl-author')).toHaveText('Agent 改动')

  // A hand edit joins the same turn as the user's own change, and its diff
  // shows the line the user actually wrote.
  await page.getByRole('button', { name: '代码' }).click()
  await page.locator('.fd-list button', { hasText: 'src/App.tsx' }).click()
  await page.getByRole('button', { name: '编辑' }).click()
  const marker = '手改差异标记'
  await page.locator('.fd-editor').fill(`export default function App(){ return <main>${marker}</main> }`)
  await page.getByRole('button', { name: '保存并重建' }).click()
  await expect(page.locator('.fd-saved')).toBeVisible({ timeout: 60_000 })
  await page.getByRole('button', { name: '关闭' }).click()

  await expect(files2.locator('summary')).toContainText('修改了 2 个文件')
  const handRow = files2.locator('.tl-file-row', { hasText: 'src/App.tsx' })
  await expect(handRow.locator('.tl-author')).toHaveText('用户手改')
  await handRow.locator('.tl-file-toggle').click()
  await expect(handRow.locator('.fd-diff-add').filter({ hasText: marker })).toBeVisible()
})
