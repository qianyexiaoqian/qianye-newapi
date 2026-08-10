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
 * QyRestrictedGate —— 登录后主内容区的受限闸，在**真实路由器**里跑。
 *
 * # 守什么
 *
 *   1. 受限账号打开白名单之外的页面（这里用 `/wallet`）时，页面本身**不渲染**，
 *      换成受限落地页。只测 `isQyRestrictedPathAllowed` 是不够的：闸把
 *      `props.children` 无条件渲染出来，那个纯函数测试照样全绿。
 *   2. 受限账号打开白名单里的页面（`/qy/tickets`）时页面照常渲染，且横幅仍在 ——
 *      横幅是"持续可见"的，不是落地页的一部分。
 *   3. 正常账号：页面渲染、横幅一个字都没有。把所有人的界面改坏与漏放受限用户
 *      同样严重。
 *
 * i18n 用空 resources 初始化，`t('x')` 回落成键名本身，所以断言直接打在键名上。
 * 这样文案改写不会把测试改红，而键被删掉会。
 */
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import { Window } from 'happy-dom'

const domWindow = new Window()
const domGlobals = [
  'window',
  'document',
  'navigator',
  'HTMLElement',
  'Node',
  'Element',
  'Event',
  'CustomEvent',
  'MutationObserver',
  'requestAnimationFrame',
  'cancelAnimationFrame',
  'getComputedStyle',
  'localStorage',
] as const

for (const key of domGlobals) {
  Object.defineProperty(globalThis, key, {
    configurable: true,
    value: domWindow[key],
  })
}

// happy-dom 没有实现 scrollTo，而路由器每次导航都会调它 —— 缺了它会在导航中途
// 抛错，整棵树掉进 CatchBoundary，看起来像是断言写错了。
for (const target of [globalThis, domWindow]) {
  Object.defineProperty(target, 'scrollTo', {
    configurable: true,
    value: () => {},
  })
}

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const i18next = (await import('i18next')).default
const { initReactI18next } = await import('react-i18next')
await i18next
  .use(initReactI18next)
  .init({ lng: 'en', resources: { en: { translation: {} } } })

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
const { QyRestrictedGate } = await import('../qy-restricted-gate')
const { qyKeys } = await import('../../lib/query-keys')
const { QY_DISABLED_CONFIG } = await import('../../lib/config-query')
const { useAuthStore } = await import('@/stores/auth-store')
const { USER_STATUS } = await import('@/features/users/constants')

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

const ENABLED_CONFIG = {
  ...QY_DISABLED_CONFIG,
  enabled: true,
  available: true,
  features: {
    transfer: true,
    commission: true,
    withdraw: true,
    availability: true,
    violation: true,
    lottery: true,
    ticket: true,
    group_matrix: true,
  },
}

/** 被闸包住的页面。渲染出来即证明闸放行了。 */
function PageBody() {
  return <div data-testid='page-body'>page body</div>
}

function Shell() {
  return (
    <QyRestrictedGate>
      <PageBody />
    </QyRestrictedGate>
  )
}

const rootRoute = createRootRoute({ component: Outlet })
const routeTree = rootRoute.addChildren([
  createRoute({
    getParentRoute: () => rootRoute,
    path: '/wallet',
    component: Shell,
  }),
  createRoute({
    getParentRoute: () => rootRoute,
    path: '/qy/tickets',
    component: Shell,
  }),
])

/** 一次挂载之后从 DOM 上抄下来的快照。 */
type Snapshot = { pageBody: boolean; text: string }

async function mountAt(path: string, status: number): Promise<Snapshot> {
  useAuthStore.setState((state) => ({
    ...state,
    auth: {
      ...state.auth,
      user: { id: 1, username: 'probe', role: 1, status },
      accessToken: 'probe',
    },
  }))

  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  queryClient.setQueryData(qyKeys.config(), ENABLED_CONFIG)

  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [path] }),
  })

  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)

  await act(async () => {
    root.render(
      <QueryClientProvider client={queryClient}>
        {/* eslint-disable-next-line @typescript-eslint/no-explicit-any */}
        <RouterProvider router={router as any} />
      </QueryClientProvider>
    )
  })
  // 路由器与 react-query 在挂载之后还会各通知一轮，冲干净再读 DOM。
  await act(async () => {})

  const snapshot: Snapshot = {
    pageBody: container.querySelector('[data-testid="page-body"]') != null,
    text: container.textContent ?? '',
  }

  // 当场卸载：留着不卸的话，下一个用例改 auth store 会把**上一个**闸也重渲染
  // 一遍，那次 setState 落在 act 之外 —— 一串 act 告警，根因是用例之间串状态。
  await act(async () => root.unmount())
  container.remove()
  return snapshot
}

describe('QyRestrictedGate', () => {
  test('受限账号 + 白名单之外的页面 → 页面不渲染，落地页顶上', async () => {
    const view = await mountAt('/wallet', USER_STATUS.DISABLED)
    assert.equal(view.pageBody, false, '钱包页仍然被渲染了')
    assert.ok(view.text.includes('qy_restricted_landing_title'))
    assert.ok(view.text.includes('qy_restricted_go_tickets'))
    // 横幅同时在场：落地页不是横幅的替代品。
    assert.ok(view.text.includes('qy_restricted_banner_title'))
  })

  test('受限账号 + 工单页 → 页面照常渲染，横幅仍然在', async () => {
    const view = await mountAt('/qy/tickets', USER_STATUS.DISABLED)
    assert.ok(view.pageBody, '工单页应当照常渲染')
    assert.ok(view.text.includes('qy_restricted_banner_title'))
    assert.ok(!view.text.includes('qy_restricted_landing_title'))
  })

  // 横幅与落地页永远同时在场，所以两块文案不能各说一遍同样的话：实测时
  // 「哪些事不能做」与「怎么申诉」在同一屏上出现了两次，把真正的增量信息
  // （你的额度/密钥/工单都还在）挤到了下面。这条把「同一个文案键在一屏里
  // 最多出现一次」钉住 —— 它是可观察的用户体验，不是实现细节。
  test('受限落地页不重复横幅已经说过的话', async () => {
    const view = await mountAt('/wallet', USER_STATUS.DISABLED)
    for (const key of [
      'qy_restricted_banner_title',
      'qy_restricted_blocked',
      'qy_restricted_appeal_hint',
      'qy_restricted_kept',
      'qy_restricted_landing_title',
    ]) {
      const hits = view.text.split(key).length - 1
      assert.ok(hits <= 1, `${key} 在同一屏里出现了 ${hits} 次`)
    }
    // 两段话各自仍有归宿：不可用清单在横幅，保留清单在落地页。
    assert.ok(view.text.includes('qy_restricted_blocked'))
    assert.ok(view.text.includes('qy_restricted_kept'))
  })

  test('正常账号 → 页面渲染，受限文案一个字都没有', async () => {
    const view = await mountAt('/wallet', USER_STATUS.ENABLED)
    assert.ok(view.pageBody)
    assert.ok(!view.text.includes('qy_restricted_banner_title'))
    assert.ok(!view.text.includes('qy_restricted_landing_title'))
  })
})
