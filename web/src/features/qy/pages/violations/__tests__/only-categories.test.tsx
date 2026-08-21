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
 * 用户端「我的违规记录」只留违规类型。
 *
 * # 要求本体
 *
 * 项目方原话：「我的违规记录，这里只显示违规类型就行。把窗口违规次数、距离封号
 * 还剩余、累计扣费这些移除掉吧。」三个统计块连同它们唯一的数据源
 * （`GET /api/qy/violation/my-summary`）一起从这一页下线。
 *
 * # 为什么要真渲染，而不是只扫源码
 *
 * 「界面上还看不看得见」只有渲染能回答。删掉 JSX 却把 `useQuery` 留着，源码扫描
 * （`source.includes('QyStatGrid')`）会转绿，而页面每次打开仍旧多打一个接口；
 * 反过来，把统计块挪进一个子组件也能骗过任何按文件的字面量断言。这里改成：
 * **把 my-summary 桩成一组显眼的哨兵数字，然后看它有没有被请求、有没有渲染出来。**
 *
 * # 这里钉住三件事
 *
 *  1. 这一页不再请求 my-summary，且哨兵数字一个都不出现在界面上；
 *  2. 违规类型公示照旧渲染 —— 三块拿掉之后它是**唯一**的封号预警渠道，
 *     连它一起弄没，用户会毫无预警地被封；
 *  3. 那三块的文案键不许以孤儿的形态留在语言包里。
 *
 * 后端 `my-summary` **仍然如实下发**那些字段（含 `remaining_threshold` 那一组），
 * 只是没人渲染 —— 保留理由与代价写在 `../types.ts` 的 `QyMyViolationSummary`
 * 顶部。要把三块加回来，先读那段注释，再回来删本文件的对应用例。
 *
 * 文案一律从真实的 `src/i18n/qy/zh.json` 自己插值算出来，不抄字面量。
 * 变异验证见文件末尾。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { Window } from 'happy-dom'

import en from '@/i18n/qy/en.json'
import zh from '@/i18n/qy/zh.json'

const zhKeys = zh as Record<string, string>
const enKeys = en as Record<string, string>

const domWindow = new Window({ height: 900, width: 1280 })
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
const noopScroll = () => {}
Object.defineProperty(globalThis, 'scrollTo', {
  configurable: true,
  value: noopScroll,
})
Object.defineProperty(domWindow, 'scrollTo', {
  configurable: true,
  value: noopScroll,
})

const { act } = await import('react')
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

await i18next.use(initReactI18next).init({
  interpolation: { escapeValue: false },
  lng: 'zh',
  nsSeparator: false,
  resources: { zh: { translation: zhKeys } },
})

const { QyMyViolations } = await import('../index')
const { QyMyViolationCategoriesCard } =
  await import('../components/categories-card')

// 这一页走 `@/lib/api`，而它复用的是 `@/lib/http-client` 的 axios 实例。
// 桩必须打在那个实例的 adapter 上：打在 fetch 上请求会整体失败，页面塌成
// 「页面加载失败」，于是所有断言都以"没渲染出来"的形态通过——测了个寂寞。
const httpClient = await import('@/lib/http-client')
const axiosApi = (
  httpClient as unknown as { api: { defaults: { adapter: unknown } } }
).api

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

const roots: { container: HTMLElement; root: { unmount: () => void } }[] = []
// axios 实例是**跨测试文件共享**的模块单例（bun 在同一个进程里跑整个目录）。
// 换掉 adapter 不还回去，后面每个文件的 HTTP 桩都会读到这里的假响应。
const originalAdapter = axiosApi.defaults.adapter
after(() => {
  axiosApi.defaults.adapter = originalAdapter
  for (const entry of roots) {
    entry.root.unmount()
    entry.container.remove()
  }
})

/**
 * 三个统计块各自的哨兵。取的是四位重复数字，界面上任何一处正常数据都不会
 * 长成这样 —— 一旦它出现在 `textContent` 里，就只可能是那三块回来了。
 */
const SENTINEL = {
  hitCount: 8181,
  banThreshold: 9191,
  windowHours: 2121,
  remaining: 7171,
  remainingThreshold: 5151,
  remainingWindowHours: 4141,
  remainingHitCount: 3131,
}

const CATEGORY_TITLE = '指令注入'
/** 与后端 `toUserView` 的兜底对外文案同字面。 */
const RECORD_REASON = '内容违反使用策略'

async function mountPage(): Promise<{ text: string; urls: string[] }> {
  const urls: string[] = []
  axiosApi.defaults.adapter = async (config: { url?: string }) => {
    const url = String(config.url ?? '')
    urls.push(url)
    let payload: unknown = { enabled: true, features: { violation: true } }
    if (url.includes('/violation/my-summary')) {
      payload = {
        hit_count: SENTINEL.hitCount,
        window_hours: SENTINEL.windowHours,
        ban_threshold: SENTINEL.banThreshold,
        remaining: SENTINEL.remaining,
        remaining_line: 'account',
        remaining_threshold: SENTINEL.remainingThreshold,
        remaining_window_hours: SENTINEL.remainingWindowHours,
        remaining_hit_count: SENTINEL.remainingHitCount,
        banned: false,
        total_fee_quota: 1_000_000,
        policy_action: 'ban',
      }
    } else if (url.includes('/violation/my-categories')) {
      payload = {
        items: [
          {
            id: 5,
            title: CATEGORY_TITLE,
            description: '',
            threshold: 3,
            window_hours: 24,
            hit_count: 2,
            remaining: 1,
          },
        ],
        account_threshold: 10,
        account_hit_count: 2,
        account_window_hours: 24,
        policy_action: 'ban',
        banned: false,
        threshold_semantics: 'any_line',
      }
    } else if (url.includes('/violation/my-records')) {
      payload = {
        items: [
          {
            id: 77,
            created_at: 1_755_000_000,
            model_name: 'gpt-4o',
            reason: RECORD_REASON,
            category: CATEGORY_TITLE,
            blocked: true,
            fee_quota: 500_000,
            fee_status: 'charged',
            status: 'active',
            counter_after: 2,
          },
        ],
        total: 1,
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
  const rootRoute = createRootRoute({ component: Outlet })
  const pageRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/qy/violations',
    component: () => (
      <QueryClientProvider client={queryClient}>
        <QyMyViolations />
      </QueryClientProvider>
    ),
  })
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: ['/qy/violations'] }),
    routeTree: rootRoute.addChildren([pageRoute]),
  })
  await act(async () => {
    root.render(<RouterProvider router={router as never} />)
  })
  await act(async () => {
    await new Promise((r) => setTimeout(r, 50))
  })
  roots.push({ container, root })
  return { text: container.textContent ?? '', urls }
}

describe('用户端违规页只留违规类型', () => {
  test('页面打得开，且渲染的是列表不是状态边界', async () => {
    const { text } = await mountPage()
    // 这两句一旦出现，说明整页被 QyPageBoundary 换掉了，
    // 下面所有"没渲染出来"的断言会以最坏的方式通过。
    for (const dead of ['qy_cfg_error_title', 'qy_cfg_disabled_desc']) {
      assert.ok(
        !text.includes(zhKeys[dead]),
        `整页被状态边界换掉了（${dead}）：${text}`
      )
    }
    assert.ok(text.includes('gpt-4o'), `记录行没渲染出来：${text}`)
  })

  test('不再请求 my-summary —— 那个接口只喂过被移除的三块', async () => {
    const { urls } = await mountPage()
    assert.ok(
      urls.some((u) => u.includes('/violation/my-categories')),
      `连类型公示都没请求，这一页的桩没生效：${urls.join(' , ')}`
    )
    assert.ok(
      urls.some((u) => u.includes('/violation/my-records')),
      `记录列表没请求：${urls.join(' , ')}`
    )
    assert.deepEqual(
      urls.filter((u) => u.includes('/violation/my-summary')),
      [],
      '这一页又去拉 my-summary 了：三个统计块被移除之后，那个接口在用户端没有任何消费者，多打一次是纯粹的浪费，也是三块正在回归的第一个信号'
    )
  })

  test('窗口违规次数 / 距离封号还剩余 / 累计扣费的数字一个都不出现', async () => {
    const { text } = await mountPage()
    // 哪怕有人把 my-summary 重新接回来，只要不渲染，这条仍然是绿的；
    // 而只要那三块有一格重新画出来，这里立刻红。
    for (const [name, value] of Object.entries(SENTINEL)) {
      assert.ok(
        !text.includes(String(value)),
        `${name}（${value}）被渲染到了用户端违规页上 —— 项目方要求这一页只显示违规类型：${text}`
      )
    }
  })

  test('违规类型公示还在 —— 它现在是唯一的封号预警渠道', async () => {
    const { text } = await mountPage()
    assert.ok(
      text.includes(zhKeys['qy_vio_cat_title']),
      `公示卡片整块没了：${text}`
    )
    // 期望值独立算出：从真实 zh.json 原文自己插值。
    const want = zhKeys['qy_vio_cat_sentence']
      .replaceAll('{{title}}', CATEGORY_TITLE)
      .replaceAll('{{hit}}', '2')
      .replaceAll('{{threshold}}', '3')
      .replaceAll('{{hours}}', '24')
      .replaceAll('{{action}}', zhKeys['qy_vio_policy_action_ban'])
    assert.ok(!want.includes('{{'), `期望值算错了，还有没替换的占位符：${want}`)
    assert.ok(
      text.includes(want),
      `「你违规了什么类型多少次、到多少次封号」这一句没渲染出来。三块统计移除之后它是用户唯一能知道自己快被封的地方：${text}`
    )
  })
})

describe('被移除那三块的文案键不许留成孤儿', () => {
  /**
   * 这 11 个键随三个统计块一起下线。留着不会有任何测试变红（本仓的
   * `i18n-key-coverage` 只查"用到的键在不在"，不查"在的键有没有人用"），
   * 但它们会一直躺在两份语言包里，让下一个人以为界面上还有这些文案。
   *
   * 如果哪天项目方要把三块加回来：先读 `../types.ts` 里
   * `QyMyViolationSummary` 顶部那段注释，再回来删掉这条用例。
   */
  const RETIRED_KEYS = [
    'qy_vio_my_window_hits',
    'qy_vio_my_window_hint',
    'qy_vio_my_window_hint_unlimited',
    'qy_vio_my_remaining',
    'qy_vio_my_remaining_banned',
    'qy_vio_my_remaining_by_account',
    'qy_vio_my_remaining_by_category',
    'qy_vio_my_remaining_line_scale',
    'qy_vio_my_remaining_line_scale_unlimited',
    'qy_vio_my_total_fee',
    'qy_vio_my_progress',
  ]

  test('zh 与 en 都不再带着它们', () => {
    const leftover: string[] = []
    for (const key of RETIRED_KEYS) {
      if (key in zhKeys) leftover.push(`${key} (zh)`)
      if (key in enKeys) leftover.push(`${key} (en)`)
    }
    assert.deepEqual(
      leftover,
      [],
      `以下文案键已经没有任何组件引用，却还留在语言包里：\n${leftover.join('\n')}`
    )
  })
})

/* ── 公示卡片：「已达门槛」与「已经被处置」是两句不同的话 ─────────────── */

async function mountCard(banned: boolean) {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(
      <QyMyViolationCategoriesCard
        data={{
          account_threshold: 10,
          account_hit_count: 3,
          account_window_hours: 24,
          policy_action: 'ban',
          banned,
          threshold_semantics: 'any_line',
          items: [
            {
              id: 5,
              title: CATEGORY_TITLE,
              description: '',
              threshold: 3,
              window_hours: 24,
              hit_count: 3,
              remaining: 0,
            },
          ],
        }}
      />
    )
  })
  await act(async () => {})
  roots.push({ container, root })
  return container
}

describe('公示卡片在"已达门槛"之后说的话', () => {
  test('尚未被处置时是预告', async () => {
    const container = await mountCard(false)
    const text = container.textContent ?? ''
    const want = zhKeys['qy_vio_cat_remaining_none'].replace(
      '{{action}}',
      zhKeys['qy_vio_policy_action_ban']
    )
    assert.ok(text.includes(want), `没有渲染出预告句，实际：${text}`)
  })

  test('已经被处置时改成"已按 X 处理"，不再预告一件已经发生的事', async () => {
    const container = await mountCard(true)
    const text = container.textContent ?? ''
    const done = zhKeys['qy_vio_cat_remaining_done'].replace(
      '{{action}}',
      zhKeys['qy_vio_policy_action_ban']
    )
    const stale = zhKeys['qy_vio_cat_remaining_none'].replace(
      '{{action}}',
      zhKeys['qy_vio_policy_action_ban']
    )
    assert.ok(text.includes(done), `没有渲染出"已处理"那一句，实际：${text}`)
    assert.ok(
      !text.includes(stale),
      '已经被封的人仍被告知"下一次违规才会被处理" —— 那是一句已经过期的预告'
    )
  })

  test('"已处理"那一句必须给出下一步（申诉），中英齐备', () => {
    for (const [lang, dict] of [
      ['zh', zhKeys],
      ['en', enKeys],
    ] as const) {
      const text = dict['qy_vio_cat_remaining_done']
      assert.ok(text, `${lang} 缺少 qy_vio_cat_remaining_done`)
      assert.ok(
        text.includes('{{action}}'),
        `${lang}: 处置动作被写死了，「仅记录」档下会变成一句吓人的假话`
      )
      assert.ok(
        lang === 'zh'
          ? text.includes('申诉')
          : text.toLowerCase().includes('appeal'),
        `${lang}: 已经被处置的人最需要的是申诉入口，这一句没有给`
      )
    }
  })
})

/*
 * # 变异验证
 *
 *  1. index.tsx 里把 my-summary 的 useQuery 接回来
 *     → 「不再请求 my-summary」KILLED。
 *  2. index.tsx 里把「累计扣费」那一格重新画出来（label 用字面量，
 *     绕开语言包）→ 「哨兵数字一个都不出现」KILLED。
 *  3. index.tsx 里删掉 <QyMyViolationCategoriesCard />
 *     → 「违规类型公示还在」KILLED。
 *  4. zh.json / en.json 里把 qy_vio_my_total_fee 加回去
 *     → 「不许留成孤儿」KILLED。
 *  5. categories-card.tsx 里把 `data.banned === true` 改成 `false`
 *     → 「已经被处置时改成已按 X 处理」KILLED。
 */
