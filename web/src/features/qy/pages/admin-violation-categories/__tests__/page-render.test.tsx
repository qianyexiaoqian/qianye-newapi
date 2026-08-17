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
 * 违规类型这一页**能不能打开**。
 *
 * # 为什么需要一个真渲染
 *
 * 项目方报过一次「违规类型的页面无法正常加载」。这一页原有的用例全是静态断言
 * (接线、i18n 键、文案口径),它们在**页面整块被错误边界换掉**时一条都不会红:
 * 一次 `row.category.id` 读到 undefined、一次响应里少了 `threshold_state`,
 * 页面上就只剩「页面加载失败」四个字,而所有静态用例照旧全绿。
 *
 * 这一页尤其吃这一口:它的每一格都直接读 `row.category.*`,而这份响应会随
 * **种子类型的增减**变形 —— 上一轮往种子里补了九类,这一页的行数从 6 变成 15,
 * 没有任何用例察觉到形状变了。
 *
 * # 这里钉住三件事
 *
 *  1. 拿到一份真实形状的响应时,这一页渲染出来的是**表格**,不是错误态;
 *  2. 阈值那一列按三态各说各的话 —— 尤其 `unset` 绝不显示成一个孤零零的 0;
 *  3. 两条横幅按各自的口径出现:「一条线都没生效」看全站,
 *     「还有 N 类没配」只数业务类型(兜底不算)。
 *
 * 文案一律从真实的 `src/i18n/qy/zh.json` 取,不抄字面量。
 * 变异验证见文件末尾。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { Window } from 'happy-dom'

import zh from '@/i18n/qy/zh.json'

import type { QyViolationCategoryRow } from '../types'

const zhKeys = zh as Record<string, string>

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

const { QyAdminViolationCategories } = await import('../index')
// 这一页走的是 `@/lib/api` 那个 axios 实例(带 Bearer 注入、401 重试、GET 去重),
// 桩必须打在它的 adapter 上。打在 fetch 上会让请求整体失败,于是这一页永远
// 渲染成「页面加载失败」—— 一个看起来在测错误态、实际什么都没测的用例。
const httpClient = await import('@/lib/http-client')
const axiosApi = (
  httpClient as unknown as { api: { defaults: { adapter: unknown } } }
).api

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

const roots: { container: HTMLElement; root: { unmount: () => void } }[] = []
// axios 实例是**跨测试文件共享**的模块单例(bun 在同一个进程里跑整包)。
// 换掉它的 adapter 而不还回去,后面每一个文件的 HTTP 桩都会读到这里的假响应 ——
// 表现是别处的用例莫名其妙地少跑、报错在完全无关的页面上。
const originalAdapter = axiosApi.defaults.adapter
after(() => {
  axiosApi.defaults.adapter = originalAdapter
  for (const entry of roots) {
    entry.root.unmount()
    entry.container.remove()
  }
})

function row(over: {
  id: number
  key: string
  name: string
  threshold?: number
  enabled?: boolean
  fallback?: boolean
  state: QyViolationCategoryRow['threshold_state']
  rules?: number
}): QyViolationCategoryRow {
  return {
    category: {
      id: over.id,
      key: over.key,
      name: over.name,
      remark: '内部说明',
      public_title: over.fallback === true ? '' : '公示标题',
      public_desc: '',
      ai_guidance: '',
      ai_excluded: false,
      published: over.fallback !== true,
      enabled: over.enabled ?? true,
      window_hours: 24,
      threshold: over.threshold ?? 0,
      sort_order: over.id * 10,
      is_fallback: over.fallback === true,
      created_at: 0,
      updated_at: 0,
    },
    rule_count: over.rules ?? 0,
    threshold_state: over.state,
  }
}

async function mountPage(rows: QyViolationCategoryRow[]): Promise<string> {
  // 桩必须按 URL 分流:`QyPageBoundary` 先读 `/api/qy/config`,那一路拿不到
  // `enabled:true` 就整页塌成「本站未启用该功能」的中性空态 —— 表格根本不渲染,
  // 而所有断言会以"这一行没渲染出来"的形态失败,看起来像页面坏了。
  axiosApi.defaults.adapter = async (config: { url?: string }) => {
    const url = String(config.url ?? '')
    const payload = url.includes('/violation/categories')
      ? { items: rows, fallback_id: 1, threshold_semantics: 'any_line' }
      : { enabled: true, features: { violation: true } }
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
    path: '/qy/admin/violation-categories',
    component: () => (
      <QueryClientProvider client={queryClient}>
        <QyAdminViolationCategories />
      </QueryClientProvider>
    ),
  })
  const router = createRouter({
    history: createMemoryHistory({
      initialEntries: ['/qy/admin/violation-categories'],
    }),
    routeTree: rootRoute.addChildren([pageRoute]),
  })
  await act(async () => {
    root.render(<RouterProvider router={router as never} />)
  })
  await act(async () => {
    await new Promise((r) => setTimeout(r, 50))
  })
  roots.push({ container, root })
  return container.textContent ?? ''
}

/** 现网形状:五类已配线、九类还没配、一个兜底。 */
const liveShape: QyViolationCategoryRow[] = [
  row({
    id: 2,
    key: 'jailbreak',
    name: '破限(越狱)',
    threshold: 3,
    state: 'active',
    rules: 10,
  }),
  row({
    id: 3,
    key: 'reverse',
    name: '逆向(套提示词)',
    threshold: 3,
    state: 'active',
    rules: 4,
  }),
  row({
    id: 4,
    key: 'distill',
    name: '蒸馏(批量采集)',
    threshold: 5,
    state: 'active',
    rules: 1,
  }),
  row({
    id: 5,
    key: 'pressure',
    name: '高压(提示词注入)',
    threshold: 5,
    enabled: false,
    state: 'disabled',
    rules: 6,
  }),
  row({ id: 131, key: 'cyber_attack', name: '网络攻击与逆向', state: 'unset' }),
  row({ id: 138, key: 'self_harm', name: '自伤与自杀', state: 'unset' }),
  row({
    id: 1,
    key: 'uncategorized',
    name: '未分类',
    fallback: true,
    state: 'unset',
    rules: 1,
  }),
]

describe('这一页打得开', () => {
  test('拿到真实形状的响应时渲染的是表格,不是错误态', async () => {
    const text = await mountPage(liveShape)

    // 这两句就是项目方报的那个现象:整页被 QyPageBoundary 换成
    // 「页面加载失败」或「本站未启用该功能」,表格一行都没有。
    for (const dead of ['qy_cfg_error_title', 'qy_cfg_disabled_desc']) {
      assert.ok(
        !text.includes(zhKeys[dead]),
        `整页被状态边界换掉了(${dead}):${text}`
      )
    }
    for (const name of ['破限(越狱)', '未分类', '自伤与自杀']) {
      assert.ok(text.includes(name), `${name} 那一行没渲染出来:${text}`)
    }
    // 兜底那一行必须带标记:它不是业务类型,归档它会让规则变成孤儿。
    assert.ok(text.includes(zhKeys['qy_vcat_flag_fallback']))
  })

  test('阈值那一列三态各说各的话', async () => {
    const text = await mountPage(liveShape)

    const active = zhKeys['qy_vcat_threshold_value']
      .replaceAll('{{count}}', '3')
      .replaceAll('{{hours}}', '24')
    assert.ok(text.includes(active), `生效那一档没渲染出来:${text}`)
    assert.ok(text.includes(zhKeys['qy_vcat_threshold_unset'].split('{{')[0]))
    assert.ok(
      text.includes(zhKeys['qy_vcat_threshold_disabled'].split('{{')[0])
    )
  })

  test('「还有 N 类没配」只数业务类型,兜底不算', async () => {
    const text = await mountPage(liveShape)
    // liveShape 里 unset 的业务类型是 cyber_attack 与 self_harm 两条,兜底不计入。
    assert.ok(
      text.includes(
        zhKeys['qy_vcat_unset_banner'].replaceAll('{{count}}', '2')
      ),
      `未配阈值的计数不对(兜底被算进去了?):${text}`
    )
  })

  test('还有一条线在生效时,不显示「一条线都没生效」那条横幅', async () => {
    const text = await mountPage(liveShape)
    assert.ok(
      !text.includes(zhKeys['qy_vcat_all_idle_banner']),
      `已经有 active 的类型了,却仍在说全站一条线都没生效:${text}`
    )
  })
})

/* ── 变异验证 ─────────────────────────────────────────────────────── */

describe('变异验证:两条横幅真的跟着数据走', () => {
  test('全部未配时才出现「一条线都没生效」', async () => {
    const allUnset = liveShape.map((r) => ({
      ...r,
      threshold_state: 'unset' as const,
      category: { ...r.category, threshold: 0 },
    }))
    const text = await mountPage(allUnset)
    assert.ok(
      text.includes(zhKeys['qy_vcat_all_idle_banner']),
      `全站一条线都没有,却没有那条横幅:${text}`
    )
    // 六个业务类型 + 一个兜底 ⇒ 未配计数是 6,不是 7。
    assert.ok(
      text.includes(
        zhKeys['qy_vcat_unset_banner'].replaceAll('{{count}}', '6')
      ),
      `未配阈值的计数把兜底算进去了:${text}`
    )
  })

  test('全部配齐时两条横幅都消失', async () => {
    const allActive = liveShape.map((r) => ({
      ...r,
      threshold_state: 'active' as const,
      category: { ...r.category, threshold: 3, enabled: true },
    }))
    const text = await mountPage(allActive)
    assert.ok(!text.includes(zhKeys['qy_vcat_all_idle_banner']))
    assert.ok(
      !text.includes(zhKeys['qy_vcat_unset_banner'].split('{{')[0]),
      `一个未配的都没有了,却仍在提示还有几类没配:${text}`
    )
  })
})
