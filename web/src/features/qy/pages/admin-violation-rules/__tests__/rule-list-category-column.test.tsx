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
 * 「这条规则命中了记到哪里」必须在**列表页**就看得见。
 *
 * 规则列表原来有九列（优先级/名称/阶段/匹配方式/处置/计费/作用域/状态/更新时间），
 * 唯独没有类型。于是"二十几条规则分别归到哪一类"只能一条一条打开抽屉去数 ——
 * 而项目方问的正是这件事。缺一列不会报错、不会变红，它只是让一个问题永远
 * 答不上来，所以必须有一条用例钉住它。
 *
 * 两件事一起钉：
 *   - 类型**名字**（记到哪个桶）；
 *   - 那个桶的**阈值状态**（记满了会不会有事）。少了后者，一个阈值为 0 的桶
 *     在列表上与"已经配好了"长得一模一样。
 *
 * 以及 `category_id = 0` 的历史规则：运行期后端把它折进兜底类型，列表也必须
 * 按同一口径显示 —— 列表说「未分类」而后端记到别处，是最难查的那种不一致。
 *
 * 变异实验见文件末尾。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { Window } from 'happy-dom'

import zh from '@/i18n/qy/zh.json'

import type { QyViolationRule } from '../types'

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

// TanStack Router 在每次导航后调用 `scrollTo`，happy-dom 没有实现它。
// 这里只需要它别抛 —— 滚动位置不参与任何断言。
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
// 这一页的外壳 `QySectionPageLayout` 用了 `useLocation`，没有路由上下文直接抛，
// 所以挂一个只有一条路由的内存路由 —— 它只是让外壳能渲染，不参与任何断言。
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

const { QyAdminViolationRules } = await import('../index')
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

function rule(overrides: Partial<QyViolationRule>): QyViolationRule {
  return {
    id: 1,
    name: '规则',
    remark: '',
    category_id: 0,
    enabled: true,
    mode: 'shadow',
    priority: 100,
    phase: 'prompt',
    match_type: 'keyword',
    pattern: 'x',
    action: 'record_only',
    fee_mode: 'none',
    fee_fixed: '0',
    fee_multiple: '0',
    fee_max_quota: 0,
    public_reason: '',
    block_message: '',
    group_scope_mode: 'all',
    group_scope: '',
    model_scope: '',
    status_scope: '',
    source: 'custom',
    window_seconds: 0,
    rate_threshold: 0,
    created_at: 0,
    updated_at: 0,
    ...overrides,
  } as unknown as QyViolationRule
}

const categories = {
  fallback_id: 1,
  threshold_semantics: 'any_line',
  items: [
    {
      rule_count: 1,
      threshold_state: 'active',
      category: {
        id: 2,
        key: 'jailbreak',
        name: '破限(越狱)',
        remark: '内部判据，绝不渲染到用户端',
        public_title: '绕过安全策略',
        public_desc: '',
        published: true,
        enabled: true,
        window_hours: 24,
        threshold: 3,
        sort_order: 10,
        is_fallback: false,
        created_at: 0,
        updated_at: 0,
      },
    },
    {
      rule_count: 2,
      threshold_state: 'unset',
      category: {
        id: 1,
        key: 'uncategorized',
        name: '未分类',
        remark: '',
        public_title: '',
        public_desc: '',
        published: false,
        enabled: false,
        window_hours: 24,
        threshold: 0,
        sort_order: 999,
        is_fallback: true,
        created_at: 0,
        updated_at: 0,
      },
    },
  ],
}

let activeQueryClient: InstanceType<typeof QueryClient> | null = null

const rootRoute = createRootRoute({ component: Outlet })
const pageRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/qy/admin/violation-rules',
  component: () => (
    <QueryClientProvider client={activeQueryClient!}>
      <QyAdminViolationRules />
    </QueryClientProvider>
  ),
})
const routeTree = rootRoute.addChildren([pageRoute])

async function mountList(
  rules: QyViolationRule[],
  withCategories = true,
  statsOverride?: unknown
) {
  for (const entry of roots.splice(0)) {
    entry.root.unmount()
    entry.container.remove()
  }
  await act(async () => {})

  // 预置的数据一挂载就会被判为陈旧并触发真实 fetch，而这里没有网络；
  // 那次失败会把列表清空，表现为"单跑绿、合跑红"。
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, refetchOnMount: false } },
  })
  queryClient.setQueryData(
    qyKeys.adminViolationRules({
      p: 1,
      page_size: 20,
      phase: undefined,
      keyword: undefined,
    }),
    { items: rules, total: rules.length, page: 1, page_size: 20 }
  )
  if (withCategories) {
    queryClient.setQueryData(qyKeys.adminViolationCategories(), categories)
  }
  // 统计也必须预置。它与类型列无关，但页面顶部直接读 `data.breaker.*`：
  // 不给的话这次查询会去发真实请求，整包跑时别的用例装的全局 fetch 桩会回一份
  // 空对象，于是页面在渲染中途抛错、表格根本不渲染 —— 症状是"单跑绿、合跑红"。
  queryClient.setQueryData(
    qyKeys.adminViolationStats(),
    statsOverride ?? {
      hours: 24,
      record_count: 0,
      blocked: 0,
      shadow_count: 0,
      fee_quota: 0,
      clamp_count: 0,
      ban_count: 0,
      by_rule: [],
      by_model: [],
      breaker: { rate_local_hits: 0 },
      rules: {
        version: 1,
        loaded_at: 0,
        prompt_rule: 1,
        post_rule: 0,
        shadow_rule: 1,
        enforce_rule: 0,
      },
      policy: {
        insufficient_balance: 'record_only',
        auto_ban_threshold: 0,
        auto_ban_window_h: 24,
      },
    }
  )
  // qy 扩展开关。`QyPageBoundary` 在 `status === 'disabled'` 时整块换成
  // 「本站未启用该功能」的空态，表格根本不渲染。单跑时这次查询失败 →
  // status 是 unknown → 照常放行；整包跑时别的用例装的桩会让它落到"关闭"，
  // 于是这几条用例只在合跑时红。显式给它一份"开着"的配置。
  queryClient.setQueryData(qyKeys.config(), { enabled: true })
  activeQueryClient = queryClient

  const router = createRouter({
    routeTree,
    history: createMemoryHistory({
      initialEntries: ['/qy/admin/violation-rules'],
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

/**
 * 规则表。
 *
 * 不能拿页面上第一张表：违规计数卡（用户 / 窗口内次数 / 累计次数 …）排在规则表
 * 前面，统计一有数据它就渲染出来。按「优先级」这个只有规则表才有的表头锚定。
 */
function rulesTable(container: HTMLElement): Element {
  const tables = [...container.querySelectorAll('table')]
  for (const table of tables) {
    // 全选框只有规则表有。不按表头文字锚定：i18next 是模块单例，整包跑时
    // 别的用例可能已经把它初始化成另一份资源，届时 `t()` 返回的是 key 本身。
    if (table.querySelector('thead [role="checkbox"]') != null) return table
  }
  assert.fail(`页面上找不到规则表（共 ${tables.length} 张表）`)
}

/** 规则表的表头文字，按屏幕上的先后顺序。 */
function headers(container: HTMLElement): string[] {
  return [...rulesTable(container).querySelectorAll('thead th')].map((node) =>
    (node.textContent ?? '').trim()
  )
}

/** 规则表指定行的单元格文字。 */
function rowCells(container: HTMLElement, index: number): string[] {
  const row = rulesTable(container).querySelectorAll('tbody tr')[index]
  assert.ok(row, `第 ${index} 行不存在`)
  return [...row.querySelectorAll('td')].map((node) =>
    (node.textContent ?? '').trim()
  )
}

describe('规则列表的违规类型列', () => {
  test('表头有这一列，且紧挨着规则名称', async () => {
    const container = await mountList([rule({ id: 1, category_id: 2 })])
    const cols = headers(container)
    const at = cols.indexOf(dict.qy_vio_col_category)
    assert.notEqual(at, -1, `列表没有类型列：${cols.join(' / ')}`)
    assert.equal(
      cols[at - 1],
      dict.qy_vio_field_name,
      `类型列没有紧跟规则名称：${cols.join(' / ')}`
    )
  })

  test('配了阈值的类型，同时写出名字与「N 次 / H 小时」', async () => {
    const container = await mountList([rule({ id: 1, category_id: 2 })])
    const cells = rowCells(container, 0)
    const at = headers(container).indexOf(dict.qy_vio_col_category)
    const text = cells[at] ?? ''
    assert.ok(text.includes('破限(越狱)'), `类型名缺失：${text}`)
    assert.ok(
      text.includes(
        dict.qy_vcat_threshold_value
          .replace('{{count}}', '3')
          .replace('{{hours}}', '24')
      ),
      `阈值状态缺失：${text}`
    )
  })

  test('没配阈值的类型标成「不单独触发」，不与生效档共用一种写法', async () => {
    const container = await mountList([rule({ id: 1, category_id: 1 })])
    const at = headers(container).indexOf(dict.qy_vio_col_category)
    const text = rowCells(container, 0)[at] ?? ''
    assert.notEqual(text, dict.qy_vio_col_category_none)
    assert.ok(text.includes('未分类'), `类型名缺失：${text}`)
    assert.ok(
      text.includes(dict.qy_vcat_flag_fallback),
      `兜底标记缺失：${text}`
    )
    assert.ok(
      text.includes(dict.qy_vcat_threshold_off),
      `阈值状态缺失：${text}`
    )
  })

  test('category_id=0 的历史规则按兜底类型显示，与后端口径一致', async () => {
    const container = await mountList([rule({ id: 1, category_id: 0 })])
    const at = headers(container).indexOf(dict.qy_vio_col_category)
    const text = rowCells(container, 0)[at] ?? ''
    // 不能只看“写了未分类”：读不到清单时的兑底文案本身就是
    // 「未分类(兜底)」，两者在字面上分不开。真正折进兜底类型时，
    // 那一行的阈值状态会一起显示 —— 那才是“真的查到了那一行”的证据。
    assert.notEqual(text, dict.qy_vio_col_category_none)
    assert.ok(text.includes('未分类'), `没折进兜底类型：${text}`)
    assert.ok(
      text.includes(dict.qy_vcat_flag_fallback),
      `兜底标记缺失：${text}`
    )
    assert.ok(
      text.includes(dict.qy_vcat_threshold_off),
      `阈值状态缺失：${text}`
    )
  })

  test('类型清单读不出来时退回中文措辞，不漏裸主键', async () => {
    const container = await mountList([rule({ id: 1, category_id: 2 })], false)
    const at = headers(container).indexOf(dict.qy_vio_col_category)
    const text = rowCells(container, 0)[at] ?? ''
    assert.equal(text, dict.qy_vio_col_category_none)
  })

  /*
   * 统计接口少给一个子对象时，这一页仍然要能用。
   *
   * 页面顶部（降级提示）与影子横幅都在读 `stats.breaker.*` / `stats.rules.*`。
   * 类型上它们是必填的，但类型说的是「后端应该给」，运行期拿到的是「后端这次
   * 给了什么」：接口降级、老版本后端、反向代理裁剪字段都会让子对象缺席，而
   * 直读一层会在渲染中途抛 TypeError —— 整页白屏，规则表一行都不剩。
   *
   * 而这一页正是项目方要看"规则归到哪一类"的那一页：一份可有可无的统计
   * 不该有能力把它整个打掉。断言落在**规则表仍在**，不是"没抛异常"。
   */
  test('统计缺 breaker / rules 子对象时，规则表照常渲染（不白屏）', async () => {
    const container = await mountList([rule({ id: 1, category_id: 2 })], true, {
      hours: 24,
      record_count: 0,
      blocked: 0,
      shadow_count: 0,
      fee_quota: 0,
      clamp_count: 0,
      ban_count: 0,
      by_rule: [],
      by_model: [],
    })
    const cols = headers(container)
    assert.notEqual(
      cols.indexOf(dict.qy_vio_col_category),
      -1,
      `统计降级后规则表没渲染出来：${cols.join(' / ')}`
    )
    const at = cols.indexOf(dict.qy_vio_col_category)
    assert.ok(
      (rowCells(container, 0)[at] ?? '').includes('破限(越狱)'),
      '统计降级后规则行没渲染'
    )
  })
})

/*
 * ── 变异验证（逐条改坏 `index.tsx` 再跑本文件，改完即还原；括号里是实测结果）──
 *
 *  L1 删掉整个 `id: 'category'` 列                          → 5 条全红
 *  L2 把这一列挪到最右（`actions` 之前）                     → 「紧挨着规则名称」红
 *     位置不是审美：类型是"这条规则算给谁"，挤到最右边等于又要横向找一次。
 *  L3 把阈值徽标两档一起删，只留类型名                       → 3 条红
 *  L4 `row.category_id > 0 ? … : categoryFallbackId`
 *     改成 `row.category_id`                                → 「category_id=0 …」红
 *     这一条最初没被抓到：读不到清单时的兜底文案本身就是「未分类(兜底)」，
 *     与真的折进兜底类型在字面上分不开。补上「阈值状态必须一起出现」之后才红 ——
 *     用例现在断言的是"真的查到了那一行"，不是"字里有未分类三个字"。
 *  L5 读不到清单时改渲染 `row.category_id`                   → 「类型清单读不出来时…」红
 */
