/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
/*
 * 首页把「管理员配的自定义首页内容」交回上游那一支。
 *
 * # 这条测试守的是什么
 *
 * 路由 `/` 从上游的 `<Home>` 换成二开的 `<QyLanding>` 之后，后台
 * 「设置 → 首页内容」这个功能的存亡就完全取决于 `QyLanding` 里那一句
 * `if (content) return <Home />`。删掉它不会有任何编译错误、也不会白屏 ——
 * 表现是**管理员配的整页内容凭空消失**，而站点仍然显示一张漂亮的默认落地页，
 * 没有人会立刻意识到是首页坏了。
 *
 * 所以两支都要钉：配了内容显示内容，没配才显示二开落地页。
 *
 * # 为什么整页真渲染而不是断言源码
 *
 * 这一支的失效方式包括「渲染了但被包在错误的布局里」「上游 Home 自己那层
 * isLoaded 门槛把内容挡住了」，源码扫描一条都看不出来。挡在网络边界上的是
 * axios 适配器（唯一被 mock 的东西），页面、布局、上游 Home 全是真的。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { Window } from 'happy-dom'

const domWindow = new Window({ width: 1280, height: 900 })
for (const key of [
  'window',
  'document',
  'navigator',
  'localStorage',
  'sessionStorage',
  'HTMLElement',
  'HTMLDivElement',
  'Node',
  'Element',
  'Event',
  'CustomEvent',
  'MutationObserver',
  'ResizeObserver',
  'IntersectionObserver',
  'requestAnimationFrame',
  'cancelAnimationFrame',
  'getComputedStyle',
  'matchMedia',
  'DOMRect',
  'customElements',
  'CSS',
] as const) {
  const value = domWindow[key as keyof Window]
  if (value === undefined) continue
  Object.defineProperty(globalThis, key, { configurable: true, value })
}

// happy-dom 没有 scrollTo，而 TanStack Router 挂载时就会调用它做滚动恢复；
// 缺了它整棵树会掉进 CatchBoundary，测的就不再是页面而是错误边界了。
for (const target of [globalThis, domWindow] as unknown as Record<
  string,
  unknown
>[]) {
  target.scrollTo = () => {}
}

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const i18next = (await import('i18next')).default
const { initReactI18next } = await import('react-i18next')
const qyEn = (await import('@/i18n/qy/en.json')).default
await i18next
  .use(initReactI18next)
  .init({ lng: 'en', resources: { en: { translation: qyEn } } })

const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const {
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
} = await import('@tanstack/react-router')
const { api } = await import('@/lib/api')
const { QyLanding } = await import('..')

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

/** 当前这一轮里 `/api/home_page_content` 要回答什么。 */
let homeContent = ''

/*
 * 唯一的 mock 点：网络。适配器按 URL 分流，其余端点一律回空成功响应 ——
 * 页头/页脚的 `/api/status` 之类不是这条测试的对象，但它们必须能返回，
 * 否则整页会卡在各自的加载态上。
 */
api.defaults.adapter = async (config) => ({
  data: config.url?.includes('home_page_content')
    ? { success: true, data: homeContent }
    : { success: true, data: null },
  status: 200,
  statusText: 'OK',
  headers: {},
  config,
})

const rootRoute = createRootRoute({ component: Outlet })
const routeTree = rootRoute.addChildren([
  createRoute({
    getParentRoute: () => rootRoute,
    path: '/',
    component: QyLanding,
  }),
])

const mounted: {
  container: HTMLDivElement
  root: ReturnType<typeof createRoot>
}[] = []

after(async () => {
  for (const { container, root } of mounted) {
    await act(async () => root.unmount())
    container.remove()
  }
})

async function renderHome(content: string): Promise<HTMLElement> {
  homeContent = content
  localStorage.clear()

  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  // 首屏信号板的目录预置成空：它自己的兜底由 signal-board 那份用例守，
  // 这里预置只是为了让它不再发一次异步请求，免得断言跑在半途的那一帧上。
  queryClient.setQueryData(['pricing'], { success: true, data: [] })
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(
      <QueryClientProvider client={queryClient}>
        {/* 路由树是本地拼的，与仓库里那棵生成树不同 —— 类型对不上是预期的。 */}
        <RouterProvider router={router as never} />
      </QueryClientProvider>
    )
  })
  // 首页内容是 effect 里取的，等一轮微任务让 setState 落地。
  await act(async () => {
    await Promise.resolve()
  })
  mounted.push({ container, root })
  return container
}

describe('首页与管理员自定义内容', () => {
  test('管理员配了内容时显示那份内容，而不是二开落地页', async () => {
    const container = await renderHome('# 停机维护公告')
    const text = container.textContent ?? ''
    assert.match(text, /停机维护公告/)
    assert.doesNotMatch(text, /Purity is the first rule/)
  })

  test('没配内容时显示二开落地页的承诺区段', async () => {
    const container = await renderHome('')
    const text = container.textContent ?? ''
    assert.match(text, /Purity is the first rule/)
    assert.match(text, /The star you picked is the star you get/)
  })
})
