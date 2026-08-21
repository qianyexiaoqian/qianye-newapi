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
 * 「结算台」三合一之后，**不可见的标签一个请求都不发**。
 *
 * # 这条测试守的是什么
 *
 * 合并之前三张表是三个页面，谁也不会替谁打请求。收进同一个宿主之后，只要有人
 * 给 `QyPageTabs` 加上 `keepMounted`（Base UI 的 `Tabs.Panel` 支持，而且"切回来
 * 不闪"看起来是个改进），三张标签会同时挂载 —— 一进页面就是三份查询，其中
 * 日消费明细那条是**主库 logs 的区间聚合**，另一条还带着队列角标的统计。
 *
 * 这件事在界面上没有任何症状：页面照常渲染、数字照常对。只有服务器知道每次
 * 打开这一页多打了两份重查询。所以它必须由测试钉住，而且必须**数请求**，
 * 不能数"渲染了几个组件"。
 *
 * # 顺带钉住的另外两件事
 *
 *   · 切标签会把 hash 写进地址栏（刷新回到同一张、也能把这一屏发给同事）；
 *   · 切回来之后**不重打**已经取过的那条查询（react-query 的缓存兜住）——
 *     否则运营在三张表之间来回对账，每切一次就是一份新的大表聚合。
 *
 * # 为什么挂真的路由器
 *
 * 标签状态存在 URL hash 里，`useQyTabHash` 用的是 `useLocation` /
 * `useNavigate`。假掉路由器就等于假掉被测的那一半。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { after, describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { Window } from 'happy-dom'

const here = dirname(fileURLToPath(import.meta.url))
const srcDir = join(here, '..', '..', '..', '..', '..')

const domWindow = new Window({
  height: 900,
  url: 'http://localhost/qy/admin/settlement',
  width: 1280,
})
const domGlobals = [
  'window',
  'document',
  'navigator',
  'localStorage',
  'sessionStorage',
  'HTMLElement',
  'HTMLInputElement',
  'HTMLSelectElement',
  'HTMLTextAreaElement',
  'Node',
  'Element',
  'Event',
  'CustomEvent',
  'KeyboardEvent',
  'MouseEvent',
  'PointerEvent',
  'MutationObserver',
  'ResizeObserver',
  'IntersectionObserver',
  'requestAnimationFrame',
  'cancelAnimationFrame',
  'getComputedStyle',
  'matchMedia',
  'DOMRect',
] as const

for (const key of domGlobals) {
  const value = domWindow[key as keyof Window]
  if (value === undefined) continue
  Object.defineProperty(globalThis, key, { configurable: true, value })
}

// happy-dom 没有实现 scrollTo，而路由器每次导航都会调它 —— 缺了它导航会在
// 半路抛错，整棵树掉进 CatchBoundary，看起来像是断言写错了。
for (const target of [globalThis, domWindow]) {
  Object.defineProperty(target, 'scrollTo', {
    configurable: true,
    value: () => {},
  })
}

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
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
const i18next = (await import('i18next')).default
const { initReactI18next } = await import('react-i18next')

const zhBundle = JSON.parse(
  readFileSync(join(srcDir, 'i18n', 'qy', 'zh.json'), 'utf8')
) as Record<string, string>

await i18next.use(initReactI18next).init({
  interpolation: { escapeValue: false },
  lng: 'zh',
  nsSeparator: false,
  resources: { zh: { translation: zhBundle } },
})

const { api } = await import('@/lib/api')
const { ROLE } = await import('@/lib/roles')
const { useAuthStore } = await import('@/stores/auth-store')
const { QY_DISABLED_CONFIG } = await import('@/features/qy/lib/config-query')
const { qyKeys } = await import('@/features/qy/lib/query-keys')
const { qyTabHash } = await import('@/features/qy/lib/pages')
const { QY_API_PREFIX } = await import('@/features/qy/lib/api')
const { QyAdminSettlementHub } = await import('../hub')

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

/**
 * 每条出去的请求记一笔路径。这是本文件唯一的判据。
 *
 * 回包按端点给"空但结构完整"的一页：三张表都会去读各自的汇总字段
 * （日消费明细读 `range` / `summary`，提现审核读队列角标），少一个字段就是
 * 一次渲染期崩溃 —— 那会把整棵树打进 CatchBoundary，标签栏跟着消失，
 * 于是"点不到标签"看起来像是选择夹坏了，而真实原因只是夹具喂错了形状。
 */
let sent: string[] = []
const EMPTY_PAGE = { items: [], total: 0, p: 1, page_size: 20 }
function responseFor(path: string): Record<string, unknown> {
  const url = path.slice(QY_API_PREFIX.length)
  if (url.startsWith('/admin/commission/daily-consume')) {
    return {
      ...EMPTY_PAGE,
      index_ready: true,
      accrual_users_without_logs: 0,
      range: { start_date: '20260820', end_date: '20260820', days: 1 },
      summary: { user_count: 0, request_count: 0 },
    }
  }
  if (url.startsWith('/admin/withdraw/stats')) {
    return { buckets: [], sla_breached: 0, payout_sla_breached: 0 }
  }
  if (url.startsWith('/admin/commission/health')) {
    return {
      daily_settle: {
        today: '20260821',
        day_offset_minutes: 480,
        next_run_after: 1787328000,
        max_attempts: 5,
        payout_day_offset: 1,
        ran_today: false,
      },
    }
  }
  return EMPTY_PAGE
}

api.defaults.adapter = async (config) => {
  const url = String(config.url)
  // 查询串一起记下来：axios 把它放在 `config.params` 里，只记 `config.url`
  // 的话"换了筛选之后重新取数"这件事在记录上完全看不出来。
  const params = new URLSearchParams(
    Object.entries((config.params ?? {}) as Record<string, unknown>).map(
      ([key, value]) => [key, String(value)]
    )
  ).toString()
  sent.push(params === '' ? url : `${url}?${params}`)
  return {
    data: { success: true, data: responseFor(url) },
    status: 200,
    statusText: 'OK',
    headers: {},
    config,
  }
}

/** 每张标签自己那条主查询的路径前缀，用来把请求归到标签上。 */
const TAB_ENDPOINTS: Readonly<Record<string, string>> = {
  '/qy/admin/daily-consume': '/admin/commission/daily-consume',
  '/qy/admin/commission-records': '/admin/commission/records',
  '/qy/admin/withdrawals': '/admin/withdraw',
}

function hits(prefix: string): number {
  return sent.filter((url) => url.startsWith(`${QY_API_PREFIX}${prefix}`))
    .length
}

/**
 * 只数 qy 自己的请求。
 *
 * 上游的会话探测（`/api/user/2fa/status`、`/api/user/passkey`）挂在布局层，
 * 与"这一页的标签发了几条请求"无关；把它们算进来只会让这条断言随上游改动
 * 而红，而那种红没有任何信息量。
 */
function qyRequests(): string[] {
  return sent.filter((url) => url.startsWith(QY_API_PREFIX))
}

const cleanups: Array<() => Promise<void>> = []
after(async () => {
  for (const fn of cleanups) await fn()
})

async function mountHub() {
  sent = []

  useAuthStore.setState((state) => ({
    ...state,
    auth: {
      ...state.auth,
      user: {
        id: 1,
        username: 'qy-probe-admin',
        role: ROLE.SUPER_ADMIN,
        status: 1,
      },
      accessToken: 'probe',
    },
  }))

  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: Infinity } },
  })
  // 扩展配置直接塞进缓存：它是 `useQyConfig` 的数据源，不预置的话每次挂载都会
  // 多一条 `/config` 请求，而那条与"标签发了几个请求"无关。
  queryClient.setQueryData(qyKeys.config(), {
    ...QY_DISABLED_CONFIG,
    enabled: true,
    available: true,
    features: {
      ...QY_DISABLED_CONFIG.features,
      commission: true,
      withdraw: true,
    },
  })

  const rootRoute = createRootRoute({ component: Outlet })
  const hubRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/qy/admin/settlement',
    validateSearch: (search: Record<string, unknown>) => ({
      inviter_id:
        typeof search.inviter_id === 'string' ? search.inviter_id : '',
    }),
    component: QyAdminSettlementHub,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([hubRoute]),
    history: createMemoryHistory({ initialEntries: ['/qy/admin/settlement'] }),
  })

  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  cleanups.push(async () => {
    await act(async () => root.unmount())
    container.remove()
  })

  await act(async () => {
    root.render(
      <QueryClientProvider client={queryClient}>
        {/* eslint-disable-next-line @typescript-eslint/no-explicit-any */}
        <RouterProvider router={router as any} />
      </QueryClientProvider>
    )
  })

  return { container, router }
}

/** 按标签标题点一下标签栏上的触发器。 */
async function clickTab(container: HTMLElement, label: string) {
  const trigger = [...container.querySelectorAll('button')].find(
    (node) => node.textContent?.trim() === label
  )
  assert.ok(trigger != null, `标签栏上找不到「${label}」`)
  await act(async () => {
    trigger.click()
  })
}

describe('结算台的三张标签', () => {
  test('一进页面只有第一张标签在取数，另外两张一条请求都没发', async () => {
    const { container } = await mountHub()

    assert.equal(
      hits(TAB_ENDPOINTS['/qy/admin/daily-consume'] as string),
      1,
      '第一张标签（日消费明细）没有取数'
    )
    assert.equal(
      hits(TAB_ENDPOINTS['/qy/admin/commission-records'] as string),
      0,
      '佣金审核在后台偷偷取了数：一进页面就是三份查询'
    )
    assert.equal(
      hits(TAB_ENDPOINTS['/qy/admin/withdrawals'] as string),
      0,
      '提现审核在后台偷偷取了数（列表 + 队列角标两条）'
    )
    assert.deepEqual(
      qyRequests().length,
      1,
      `一进页面只该有一条 qy 请求，实际打了 ${qyRequests().length} 条：${qyRequests().join(' ')}`
    )

    // 三张标签的标题都在标签栏上 —— 不可见 ≠ 点不到。
    for (const label of ['日消费明细', '佣金审核', '提现审核']) {
      assert.ok(
        [...container.querySelectorAll('button')].some(
          (node) => node.textContent?.trim() === label
        ),
        `标签栏上少了「${label}」`
      )
    }
  })

  test('切到佣金审核：hash 跟着变，只有这一张标签开始取数', async () => {
    const { container, router } = await mountHub()
    sent = []

    await clickTab(container, '佣金审核')

    assert.equal(
      router.state.location.hash,
      qyTabHash('/qy/admin/commission-records'),
      '切标签没有写进地址栏：刷新会回到第一张，也没法把这一屏发给同事'
    )
    assert.equal(
      hits(TAB_ENDPOINTS['/qy/admin/commission-records'] as string),
      1,
      '切过去了但没取数'
    )
    assert.equal(
      hits(TAB_ENDPOINTS['/qy/admin/daily-consume'] as string),
      0,
      '切走的那张标签还在重打它的大表聚合'
    )
    assert.equal(
      hits(TAB_ENDPOINTS['/qy/admin/withdrawals'] as string),
      0,
      '没被选中的提现审核也在取数'
    )
    // 这一张标签额外打一条结算调度快照：撤掉「立即结算」之后，"什么时候
    // 自动到账"就是靠它回答的。它是**这一张标签**的一部分，所以只在这里出现。
    assert.equal(
      hits('/admin/commission/health'),
      1,
      '佣金审核那段"什么时候到账"的说明没有取到结算调度快照'
    )
    assert.equal(
      qyRequests().length,
      2,
      `切到佣金审核只该打 2 条 qy 请求（计佣流水 + 结算调度），实际 ${qyRequests().length} 条：${qyRequests().join(' ')}`
    )
  })

  test('切到提现审核：列表 + 队列角标两条，别的标签不出声', async () => {
    const { container } = await mountHub()
    sent = []

    await clickTab(container, '提现审核')

    assert.equal(
      hits(TAB_ENDPOINTS['/qy/admin/withdrawals'] as string),
      2,
      '提现审核这一屏是列表 + 队列角标两条查询'
    )
    assert.equal(
      hits(TAB_ENDPOINTS['/qy/admin/daily-consume'] as string),
      0,
      '切走的日消费明细还在取数'
    )
    assert.equal(
      hits(TAB_ENDPOINTS['/qy/admin/commission-records'] as string),
      0,
      '没被选中的佣金审核也在取数'
    )
    assert.equal(
      qyRequests().length,
      2,
      `切到提现审核只该打 2 条 qy 请求，实际 ${qyRequests().length} 条：${qyRequests().join(' ')}`
    )
  })

  /*
   * 「切走再切回来，这一张标签自己的筛选保不保留」——**不保留**，这是刻意的。
   *
   * 保留只有两条实现路径，两条都比"重打一次筛选"贵：
   *   · `keepMounted` 让三张标签常驻 —— 那正好把上面三条断言全部推翻，
   *     一进页面就是三份查询；
   *   · 把三组筛选全部提到 URL 里 —— 地址栏会长出十来个参数，而且三张表都有
   *     `keyword`、都有分页，撞名之后一处改动会串到另一张表上。
   *
   * 真正需要跨页活下来的只有**下钻**那一个参数（`?inviter_id=`），它本来就在
   * URL 上，重定向也会转发。所以这里把"标签内筛选是临时的"钉成契约：
   * 谁要改成保留，必须先回答上面那两条代价，而不是顺手加个 `keepMounted`。
   */
  test('切走再切回来：这一张标签的筛选回到默认，不保留', async () => {
    const { container } = await mountHub()

    const keyword = [...container.querySelectorAll('input')].find(
      (node) => node.getAttribute('placeholder') === zhBundle.qy_dc_keyword_ph
    )
    assert.ok(keyword != null, '日消费明细上找不到关键词输入框')

    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(
        keyword.constructor.prototype as object,
        'value'
      )?.set
      setter?.call(keyword, 'qy-probe')
      keyword.dispatchEvent(new Event('input', { bubbles: true }))
    })
    assert.ok(
      qyRequests().some((url) => url.includes('keyword=qy-probe')),
      '改了筛选却没有重新取数'
    )

    await clickTab(container, '提现审核')
    sent = []
    await clickTab(container, '日消费明细')

    const back = [...container.querySelectorAll('input')].find(
      (node) => node.getAttribute('placeholder') === zhBundle.qy_dc_keyword_ph
    )
    assert.equal(
      back?.value,
      '',
      '筛选跨标签活下来了：那意味着这张标签被 keepMounted 常驻，隐藏时也在取数'
    )
    assert.equal(
      qyRequests().filter((url) => url.includes('keyword=qy-probe')).length,
      0,
      '切回来之后还在按旧关键词取数'
    )
  })
})
