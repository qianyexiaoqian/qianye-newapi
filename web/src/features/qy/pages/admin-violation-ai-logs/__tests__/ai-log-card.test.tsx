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
 * 审核日志页**在浏览器里**的样子。
 *
 * 这一页第一版是 AI 审核配置页底下的第五张卡,项目方打开那一页之后根本没看见
 * 它、直接问"日志表在哪" —— 纯逻辑用例一条都看不见这种失效,因为逻辑全对。
 * 所以这里挂真组件、真 i18n、真路由,问的全是"打开这一页能不能看见":
 *
 *  1. 项目方那句话的四个问号:哪个分组、哪个模型、谁触发的、内容在哪。
 *     前三个必须出现在表格行里;第四个是一个带字数的按钮(内容是 text 列,
 *     不进列表)。
 *  2. `content_chars = 0` 的行(留存这一列存在之前写下的)要明说「未留存」,
 *     而不是给一个点了打不开的按钮。
 *  3. 一份少了 `items` 的降级响应不能把整页打掉 —— 类型上它是必填的,
 *     运行期拿到的是"后端这次给了什么"。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { Window } from 'happy-dom'

import zh from '@/i18n/qy/zh.json'

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
  resources: { zh: { translation: zh as Record<string, string> } },
})

const { QyAdminViolationAiLogs } = await import('../index')
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

const row = {
  id: 900,
  review_no: 'ai_req-1_prompt',
  user_id: 42,
  username: 'alice',
  phase: 'prompt',
  channel_name: '自建审核端点',
  review_model: 'granite-guardian',
  outcome: 'violation',
  violated: true,
  category: 'jailbreak',
  confidence: '0.9100',
  reason: '尝试绕过系统提示',
  prompt_tokens: 100,
  completion_tokens: 20,
  total_tokens: 120,
  cost_usd: '0.0001',
  cost_unknown: false,
  attempts: 1,
  latency_ms: 320,
  rule_id: 5,
  record_id: 7,
  request_id: 'req-1',
  model_name: 'gpt-5',
  using_group: 'default',
  content_chars: 42,
  created_at: 1_780_000_000,
}

const logs = { items: [row], total: 1, page: 1, page_size: 20 }

// 不用 `| null` + `!`:这个夹具在 mountPage 之前不会渲染,给它一个空 client
// 做初值既避免了非空断言,也让"忘了 mountPage"表现为空页面而不是运行时崩溃。
let activeQueryClient = new QueryClient()

const rootRoute = createRootRoute({ component: Outlet })
const pageRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/qy/admin/violation-ai-logs',
  component: () => (
    <QueryClientProvider client={activeQueryClient}>
      <QyAdminViolationAiLogs />
    </QueryClientProvider>
  ),
})
// 配置页只需要存在,不需要渲染:右上角那个链接指向它,路由解析不到的话
// TanStack Router 会抛,而那与被测的东西无关。
const configRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/qy/admin/violation-ai-review',
  component: () => null,
})
const routeTree = rootRoute.addChildren([pageRoute, configRoute])

async function mountPage(logsPayload: unknown = logs) {
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
  queryClient.setQueryData(qyKeys.config(), { enabled: true })
  queryClient.setQueryData(qyKeys.adminViolationAiChannels(), {
    items: [{ id: 25, name: 'granite-guardian-2b', enabled: true }],
    key_configured: true,
  })
  queryClient.setQueryData(qyKeys.adminGroupOptions(), {
    options: [
      { name: 'default', ratio: 1, public_usable: true, has_channels: true },
    ],
    probe_ok: true,
  })
  // 键必须与组件算出来的那一份逐字相同:空筛选项在 api.ts 里被剥掉,
  // 所以初始查询的键就是 { p: 1, page_size: 20 }。
  queryClient.setQueryData(
    qyKeys.adminViolationAiLogs({ p: 1, page_size: 20 }),
    logsPayload
  )
  activeQueryClient = queryClient

  const router = createRouter({
    routeTree,
    history: createMemoryHistory({
      initialEntries: ['/qy/admin/violation-ai-logs'],
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

/** 日志表:按「送审内容」这个只有它才有的表头锚定。 */
function logTable(container: HTMLElement): Element | null {
  for (const table of container.querySelectorAll('table')) {
    const heads = [...table.querySelectorAll('thead th')].map((n) =>
      (n.textContent ?? '').trim()
    )
    if (heads.includes(dict.qy_ai_log_col_content)) return table
  }
  return null
}

describe('AI 审核日志页', () => {
  test('打开这一页就能看见日志表,分组/模型/触发用户都在行里', async () => {
    const container = await mountPage()
    const table = logTable(container)
    assert.ok(table, '页面上找不到审核日志表 —— 这一页只有这一件事要做')

    const rows = [...table.querySelectorAll('tbody tr')]
    assert.equal(rows.length, 1)
    const text = rows[0]?.textContent ?? ''
    for (const expected of ['default', 'gpt-5', 'alice', '#42']) {
      assert.ok(
        text.includes(expected),
        `行里缺少 ${expected}:这几列正是"哪个分组的哪个模型、谁触发的"的答案 —— ${text}`
      )
    }
    // 内容不进列表(text 列,一页 20 行会拖几十 KB),只给一个带字数的入口。
    assert.ok(
      text.includes(dict.qy_ai_log_view_content.replace('{{chars}}', '42')),
      `内容入口应当是一个标了字数的按钮:${text}`
    )
  })

  test('审核渠道与审核模型出现在行里,不是只藏在详情弹窗', async () => {
    // 项目方点名要的:「使用的审核渠道」。它一直在行数据上,但第一版只在
    // 详情弹窗里显示 —— 多渠道站点排障的第一刀恰恰是"是不是某一个渠道在误判",
    // 那一刀要在**列表**上就砍得下去。
    const container = await mountPage()
    const heads = [
      ...(logTable(container)?.querySelectorAll('thead th') ?? []),
    ].map((n) => (n.textContent ?? '').trim())
    assert.ok(
      heads.includes(dict.qy_ai_log_col_reviewer),
      `表头里没有「审核渠道 / 模型」:${heads.join(' | ')}`
    )
    const text =
      logTable(container)?.querySelector('tbody tr')?.textContent ?? ''
    for (const expected of ['自建审核端点', 'granite-guardian']) {
      assert.ok(text.includes(expected), `行里缺少 ${expected} —— ${text}`)
    }
  })

  test('审核耗时在行里,重试链把渠道数缀出来', async () => {
    // 耗时本身早就在行数据上,但一直没露出。它是这一页第三个高频问题
    // (前两个是"审了谁""凭什么"):**转发前**那一档的耗时是实打实加在用户
    // 首字节延迟上的,而抽样率调到多少合适,唯一的依据就是这一列。
    //
    // attempts 必须一起露:一次审核最多打三个渠道,3 秒可能是"这次真的慢",
    // 也可能是"前两个渠道各挂了一次"。没有分母的耗时读不出是哪一种。
    const container = await mountPage({
      ...logs,
      items: [{ ...row, latency_ms: 3200, attempts: 3 }],
    })
    const text =
      logTable(container)?.querySelector('tbody tr')?.textContent ?? ''
    assert.ok(text.includes('3200 ms'), `行里没有耗时:${text}`)
    assert.ok(
      text.includes(dict.qy_ai_log_attempts.replace('{{count}}', '3')),
      `重试链应当把渠道数缀出来:${text}`
    )
  })

  test('只打了一个渠道时不缀那句废话', async () => {
    // attempts=1 是绝大多数行。每一行都挂一句"打了 1 个渠道"会把真正异常的
    // 那几行淹掉 —— 这一列存在的意义就是让它们跳出来。
    const container = await mountPage()
    const text =
      logTable(container)?.querySelector('tbody tr')?.textContent ?? ''
    assert.ok(text.includes('320 ms'))
    assert.ok(
      !text.includes(dict.qy_ai_log_attempts.replace('{{count}}', '1')),
      `attempts=1 不该缀渠道数:${text}`
    )
  })

  test('没有留存内容的行不给入口,而是明说「未留存」', async () => {
    // 存量行(留存这一列存在之前写下的)与"那次请求没有可送审文本"都会落到 0。
    // 给它们一个点了打不开的按钮,只会让人以为功能坏了。
    const container = await mountPage({
      ...logs,
      items: [{ ...row, content_chars: 0 }],
    })
    const tr = logTable(container)?.querySelector('tbody tr')
    assert.ok(
      (tr?.textContent ?? '').includes(dict.qy_ai_log_no_content),
      `content_chars 为 0 的行必须显示「未留存」:${tr?.textContent}`
    )
  })

  test('右上角有一条回配置页的路', async () => {
    // 看日志的人最常问的第二个问题是"这些能留多久",而保留期在配置页上。
    const container = await mountPage()
    const link = [...container.querySelectorAll('a')].find((a) =>
      (a.textContent ?? '').includes(dict.qy_ai_log_go_config)
    )
    assert.ok(link, '找不到「去 AI 审核配置」')
    assert.equal(link.getAttribute('href'), '/qy/admin/violation-ai-review')
  })

  test('少了 items 的降级响应不把整页打掉', async () => {
    // 类型上 items 是必填的,但类型说的是"后端应该给",运行期拿到的是
    // "后端这次给了什么"(接口降级、老版本后端、反向代理裁剪)。
    // 直读一层会在渲染中途抛 TypeError,而这一页正是用来查审核记录的。
    const container = await mountPage({ total: 0, page: 1, page_size: 20 })
    assert.ok(
      (container.textContent ?? '').includes(dict.qy_ai_log_card_title),
      '缺字段的响应把整页打掉了'
    )
  })
})
