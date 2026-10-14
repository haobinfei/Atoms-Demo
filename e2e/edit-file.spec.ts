import { expect, test } from '@playwright/test'
import { registerVerifyAndLogin } from './auth'

// Ticket #53: edit a file in the workspace, save, and the preview picks up the
// rebuild. Break it and the error is visible with the text still there.
test('hand-edit a file, save, and watch the preview rebuild', async ({ page, request }) => {
  await registerVerifyAndLogin(page, request)
  await page.locator('.home-composer textarea').fill('一家咖啡店的电商网站')
  await page.getByRole('button', { name: '开始构建' }).click()
  await expect(page.locator('.tl-item.tl-done').first()).toBeVisible({ timeout: 90_000 })

  await page.getByRole('button', { name: '代码' }).click()
  await page.locator('.fd-list button', { hasText: 'src/App.tsx' }).click()
  await expect(page.locator('.fd-code')).toBeVisible()

  const editor = page.locator('.fd-editor')
  const preview = page.frameLocator('.preview-stage iframe')

  // 改一行 → 保存 → 预览变化.
  await page.getByRole('button', { name: '编辑' }).click()
  await editor.fill('export default function App(){ return <main>手改标记A</main> }')
  await page.getByRole('button', { name: '保存并重建' }).click()
  // Saving leaves the editor and comes back to the highlighted view.
  await expect(page.locator('.fd-editor')).toBeHidden({ timeout: 60_000 })
  await expect(preview.locator('main')).toContainText('手改标记A', { timeout: 60_000 })

  // 改坏 → 看到错误 → 可恢复. A save that compiles to nothing keeps the text,
  // says why, and leaves the previous build serving.
  await page.getByRole('button', { name: '编辑' }).click()
  const broken = 'export default function App() { this is not valid tsx <<<'
  await editor.fill(broken)
  await page.getByRole('button', { name: '保存并重建' }).click()
  await expect(page.locator('.fd-error')).toContainText('重建失败', { timeout: 60_000 })
  // The preview is still the last build that worked, not a blank frame.
  await expect(preview.locator('main')).toContainText('手改标记A')

  // 可恢复: the text the user wrote is still there to fix.
  await page.getByRole('button', { name: '编辑' }).click()
  await expect(editor).toHaveValue(/not valid tsx/)
  await editor.fill('export default function App(){ return <main>修好了B</main> }')
  await page.getByRole('button', { name: '保存并重建' }).click()
  await expect(preview.locator('main')).toContainText('修好了B', { timeout: 60_000 })
})
