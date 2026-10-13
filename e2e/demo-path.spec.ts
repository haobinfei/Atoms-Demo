import { expect, test } from '@playwright/test'
import { E2E_PASSWORD, registerVerifyAndLogin } from './auth'

const PROMPT = '一家咖啡店的电商网站'
const LAYOUT_TWEAK = '把首页布局调整得更紧凑'
const THEME = '把主题色换成绿色'

// Ticket #35: the 演示黄金路径 from docs/design/atoms-dev-gap-and-optimization.md
// §6.1, frozen as one case so a regression in any single step fails here and
// names itself. Every step is a real action against the real server — no
// preloaded data, no skipped steps, no copy changed to make it pass.
//
// The E2E server slows the 流水线 stages so a 回合 is still running while the
// browser refreshes, queues and cancels. That is the only concession: the
// timing is widened, nothing is stubbed.
test('演示路径：登录 → 生成 → 刷新 → 排队 → 取消 → 追问 → 视口 → 发布', async ({
  page,
  request,
}) => {
  // One step observes 60s of silence on purpose.
  test.setTimeout(240_000)
  // Bound every action. A multi-step case that hangs would otherwise burn the
  // whole test budget and report nothing about which step it was in.
  page.setDefaultTimeout(30_000)

  let sessionId = ''
  let beforeCancel = ''

  await test.step('1 登录已有账户：项目列表与账户菜单可用', async () => {
    const { email } = await registerVerifyAndLogin(page, request)

    // The step is about an existing account, so sign out and back in rather
    // than counting the registration's own login.
    await page.locator('.menu-trigger').click()
    await page.getByRole('menuitem', { name: '退出登录' }).click()
    await expect(page.getByRole('heading', { name: '登录 Atoms' })).toBeVisible()
    await page.getByLabel('邮箱').fill(email)
    await page.getByLabel('密码', { exact: true }).fill(E2E_PASSWORD)
    await page.getByRole('button', { name: '登录', exact: true }).click()

    await page.getByRole('button', { name: '我的项目' }).click()
    await expect(page.locator('.dash')).toBeVisible()
    await page.locator('.menu-trigger').click()
    await expect(page.getByRole('menuitem', { name: '账户设置' })).toBeVisible()
    await expect(page.getByRole('menuitem', { name: '退出登录' })).toBeVisible()
    // Close it by clicking the backdrop, not the trigger: the backdrop
    // (z-index 39) is a sibling of the trigger and covers it while open, so a
    // second trigger click never lands.
    await page.locator('.menu-backdrop').click()
    await expect(page.getByRole('menuitem', { name: '账户设置' })).toHaveCount(0)
  })

  await test.step('2 新建项目并输入一句需求：时间线按回合分组', async () => {
    // Scoped to the tab: the empty project list also offers a 新建项目 button,
    // so an unscoped role query is ambiguous.
    await page.locator('.home-tabs .tab', { hasText: '新建项目' }).click()
    await page.locator('.home-composer textarea').fill(PROMPT)
    await page.getByRole('button', { name: '开始构建' }).click()
    await expect(page.locator('.tl-turn-result').first()).toContainText('本轮完成', {
      timeout: 90_000,
    })

    const first = page.locator('.tl-turn').first()
    await expect(first).toContainText('第 1 回合')
    await expect(first.locator('.tl-user-bubble')).toHaveText(PROMPT)
    // 本轮修改了哪些文件 is answerable without scrolling a wall of paths.
    await expect(first.locator('.tl-files summary')).toContainText('修改了')

    const src = (await page.locator('.preview-stage iframe').getAttribute('src')) ?? ''
    sessionId = src.match(/\/app\/([^/]+)\//)?.[1] ?? ''
    expect(sessionId, 'session id from the preview address').not.toBe('')
  })

  await test.step('3 构建中刷新页面：项目、消息与构建状态全部保留', async () => {
    await page.locator('.ws-chat-form textarea').fill(LAYOUT_TWEAK)
    await page.locator('.ws-chat-form textarea').press('Enter')

    // The precondition of this step: the refresh really does land mid-build.
    await expect(page.locator('.ws-status')).toHaveText('构建中')
    await expect(page.locator('.tl-item.tl-plan').nth(1)).toBeVisible({ timeout: 60_000 })

    await page.reload()

    // 项目 and 消息 came back...
    await expect(page.locator('.ws-prompt')).toHaveText(PROMPT)
    await expect(page.locator('.tl-user-bubble', { hasText: LAYOUT_TWEAK })).toBeVisible()
    // ...the progress that had already happened is still on the timeline...
    await expect(page.locator('.tl-item.tl-plan').nth(1)).toBeVisible()
    // ...the 构建状态 is the real one rather than a blank, and the 回合 carries
    // on to completion from where the refresh found it.
    await expect(page.locator('.ws-status')).toHaveText(/构建中|构建完成/)
    await expect(page.locator('.tl-turn-result').nth(1)).toContainText('本轮完成', {
      timeout: 90_000,
    })
  })

  await test.step('4 构建中提交追问：消息进入排队，不打断当前回合', async () => {
    await page.locator('.ws-chat-form textarea').fill(LAYOUT_TWEAK)
    await page.locator('.ws-chat-form textarea').press('Enter')
    await expect(page.locator('.ws-status')).toHaveText('构建中')

    await page.locator('.ws-chat-form textarea').fill(THEME)
    await page.locator('.ws-chat-form textarea').press('Enter')

    await expect(page.locator('.ws-queue')).toContainText(THEME)
    await expect(page.locator('.ws-queue-tag')).toHaveText('排队中')
    // Not sent: the 回合 in flight was not interrupted.
    await expect(page.locator('.tl-user-bubble', { hasText: THEME })).toHaveCount(0)
  })

  await test.step('5 尝试取消回合：上一版产物仍在，会话回到就绪', async () => {
    const appURL = `/app/${sessionId}/`
    const before = await page.request.get(appURL)
    expect(before.status()).toBe(200)
    beforeCancel = await before.text()

    await page.getByRole('button', { name: '取消本回合' }).click()

    // 就绪, not 失败: a cancel is the user's decision, not a broken build.
    await expect(page.locator('.ws-status')).toHaveText('构建完成', { timeout: 60_000 })
    await expect(page.locator('.tl-item.tl-cancel')).toContainText('取消回合')
    await expect(page.locator('.tl-turn-result').nth(2)).toContainText('本轮已取消')

    // The previous version is served byte for byte.
    const after = await page.request.get(appURL)
    expect(after.status()).toBe(200)
    expect(await after.text()).toBe(beforeCancel)

    // The queued message was not the cancelled 回合's to take with it.
    await expect(page.locator('.ws-queue')).toContainText(THEME)
    await expect(page.locator('.tl-user-bubble', { hasText: THEME })).toHaveCount(0)
  })

  await test.step('6 发送追问修改主题：预览刷新一次且仅一次，随后 60 秒不被打断', async () => {
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

    // Sending the retained message is the user deciding it, which is what
    // ADR-0013 left the queue for.
    await page.getByRole('button', { name: `发送排队消息：${THEME}` }).click()
    await expect(page.locator('.tl-user-bubble', { hasText: THEME })).toBeVisible()
    await expect(page.locator('.tl-turn-result').nth(3)).toContainText('本轮完成', {
      timeout: 90_000,
    })

    const loads = () =>
      page.evaluate(() => (window as unknown as { __frameLoads: number }).__frameLoads)
    await expect.poll(loads, { message: '预览必须自动刷新一次' }).toBe(1)

    // Observing that an event never arrives needs a real wall-clock window.
    await page.waitForTimeout(60_000)
    expect(await loads(), '连续操作 60 秒不得被重载打断').toBe(1)

    // And it is still live: the app responds to input.
    const frame = page.frameLocator('.frame')
    await frame.getByRole('button', { name: '加入购物车' }).first().click()
    await expect(frame.getByRole('button', { name: '购物车（1）' })).toBeVisible()
  })

  await test.step('7 切换桌面/移动视口：切换正确，布局无破版', async () => {
    await page.getByRole('button', { name: '移动', exact: true }).click()
    await expect(page.locator('.frame.mobile')).toBeVisible()

    // "无破版", measured rather than eyeballed.
    const fits = await page.evaluate(() => {
      const el = document.querySelector('.ws-body')
      return el === null || el.scrollWidth <= el.clientWidth + 1
    })
    expect(fits, '移动视口下工作台不得横向溢出').toBe(true)

    await page.getByRole('button', { name: '桌面', exact: true }).click()
    await expect(page.locator('.frame:not(.mobile)')).toBeVisible()
  })

  await test.step('8 发布：等待期间有进行中反馈，二维码与公开链接生成', async () => {
    // The rebuild can be fast enough to miss a single screenshot, so watch for
    // the progress state rather than sampling for it.
    await page.evaluate(() => {
      const w = window as unknown as { __sawPublishProgress: boolean }
      w.__sawPublishProgress = false
      const seen = () => {
        if (document.querySelector('.pub-progress')) w.__sawPublishProgress = true
      }
      new MutationObserver(seen).observe(document.body, { childList: true, subtree: true })
      seen()
    })

    await page.getByRole('button', { name: '发布', exact: true }).click()
    await page.getByRole('button', { name: '发布 v1', exact: true }).click()

    await expect(page.getByAltText('应用二维码')).toBeVisible({ timeout: 90_000 })
    await expect(page.locator('.ws-chat-error')).toHaveCount(0)
    expect(
      await page.evaluate(
        () => (window as unknown as { __sawPublishProgress: boolean }).__sawPublishProgress,
      ),
      '发布等待期间必须有进行中反馈',
    ).toBe(true)

    const publicURL = await page.locator('.pub-link').innerText()
    expect(publicURL).toContain(`/app/${sessionId}/`)

    await page.getByRole('button', { name: '关闭', exact: true }).click()
    await expect(page.locator('.preview-badge')).toHaveText('已发布版')
  })
})
