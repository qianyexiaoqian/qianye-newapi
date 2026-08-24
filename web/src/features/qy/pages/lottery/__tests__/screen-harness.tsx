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
/**
 * 抽奖竞猜用户端「一屏到底有多少字」的测量夹具。
 *
 * 这里不做断言，只负责把一屏真的渲染出来并把可见文本交出去 —— 判断标准写在
 * 各自的测试文件里。之所以必须真渲染而不是数 i18n 值：一屏上的字有三个来源
 * （i18n 文案、后端下发的正文、以及折叠区里那些**默认不出现**的段落），
 * 只有 DOM 能把三者按"用户此刻真的看得见"这一条口径合起来算。
 */
import { Window } from 'happy-dom'

import type { QyLotActivityBrief, QyLotActivityDetail } from '../types'

const domWindow = new Window({ width: 1280, height: 900 })
for (const key of [
  'window',
  'document',
  'navigator',
  'localStorage',
  'sessionStorage',
  'HTMLElement',
  'Node',
  'Element',
  'Event',
  'CustomEvent',
  'MouseEvent',
  'PointerEvent',
  'KeyboardEvent',
  'MutationObserver',
  'ResizeObserver',
  'requestAnimationFrame',
  'cancelAnimationFrame',
  'getComputedStyle',
  'matchMedia',
  'DOMRect',
] as const) {
  const value = domWindow[key as keyof Window]
  if (value === undefined) continue
  Object.defineProperty(globalThis, key, { configurable: true, value })
}
for (const name of ['scrollTo', 'scrollIntoView'] as const) {
  const noop = () => {}
  Object.defineProperty(globalThis, name, { configurable: true, value: noop })
  Object.defineProperty(domWindow, name, { configurable: true, value: noop })
}
Object.defineProperty(globalThis.Element.prototype, 'scrollIntoView', {
  configurable: true,
  value: () => {},
})

const { act } = await import('react')
const React = await import('react')
const { createRoot } = await import('react-dom/client')
const i18next = (await import('i18next')).default
const { initReactI18next } = await import('react-i18next')
const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const {
  Outlet,
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
} = await import('@tanstack/react-router')

const zhKeys = (await import('@/i18n/qy/zh.json')).default as Record<
  string,
  string
>

await i18next.use(initReactI18next).init({
  interpolation: { escapeValue: false },
  lng: 'zh',
  nsSeparator: false,
  resources: { zh: { translation: zhKeys } },
})

const httpClient = await import('@/lib/http-client')
const axiosApi = (
  httpClient as unknown as { api: { defaults: { adapter: unknown } } }
).api
const originalAdapter = axiosApi.defaults.adapter

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

const mounted: { container: HTMLElement; root: { unmount: () => void } }[] = []

/** 拆掉本文件挂过的所有根，并把共享的 axios adapter 还回去。 */
export function cleanupQyLotScreens() {
  axiosApi.defaults.adapter = originalAdapter
  for (const entry of mounted) {
    entry.root.unmount()
    entry.container.remove()
  }
  mounted.length = 0
  document.body.innerHTML = ''
}

/** 一行大厅活动。只填卡片真的会读的字段，其余给零值。 */
export function qyLotBriefFixture(
  over: Partial<QyLotActivityBrief> = {}
): QyLotActivityBrief {
  return {
    act_no: 'LA-1',
    kind: 'draw',
    status: 'published',
    outcome: '',
    title: '春季回馈抽奖',
    stake_quota: 500000,
    open_at: 1_700_000_000,
    close_at: 1_700_600_000,
    draw_at: 1_700_700_000,
    active_count: 128,
    pool_quota: 8_000_000,
    prize_total_quota: 8_000_000,
    my_entry_count: 0,
    draw_mode: 'rank',
    series_no: '',
    issue_no: 0,
    pool_open_quota: 0,
    ball_red_pool: 0,
    ball_red_pick: 0,
    ball_blue_pool: 0,
    ball_blue_pick: 0,
    ball_result: '',
    ...over,
  }
}

/** 一场活动详情。默认是「进行中的名次制抽奖」。 */
export function qyLotDetailFixture(
  over: Partial<QyLotActivityDetail> = {}
): QyLotActivityDetail {
  return {
    ...qyLotBriefFixture(),
    intro: '本场为春季回馈活动，面向所有已完成邮箱验证的用户。',
    rules_text: JSON.stringify({ min_quota: 100000, require_email: true }),
    spec: [
      {
        tier: 1,
        name: '一等奖',
        amount_quota: 5_000_000,
        count: 1,
        win_ppm: 0,
      },
      {
        tier: 2,
        name: '二等奖',
        amount_quota: 1_000_000,
        count: 5,
        win_ppm: 0,
      },
    ],
    commit_hash: 'a'.repeat(64),
    max_entries_per_user: 3,
    cooldown_seconds: 0,
    dedup_ip: true,
    allow_multi_win: false,
    min_entries_to_hold: 20,
    settle_deadline: 1_700_900_000,
    fee_bps: 500,
    win_opt_no: 0,
    result_evidence: '',
    play_open: true,
    ...over,
  } as QyLotActivityDetail
}

export type QyLotScreen = {
  /**
   * 这一屏的根节点。
   *
   * 断言"每张卡上都有参与费"这一类**逐个元素**的事实时必须用它：在整屏文本里
   * 找关键词，会被页面别处出现的同一个词蒙混过去（分段栏那枚徽章写的正是
   * 「参与费不退」）。
   */
  container: HTMLElement
  /** 屏幕上真的看得见的字符数（空白折叠后按 code point 计）。 */
  chars: number
  /** 可见文本原文，供断言"这一句还在不在"。 */
  text: string
  /** 点一颗按钮（按可见文字精确匹配）并等一拍。 */
  click: (label: string) => Promise<boolean>
  /** 每一次请求的 url + params，按发出顺序。 */
  asked: { url: string; params: Record<string, unknown> }[]
  /** 重新读一次当前 DOM（点开折叠之后要用）。 */
  read: () => { chars: number; text: string }
}

/** 空白折叠后的可见字符数。缩进与换行不是"字"。 */
export function qyVisibleChars(raw: string): number {
  return [...raw.replace(/\s+/g, ' ').trim()].length
}

type MountOptions = {
  /** 渲染在路由下的那一屏。 */
  element: React.ReactNode
  /** 初始 URL，默认 `/qy/lottery`。 */
  path?: string
  /** 按 url 片段回数据；返回 undefined 表示这条请求不认识。 */
  respond: (url: string, params: Record<string, unknown>) => unknown
  /** 视口宽度。窄屏用 375。 */
  width?: number
}

/**
 * 挂一屏。
 *
 * 路由树刻意把 `_authenticated` 这一层也建出来 —— 详情页读的是
 * `useParams({ from: '/_authenticated/qy/lottery/$actNo/' })`，缺这一层就取不到
 * `actNo`，整屏塌成空白，而"空白"会让任何字数断言都轻松通过。
 */
export async function mountQyLotScreen(
  options: MountOptions
): Promise<QyLotScreen> {
  // 一次只留一屏在 DOM 里。上一屏不拆掉，`document.body.textContent` 会把它的字
  // 一起算进来 —— 那正是"字数统计"这件事最容易骗自己的地方。
  for (const entry of mounted) {
    await act(async () => {
      entry.root.unmount()
    })
    entry.container.remove()
  }
  mounted.length = 0
  document.body.innerHTML = ''

  const asked: { url: string; params: Record<string, unknown> }[] = []
  const width = options.width ?? 1280
  Object.defineProperty(domWindow, 'innerWidth', {
    configurable: true,
    value: width,
  })
  Object.defineProperty(globalThis, 'innerWidth', {
    configurable: true,
    value: width,
  })
  Object.defineProperty(globalThis, 'matchMedia', {
    configurable: true,
    value: (queryText: string) => {
      const max = /max-width:\s*(\d+)px/.exec(queryText)
      const matches = max == null ? false : width <= Number(max[1])
      return {
        matches,
        media: queryText,
        addEventListener: () => {},
        removeEventListener: () => {},
        addListener: () => {},
        removeListener: () => {},
        onchange: null,
        dispatchEvent: () => false,
      }
    },
  })

  axiosApi.defaults.adapter = async (config: {
    params?: Record<string, unknown>
    url?: string
  }) => {
    const url = String(config.url ?? '')
    const params = config.params ?? {}
    asked.push({ url, params })
    let payload = options.respond(url, params)
    if (payload === undefined) {
      // 引导端点的兜底。`available` 必须给 true —— 缺了它每一屏顶上都会多挂一条
      // 红色的「服务降级」横幅，而那 30 个字并不属于被测的这一屏。
      payload = {
        enabled: true,
        available: true,
        features: { lottery: true },
        lottery: {
          show_entry: true,
          plays: { rank: true, prob: true, ball: true, guess: true },
        },
      }
    }
    return {
      config,
      data: { success: true, message: '', data: payload },
      headers: {},
      status: 200,
      statusText: 'OK',
    }
  }

  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)

  const Screen = () => (
    <QueryClientProvider client={queryClient}>
      {options.element}
    </QueryClientProvider>
  )

  const rootRoute = createRootRoute({ component: Outlet })
  const authRoute = createRoute({
    getParentRoute: () => rootRoute,
    id: '_authenticated',
    component: Outlet,
  })
  const hallRoute = createRoute({
    getParentRoute: () => authRoute,
    path: '/qy/lottery',
    component: Screen,
  })
  const detailRoute = createRoute({
    getParentRoute: () => authRoute,
    path: '/qy/lottery/$actNo/',
    component: Screen,
  })
  const adminRoute = createRoute({
    getParentRoute: () => authRoute,
    path: '/qy/admin/lottery',
    component: Screen,
  })
  const router = createRouter({
    history: createMemoryHistory({
      initialEntries: [options.path ?? '/qy/lottery'],
    }),
    routeTree: rootRoute.addChildren([
      authRoute.addChildren([hallRoute, detailRoute, adminRoute]),
    ]),
  })

  await act(async () => {
    root.render(<RouterProvider router={router as never} />)
  })
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 80))
  })
  mounted.push({ container, root })

  // 弹窗走 portal 挂到 body，所以文本一律从 body 上读。
  const read = () => {
    const text = document.body.textContent ?? ''
    return { chars: qyVisibleChars(text), text: text.replace(/\s+/g, ' ') }
  }

  return {
    asked,
    container,
    get chars() {
      return read().chars
    },
    get text() {
      return read().text
    },
    read,
    click: async (label) => {
      const node = Array.from(
        document.body.querySelectorAll('button,[role="button"]')
      ).find((item) => (item.textContent ?? '').trim() === label)
      if (node == null) return false
      await act(async () => {
        node.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      })
      await act(async () => {
        await new Promise((resolve) => setTimeout(resolve, 60))
      })
      return true
    },
  }
}

export { React, act, zhKeys }
