/*
 * useQyTabHash —— 切标签**真的**会把 hash 写进地址栏。
 *
 * # 这条测试守的是什么
 *
 * `qyResolveTab` 的纯函数部分已经被 `tabs.test.ts` 测掉了，剩下的风险全在
 * 那一行 `navigate({ to: '.', hash, replace: true })` 上：`to` 写错、或者这个
 * 版本的 TanStack Router 不接受相对路径，切标签就会**静默失败** —— 点上去
 * 没有任何报错，标签也不动。那正是本仓反复出现的「写了但没接上」形状，
 * 而且是类型检查抓不到的一种（`to` 是宽松的字符串联合）。
 *
 * 所以这里挂一个真的路由器（内存历史）跑一遍，断言 hash 确实变了、
 * 且解析出来的标签跟着变。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { Window } from 'happy-dom'

const domWindow = new Window({ url: 'http://localhost/wallet' })
const domGlobals = [
  'window',
  'document',
  'navigator',
  'HTMLElement',
  'Node',
  'Element',
  'Event',
  'CustomEvent',
  'MouseEvent',
  'MutationObserver',
  'requestAnimationFrame',
  'cancelAnimationFrame',
  'getComputedStyle',
] as const

for (const key of domGlobals) {
  Object.defineProperty(globalThis, key, {
    configurable: true,
    value: domWindow[key],
  })
}

// happy-dom 没有实现 scrollTo，而路由器每次导航都会调它 —— 缺了它会在
// 导航中途抛错，整棵树掉进 CatchBoundary，看起来像是本测试的断言写错了。
Object.defineProperty(globalThis, 'scrollTo', {
  configurable: true,
  value: () => {},
})
Object.defineProperty(domWindow, 'scrollTo', {
  configurable: true,
  value: () => {},
})

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const {
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
} = await import('@tanstack/react-router')
const { useQyTabHash } = await import('../tabs')

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

const TABS = ['/qy/transfer', '/qy/transfer-logs', '/qy/pay-password']

function Probe() {
  const { active, select } = useQyTabHash(TABS)
  return (
    <div>
      <span data-testid='active'>{active}</span>
      <button
        type='button'
        data-testid='go-logs'
        onClick={() => select('/qy/transfer-logs')}
      />
    </div>
  )
}

const rootRoute = createRootRoute({ component: Outlet })
const walletRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/wallet',
  component: Probe,
})

const cleanups: Array<() => Promise<void>> = []
after(async () => {
  for (const fn of cleanups) await fn()
})

describe('useQyTabHash 在真实路由器里', () => {
  test('初始落到第一张标签；点一下之后 hash 与选中项同时变', async () => {
    const router = createRouter({
      routeTree: rootRoute.addChildren([walletRoute]),
      history: createMemoryHistory({ initialEntries: ['/wallet'] }),
    })

    const container = document.createElement('div')
    document.body.appendChild(container)
    const root = createRoot(container)
    cleanups.push(async () => {
      await act(async () => root.unmount())
      container.remove()
    })

    await act(async () => {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      root.render(<RouterProvider router={router as any} />)
    })

    const active = () =>
      container.querySelector('[data-testid="active"]')?.textContent
    assert.equal(active(), '/qy/transfer', '没有 hash 时应当落到第一张标签')

    const button = container.querySelector(
      '[data-testid="go-logs"]'
    ) as HTMLElement
    await act(async () => {
      button.click()
    })

    assert.equal(
      router.state.location.hash,
      'qy-transfer-logs',
      'select() 没有把 hash 写进地址栏：切标签会静默失败'
    )
    assert.equal(active(), '/qy/transfer-logs', '选中项没有跟着 hash 走')
  })
})
