import { randomUUID } from 'node:crypto'
import { expect, test } from '@playwright/test'
import { mailLink, registerVerifyAndLogin } from './auth'

test('team invitation, role changes, last-owner protection and editor exit keep login', async ({ page, request, context }) => {
  const owner = await registerVerifyAndLogin(page, request)
  const editorContext = await context.browser()!.newContext({ viewport: page.viewportSize()! })
  try {
    const editorPage = await editorContext.newPage()
    const editor = await registerVerifyAndLogin(editorPage, request)
    const name = `Team ${randomUUID()}`
    await page.getByRole('button', { name: '工作区', exact: true }).click()
    await page.getByLabel('名称', { exact: true }).fill(name)
    await page.getByRole('button', { name: '创建工作区', exact: true }).click()
    await expect(page.getByRole('status')).toContainText('团队工作区已创建')
    await page.getByLabel('当前工作区').selectOption({ label: name })
    await page.getByRole('button', { name: '成员', exact: true }).click()
    page.once('dialog', (dialog) => void dialog.accept())
    await page.getByRole('button', { name: '退出工作区', exact: true }).click()
    await expect(page.getByRole('alert')).toContainText('409')
    await page.getByLabel('邮箱', { exact: true }).fill(editor.email)
    await page.getByRole('button', { name: '发送邀请', exact: true }).click()
    await expect(page.getByRole('status')).toContainText('邀请已发送')

    await editorPage.goto(await mailLink(request, editor.email, 'invite'))
    await editorPage.getByRole('button', { name: '接受邀请', exact: true }).click()
    await expect(editorPage.getByRole('alert')).toContainText('已加入工作区')
    await editorPage.getByLabel('当前工作区').selectOption({ label: name })
    await editorPage.getByRole('button', { name: '成员', exact: true }).click()
    await expect(editorPage.getByRole('heading', { name, exact: true })).toBeVisible()
    await expect(editorPage.getByRole('button', { name: '发送邀请', exact: true })).toHaveCount(0)

    await page.reload()
    await page.getByRole('button', { name: '成员', exact: true }).click()
    await page.getByLabel(`${editor.email} 的角色`, { exact: true }).selectOption('owner')
    await expect(page.getByRole('status')).toContainText('成员角色已更新')
    await editorPage.reload()
    await editorPage.getByRole('button', { name: '成员', exact: true }).click()
    await expect(editorPage.getByRole('button', { name: '发送邀请', exact: true })).toBeVisible()
    await page.getByLabel(`${editor.email} 的角色`, { exact: true }).selectOption('editor')
    await expect(page.getByRole('status')).toContainText('成员角色已更新')

    await editorPage.reload()
    await editorPage.getByRole('button', { name: '成员', exact: true }).click()
    editorPage.once('dialog', (dialog) => void dialog.accept())
    await editorPage.getByRole('button', { name: '退出工作区', exact: true }).click()
    await expect(editorPage.locator('.app-email')).toHaveText(editor.email)
    await expect(editorPage.getByLabel('当前工作区').locator('option')).toHaveCount(1)
    await expect(editorPage.getByRole('button', { name: '成员', exact: true })).toHaveCount(0)
    await expect(page.locator('.app-email')).toHaveText(owner.email)
  } finally {
    await editorContext.close()
  }
})
