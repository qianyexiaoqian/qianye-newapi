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
 * 作用域卡在**页面上**的两件事。
 *
 * `ai-scope.test.ts` 覆盖的是纯逻辑(草稿往返、状态定性、名单切分)。逻辑对
 * 但卡片没有接进页面、或者接进去了却被一份缺字段的响应打成白屏 —— 这两种
 * 失效纯逻辑用例一条都看不见,而它们正是项目方打开这一页时会遇到的形态。
 *
 * 所以这里挂真组件、真 i18n:
 *
 *  1. 汇总表真的渲染出来了,而且**末行是兜底档** —— 界面顺序就是热路径的
 *     判定顺序,排反了运营会照着一个错误的心智模型去调优先级。
 *  2. 响应里少了 `summary` 时这一页仍然是一页。类型上它是必填的,但类型说的是
 *     「后端应该给」,运行期拿到的是「后端这次给了什么」(接口降级、老版本
 *     后端、反向代理裁剪)。直读一层会在渲染中途抛 TypeError,把整页打掉,
 *     而这一页正是用来回答"现在哪些分组在被监控"的。
 *
 * 变异验证见文件末尾。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { Window } from 'happy-dom'

import zh from '@/i18n/qy/zh.json'

import type { QyAiScopeList } from '../types'

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
const { QueryClient, QueryClientProvider } = await import(
  '@tanstack/react-query'
)
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
  resources: { zh: { translation: zh as Record<string, string> } },
})

const { QyAdminViolationAiReview } = await import('../index')
const { qyKeys } = await import('../../../lib/query-keys')

const dict = zh as Record<string, string>

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

const roots: { container: HTMLElement; root: { unmount: () => void } }[] = []
after(() => {
  for (const entry of roots) {
    entry.root.unmount()
    entry.container.remove()
  }
})

const scopes: QyAiScopeList = {
  items: [
    {
      id: 7,
      name: '自助注册分组',
      enabled: true,
      priority: 10,
      model_scope: '',
      group_scope: 'selfserve',
      group_scope_mode: 'include',
      pre_sample_rate_bps: 0,
      async_sample_rate_bps: 5000,
      remark: '',
      created_at: 0,
      updated_at: 0,
    },
  ] as unknown as QyAiScopeList['items'],
  summary: [
    {
      id: 7,
      name: '自助注册分组',
      fallback: false,
      enabled: true,
      priority: 10,
      model_scope: '',
      group_scope: 'selfserve',
      group_scope_mode: 'include',
      pre_sample_rate_bps: 0,
      async_sample_rate_bps: 5000,
      shadowed: false,
    },
    {
      id: 0,
      name: '未匹配任何策略',
      fallback: true,
      enabled: true,
      priority: 1 << 30,
      model_scope: '',
      group_scope: '',
      group_scope_mode: 'include',
      pre_sample_rate_bps: 0,
      async_sample_rate_bps: 0,
      shadowed: false,
    },
  ],
  fallback_bps: 0,
  max_scopes: 64,
  ai_enabled: true,
  effective_active: true,
  active_scopes: [],
}

let activeQueryClient: InstanceType<typeof QueryClient> | null = null

const rootRoute = createRootRoute({ component: Outlet })
const pageRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/qy/admin/violation-ai-review',
  component: () => (
    <QueryClientProvider client={activeQueryClient!}>
      <QyAdminViolationAiReview />
    </QueryClientProvider>
  ),
})
const routeTree = rootRoute.addChildren([pageRoute])

/**
 * 设置卡的一份最小可用响应。
 *
 * 必须预置：整包跑时别的用例装的全局 fetch 桩会让这次查询落到一份不认识的
 * 对象上，而设置卡一旦在渲染中途抛异常，**整页**都会被路由那层的错误边界
 * 换掉 —— 连带作用域卡一起消失，于是本文件的断言会以"找不到汇总表"的形态
 * 失败，看起来像作用域卡坏了。这正是下面第四条用例要钉住的那件事。
 */
const settings = {
  setting: {
    enabled: false,
    sample_rate_bps: 0,
    pre_timeout_ms: 1500,
    async_timeout_ms: 8000,
    prompt: '',
    max_input_chars: 4000,
    third_party_notice_ack: false,
  },
  default_prompt: '默认提示词',
  prompt_source: 'default',
  categories: ['uncategorized', 'jailbreak'],
  category_block: '可用的 category 取值',
  prompt_preview: '默认提示词\n\n可用的 category 取值',
  category_details: [],
  prompt_categories: { unknown: [], missing: [] },
  effective: {
    active: false,
    channels: 0,
    max_pre_timeout: 5000,
    max_async_timeout: 30000,
  },
}

async function mountPage(payload: unknown, settingPayload: unknown = settings) {
  for (const entry of roots.splice(0)) {
    entry.root.unmount()
    entry.container.remove()
  }
  await act(async () => {})

  // 预置的数据一挂载就会被判为陈旧并触发真实 fetch，而这里没有网络；
  // 那次失败会把卡片换成错误态，表现为"单跑绿、合跑红"。
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, refetchOnMount: false } },
  })
  queryClient.setQueryData(qyKeys.adminViolationAiScopes(), payload)
  queryClient.setQueryData(qyKeys.adminViolationAiSettings(), settingPayload)
  queryClient.setQueryData(qyKeys.adminViolationAiChannels(), { items: [] })
  queryClient.setQueryData(qyKeys.adminViolationAiStats(7), {
    total_calls: 0,
    total_tokens: 0,
    total_cost_usd: '0',
    violated_calls: 0,
    by_outcome: [],
    by_channel: [],
  })
  // qy 扩展开关。`QyPageBoundary` 在 `status === 'disabled'` 时整块换成
  // 「本站未启用该功能」的空态，卡片根本不渲染。
  queryClient.setQueryData(qyKeys.config(), { enabled: true })
  activeQueryClient = queryClient

  const router = createRouter({
    routeTree,
    history: createMemoryHistory({
      initialEntries: ['/qy/admin/violation-ai-review'],
    }),
  })
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(<RouterProvider router={router as never} />)
  })
  await act(async () => {})
  roots.push({ container, root })
  return container
}

/** 作用域汇总表：按「监控范围」这个只有它才有的表头锚定。 */
function scopeTable(container: HTMLElement): Element | null {
  for (const table of container.querySelectorAll('table')) {
    const heads = [...table.querySelectorAll('thead th')].map((n) =>
      (n.textContent ?? '').trim()
    )
    if (heads.includes(dict.qy_ai_scope_col_audience)) return table
  }
  return null
}

describe('AI 审核作用域卡', () => {
  test('汇总表接进了页面，且末行是兜底档', async () => {
    const container = await mountPage(scopes)
    const table = scopeTable(container)
    assert.ok(table, '页面上找不到作用域汇总表')
    const rows = [...table.querySelectorAll('tbody tr')]
    assert.equal(rows.length, 2, '汇总行数不对')
    assert.ok(
      (rows[0]?.textContent ?? '').includes('自助注册分组'),
      `第一行不是优先级最高的那一档：${rows[0]?.textContent}`
    )
    // 末行恒为兜底档。它不是排版偏好：热路径就是"前面都不匹配才落到它"，
    // 界面上排在别处会让人以为兜底率能盖住上面的策略。
    assert.ok(
      (rows[1]?.textContent ?? '').includes(dict.qy_ai_scope_fallback_name),
      `末行不是兜底档：${rows[1]?.textContent}`
    )
  })

  test('一档都没在监控时，说的是"没有分组在被监控"而不是留白', async () => {
    const active = await mountPage(scopes)
    const activeText = active.textContent ?? ''

    // 上面那份数据里唯一启用的策略是 async 50%，它是 active。这里换一份全 0 的：
    // 两档都是 0 = 配了等于没配，而那正是最需要一句话的时候。
    // 注意先把上一次的文本抄下来 —— mountPage 会卸载并移除上一个容器。
    const idle = {
      ...scopes,
      summary: scopes.summary.map((row) => ({
        ...row,
        pre_sample_rate_bps: 0,
        async_sample_rate_bps: 0,
      })),
    }
    const idleText = (await mountPage(idle)).textContent ?? ''

    assert.ok(
      activeText.includes(
        dict.qy_ai_scope_monitored_count.replace('{{count}}', '1')
      ),
      `有档在监控时没写出档数：${activeText.slice(0, 200)}`
    )
    assert.ok(
      idleText.includes(dict.qy_ai_scope_none_title),
      '零档时没有"没有分组在被监控"的告警'
    )
    assert.ok(
      !idleText.includes(
        dict.qy_ai_scope_monitored_count.replace('{{count}}', '0')
      ),
      '零档时不该再写"有 0 档正在被监控" —— 那句话读起来像配好了'
    )
  })

  /*
   * 响应缺 `summary` 时这一页仍然是一页。
   *
   * 断言落在**这张卡的标题还在**,而不是"没抛异常":抛异常的表现就是整块被
   * 错误边界换掉,标题跟着消失。少一张表远好过少一整页 —— 白屏时连
   * 「作用域读不出来」这句话都显示不了。
   */
  test('响应缺 summary 时不白屏，卡片标题仍在', async () => {
    const container = await mountPage({
      items: [],
      fallback_bps: 0,
      max_scopes: 64,
      ai_enabled: true,
      effective_active: true,
      active_scopes: [],
    })
    assert.ok(
      (container.textContent ?? '').includes(dict.qy_ai_scope_title),
      '缺 summary 时整张卡被打掉了'
    )
  })

  /*
   * 一张卡的降级响应不该打掉另一张卡。
   *
   * 这一页由四张卡拼成，它们同在一棵树上、共用**路由那一层**的错误边界：
   * 设置卡在渲染中途抛一次异常，作用域卡与渠道卡会一起消失，而运营看到的是
   * 「AI 审核这一页打不开了」—— 一个与真正的故障(设置接口降级)毫无相似之处
   * 的症状。所以爆炸半径必须被钉住，不能只钉每张卡自己。
   */
  test('设置卡的响应缺 setting 时，作用域卡仍然在页面上', async () => {
    const container = await mountPage(scopes, {
      default_prompt: '默认提示词',
      effective: { active: false, channels: 0 },
    })
    assert.ok(scopeTable(container), '设置卡降级把作用域卡一起带走了')
  })
})

/*
 * ── 变异验证（逐条改坏 `index.tsx` 再跑本文件，改完即还原并校验 sha256；
 *    括号里是实测结果）──
 *
 *  S1 页面里删掉 `<AiScopeCard />`                    → 4 条全红（卡片没接进页面）
 *  S2 `monitoredAll` 改回直读 `data.summary`          → 「缺 summary 时不白屏」红
 *     报错原文：`undefined is not an object (evaluating 'data.summary.map')`。
 *  S3 汇总表改成按 `id` 升序渲染（兜底档不再垫底）      → 「末行是兜底档」红
 *  S4 零档时不渲染 `qy_ai_scope_none_title`           → 「一档都没在监控时…」红
 *  S5 设置卡的 `data?.setting ?` 改回 `data ?`        → 「设置卡的响应缺 setting 时…」红
 *     报错原文：`undefined is not an object (evaluating 'data.setting.enabled')`，
 *     异常发生在 <AiSettingCard>，而**红的是作用域卡的用例** —— 那正是爆炸半径
 *     这件事的形状：坏的是别人，消失的是你。
 */
