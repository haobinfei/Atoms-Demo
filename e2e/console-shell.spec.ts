import { expect, test } from '@playwright/test'
import { registerVerifyAndLogin } from './auth'

// Ticket #70 / ADR-0017: 控制台外壳. The list and management pages sit in a
// sidebar + top bar; a 项目 workspace folds the sidebar away but keeps the top
// bar, because the account menu (and the theme choice inside it) has to stay
// reachable while working.
test('the console shell wraps the list and management pages', async ({ page, request }) => {
  await registerVerifyAndLogin(page, request)

  // 登录后落在控制台：侧栏在，顶栏报出当前页名。
  await expect(page.locator('.app-sidebar')).toBeVisible()
  await expect(page.locator('.header-title')).toHaveText('项目总览')

  // 导航项一个不少，且当前项被标出。新账户是个人工作区，所以没有成员与平台管理。
  const nav = page.locator('.app-nav button')
  await expect(nav).toHaveCount(3)
  for (const label of ['项目', '工作区', '账户']) {
    await expect(page.locator('.app-nav button', { hasText: label })).toBeVisible()
  }
  await expect(page.locator('.app-nav button.active')).toHaveText('项目')

  // 切页：外壳不动，只有页名与高亮跟着走。
  await page.getByRole('button', { name: '工作区', exact: true }).click()
  await expect(page.locator('.app-sidebar')).toBeVisible()
  await expect(page.locator('.header-title')).toHaveText('工作区')
  await expect(page.locator('.app-nav button.active')).toHaveText('工作区')

  // 工作区切换器在侧栏里，而不是顶栏。
  await expect(page.locator('.app-sidebar').getByLabel('当前工作区')).toBeVisible()

  // 账户菜单要能被遮罩关掉。窄屏下外壳多了一条导航带，菜单面板跟着下移，
  // 曾经压住遮罩的中心点，把这次点击截走——所以这条断言必须留在窄屏上跑。
  await page.locator('.menu-trigger').click()
  await expect(page.getByRole('menuitem', { name: '账户设置' })).toBeVisible()
  await page.locator('.menu-backdrop').click()
  await expect(page.getByRole('menuitem', { name: '账户设置' })).toHaveCount(0)
})

// 项目总览 is the console's list page: the cards are kept, and the 概览统计 are
// counted from that same list rather than from a second source (ADR-0017).
test('the projects overview keeps its cards and counts them', async ({ page, request }) => {
  await registerVerifyAndLogin(page, request)
  await page.locator('.home-composer textarea').fill('一个记录每日待办的小工具')
  await page.getByRole('button', { name: '开始构建' }).click()
  await expect(page.locator('.tl-item.tl-done').first()).toBeVisible({ timeout: 90_000 })

  await page.getByRole('button', { name: '返回首页' }).click()
  await page.getByRole('button', { name: '我的项目' }).click()

  const cards = page.locator('.dash-card')
  await expect(cards).toHaveCount(1)
  await expect(page.locator('.dash-title')).toHaveText('项目总览')

  // 统计与列表同源：项目总数就是卡片数。
  const cardCount = await cards.count()
  const total = page.locator('.dash-stat', { hasText: '项目总数' }).locator('.dash-stat-value')
  await expect(total).toHaveText(String(cardCount))

  // 搜索时结果被筛过，统计会误导，所以此时不展示。
  await page.locator('.dash-search').fill('不存在的项目名')
  await expect(page.locator('.dash-stats')).toHaveCount(0)
  await expect(page.locator('.dash-empty-title')).toHaveText('没有匹配的项目')
})

// 工作台 (Workspace) takes the whole window back, and gives the console back
// when the user leaves.
test('a 项目 workspace folds the shell away and restores it on exit', async ({ page, request }) => {
  await registerVerifyAndLogin(page, request)
  await page.locator('.home-composer textarea').fill('一家咖啡店的电商网站')
  await page.getByRole('button', { name: '开始构建' }).click()
  await expect(page.locator('.tl-item.tl-done').first()).toBeVisible({ timeout: 90_000 })

  // 侧栏收起，但顶栏还在——主题与账户菜单必须仍然够得着。
  await expect(page.locator('.app-sidebar')).toHaveCount(0)
  await expect(page.locator('.menu-trigger')).toBeVisible()
  await page.locator('.menu-trigger').click()
  await expect(page.getByRole('menuitemradio', { name: '深色' })).toBeVisible()
  await page.locator('.menu-backdrop').click()

  // 工作台占满除顶栏之外的整个视口。
  const fits = await page.evaluate(() => {
    const el = document.querySelector('.workspace')
    return el === null ? -1 : Math.round(el.getBoundingClientRect().height)
  })
  const expected = await page.evaluate(() => window.innerHeight - 56)
  expect(fits).toBe(expected)

  // 退出后控制台回来。
  await page.getByRole('button', { name: '返回首页' }).click()
  await expect(page.locator('.app-sidebar')).toBeVisible()
  await expect(page.locator('.header-title')).toHaveText('项目总览')
})
