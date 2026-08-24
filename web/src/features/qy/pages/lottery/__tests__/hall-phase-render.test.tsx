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
 * 大厅的「进行中 / 已结束」到底分没分开 —— 在**真实渲染**上。
 *
 * # 被投诉的那个形状
 *
 * 项目方原话：「娱乐抽奖，当前已结束和进行中没有进行区分。」两张标签一直都在，
 * 断的是参数名：前端发 `status=open|done`，后端读 `phase`（只认 `live|ended`，
 * 且那个 switch 没有 default 分支）。于是两张标签发出两个不同的 URL、拿回
 * 同一份列表。库里实测 published/locked/settling 一条都没有、finished 有 64 条，
 * 所以用户点开「进行中」看到的是 64 条**已经结束**的活动。
 *
 * # 为什么必须渲染，不能只读源码
 *
 * 这个缺陷在类型层、在源码断言层全绿：`status: scope` 是合法 TS，
 * `QyLotActivityListParams.status?: string` 也接得住。唯一会露馅的地方是
 * **真的发出去的那个请求**，以及那一屏上真的出现了哪几张卡。所以这里把
 * axios adapter 换成一个既回数据、又把 URL 与 query 录下来的桩，
 * 断言分三层：发出去的参数、屏幕上的标题、屏幕上的结局文案。
 *
 * 文案走真实的 `src/i18n/qy/zh.json`：键写错时 i18next 原样吐键名，
 * 下面的中文断言当场变红。期望值一律在本文件里独立写出，不从产品代码回读。
 *
 * 变异实验见文件末尾。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { Window } from 'happy-dom'

import zhKeys from '@/i18n/qy/zh.json'

import type { QyLotActivityBrief } from '../types'

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

const { QyLotHallList } = await import('../components/lottery-hall-list')
// 这一页走 `@/lib/api` 那个 axios 实例（Bearer 注入、401 重试、GET 去重），
// 桩必须打在它的 adapter 上；打在 fetch 上整页会塌成「加载失败」，
// 于是所有断言以"什么都没渲染"的形态失败，看起来像页面坏了。
const httpClient = await import('@/lib/http-client')
const axiosApi = (
  httpClient as unknown as { api: { defaults: { adapter: unknown } } }
).api

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

const roots: { container: HTMLElement; root: { unmount: () => void } }[] = []
// axios 实例是**跨测试文件共享**的模块单例（bun 在同一个进程里跑整包）。
// 不还回去，后面每个文件的 HTTP 桩都会读到这里的假响应。
const originalAdapter = axiosApi.defaults.adapter
after(() => {
  axiosApi.defaults.adapter = originalAdapter
  for (const entry of roots) {
    entry.root.unmount()
    entry.container.remove()
  }
})

/** 一行活动。只填卡片真的会读的字段，其余给零值。 */
function activity(over: Partial<QyLotActivityBrief>): QyLotActivityBrief {
  return {
    act_no: 'LA-0',
    kind: 'draw',
    status: 'published',
    outcome: '',
    title: '未命名',
    stake_quota: 1000,
    open_at: 0,
    close_at: 0,
    draw_at: 0,
    active_count: 0,
    pool_quota: 0,
    prize_total_quota: 0,
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

type Mounted = {
  /** 每一次 `/lottery/activities` 请求带的 query，按发出顺序。 */
  asked: Record<string, unknown>[]
  /** 这一屏的根节点。断言颜色 / 属性时要用，不能只看文本。 */
  container: HTMLElement
  /** 点真实的标签按钮 —— 见下面 `clickTab` 上的说明。 */
  clickTab: (label: string) => Promise<void>
  text: () => string
}

/**
 * 挂一次大厅。
 *
 * `byPhase` 按分区给数据 —— 这正是被测的那件事：桩**只按 `phase` 分流**，
 * 前端要是把参数名写错（或写回 `status`），两张标签就都会落到 `undefined`
 * 那一支上，屏幕上出现的东西立刻不对。
 */
async function mountHall(
  byPhase: Record<string, QyLotActivityBrief[]>
): Promise<Mounted> {
  const asked: Record<string, unknown>[] = []
  axiosApi.defaults.adapter = async (config: {
    params?: Record<string, unknown>
    url?: string
  }) => {
    const url = String(config.url ?? '')
    let payload: unknown = { enabled: true, features: { lottery: true } }
    if (url.includes('/lottery/activities')) {
      const params = config.params ?? {}
      asked.push(params)
      const items = byPhase[String(params.phase)] ?? []
      payload = { items, total: items.length, p: 1, page_size: 12 }
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

  // 分段与页码由宿主（hub.tsx）持有，这里用一个最小宿主复现同一条接线：
  // 组件是受控的，自己不记 scope。
  const { useState } = await import('react')
  function Host() {
    const [scope, setScope] = useState<'ended' | 'live'>('live')
    const [page, setPage] = useState(1)
    return (
      <QueryClientProvider client={queryClient}>
        <QyLotHallList
          kind='draw'
          page={page}
          scope={scope}
          onPageChange={setPage}
          onScopeChange={setScope}
        />
      </QueryClientProvider>
    )
  }

  const rootRoute = createRootRoute({ component: Outlet })
  const pageRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/qy/lottery',
    component: Host,
  })
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: ['/qy/lottery'] }),
    routeTree: rootRoute.addChildren([pageRoute]),
  })
  await act(async () => {
    root.render(<RouterProvider router={router as never} />)
  })
  await act(async () => {
    await new Promise((r) => setTimeout(r, 60))
  })
  roots.push({ container, root })

  return {
    asked,
    container,
    text: () => container.textContent ?? '',
    /*
      切换必须走**真实的那颗按钮**，而不是直接调宿主的 setState。

      `TabsTrigger` 的 `value` 与 `onValueChange` 里那个三元是两个可以各自
      漂移的地方：把 value 写回 `'open' / 'done'`，三元会把 `'done'` 归成
      `'live'`，两张标签于是又变成同一份列表 —— 而绕开这颗按钮的测试对此
      全绿（实测：那次变异 5 pass）。所以这里去 DOM 里找按钮、真的点它。
    */
    clickTab: async (label) => {
      const trigger = Array.from(container.querySelectorAll('button')).find(
        (node) => (node.textContent ?? '').trim() === label
      )
      assert.ok(trigger, `标签栏上没有一颗写着「${label}」的按钮`)
      await act(async () => {
        trigger.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      })
      await act(async () => {
        await new Promise((r) => setTimeout(r, 60))
      })
    },
  }
}

const LIVE = [
  activity({ act_no: 'LA-live', status: 'published', title: '正在开放的一场' }),
  activity({ act_no: 'LA-lock', status: 'locked', title: '已封盘等开奖' }),
]
/** 六种结局各一场。标题带序号，方便断言"六张卡都在"。 */
const ENDED = [
  activity({
    act_no: 'LA-1',
    status: 'finished',
    outcome: 'drawn',
    title: '结局一',
  }),
  activity({
    act_no: 'LA-2',
    status: 'finished',
    outcome: 'cancelled',
    title: '结局二',
  }),
  activity({
    act_no: 'LA-3',
    status: 'finished',
    outcome: 'void_min_entries',
    title: '结局三',
  }),
  activity({
    act_no: 'LA-4',
    status: 'finished',
    outcome: 'void_no_winner',
    title: '结局四',
  }),
  activity({
    act_no: 'LA-5',
    status: 'finished',
    outcome: 'void_all_correct',
    title: '结局五',
  }),
  activity({
    act_no: 'LA-6',
    status: 'finished',
    outcome: 'void_deadline',
    title: '结局六',
  }),
]

describe('大厅分区：两张标签拿的是两份东西', () => {
  test('默认视图是「进行中」，请求带 phase=live，屏幕上只有进行中的活动', async () => {
    const hall = await mountHall({ live: LIVE, ended: ENDED })

    assert.deepEqual(
      hall.asked.map((p) => p.phase),
      ['live'],
      '默认视图必须直接问「进行中」——「我现在还能参加什么」是大厅要回答的第一个问题'
    )
    // 参数名写回 `status` 时，这一条与下面的标题断言会同时红。
    assert.ok(
      !('status' in hall.asked[0]),
      '大厅分区不该再发 `status`：后端那个键是「精确状态」，不是分区'
    )

    const screen = hall.text()
    assert.ok(screen.includes('正在开放的一场'), '进行中的活动没渲染出来')
    assert.ok(screen.includes('已封盘等开奖'))
    for (const ended of ['结局一', '结局二', '结局六']) {
      assert.ok(
        !screen.includes(ended),
        `「进行中」这一档渲染出了已结束的 ${ended} —— 正是项目方投诉的那个形状`
      )
    }
  })

  test('切到「已结束」后请求带 phase=ended，屏幕换成已结束的那一批', async () => {
    const hall = await mountHall({ live: LIVE, ended: ENDED })
    await hall.clickTab(zhKeys['qy_lot_tab_done'])

    assert.deepEqual(
      hall.asked.map((p) => p.phase),
      ['live', 'ended'],
      '切换标签必须真的换一个 phase 出去'
    )

    const screen = hall.text()
    assert.ok(screen.includes('结局一'), '已结束的活动没渲染出来')
    assert.ok(
      !screen.includes('正在开放的一场'),
      '「已结束」这一档还留着进行中的活动'
    )
  })
})

describe('已结束的那一档要能看出结局', () => {
  test('六种 outcome 各自渲染出自己的文案，没有一种写成「已结束」', async () => {
    const hall = await mountHall({ live: LIVE, ended: ENDED })
    await hall.clickTab(zhKeys['qy_lot_tab_done'])
    const screen = hall.text()

    // 期望值逐条手写，不从 `qyLotOutcomeKey` 回读 —— 那等于断言它等于它自己。
    const expected = [
      '已开奖',
      '已取消(全额退款)',
      '人数不足流局(全额退款)',
      '全部猜错(全额退款)',
      '无人猜错(全额退款)',
      '逾期未结算(全额退款)',
    ]
    for (const label of expected) {
      assert.ok(
        screen.includes(label),
        `结局文案「${label}」没出现在卡片上：六种结局分不出来 = 都写成了「已结束」`
      )
    }
    // 六句必须**互不相同**：全都回落成同一句时上面的断言仍可能碰巧过。
    assert.equal(new Set(expected).size, 6)
    // 键名漏译时 i18next 原样吐键名，这一条替上面把它抓住。
    assert.ok(
      !screen.includes('qy_lot_outcome_'),
      'i18n 键没翻译，卡片上出现的是键名'
    )
  })

  test('全额退款的那几场不许挂成功色的徽章', async () => {
    const hall = await mountHall({ live: LIVE, ended: ENDED })
    await hall.clickTab(zhKeys['qy_lot_tab_done'])

    /*
      断的是**颜色**，不是文字。

      这一条原来数的是屏幕上出现了几次「成功」二字，拿文案当颜色的替身。文案精简
      之后状态与结局合成了一枚徽章（一场取消的活动上不再写着「已取消 已取消
      (全额退款)」），那枚徽章的文字换成了结局本身 —— 于是「成功」出现 0 次，
      这条用例红了，而它声称守护的东西（绿色徽章只许出现在真的开出奖的那一场上）
      一点没变。替身与本体脱钩，说明该断的从来就是本体。

      `StatusBadge` 把 variant 落成 `textColorMap` 里的一个类，`success` →
      `text-success`。它是本仓自己的组件、自己导出的映射表，不是第三方内部实现。
    */
    const badges = Array.from(
      hall.container.querySelectorAll('[data-slot="status-badge"]')
    )
    assert.equal(badges.length, 6, '六张已结束的卡应当各挂一枚状态徽章')

    const successBadges = badges.filter((node) =>
      (node.getAttribute('class') ?? '').split(/\s+/).includes('text-success')
    )
    assert.equal(
      successBadges.length,
      1,
      `${successBadges.length} 张卡挂着成功色徽章：一场全额退款的活动挂绿色成功徽章，` +
        '是在告诉用户"一切正常"，而他刚发现钱退回来了'
    )
    // 绿的那一枚必须正是「已开奖」那一场，而不是碰巧只有一枚是绿的。
    assert.equal(
      (successBadges[0].textContent ?? '').trim(),
      zhKeys['qy_lot_outcome_drawn'],
      '成功色落在了错误的结局上'
    )
  })
})

describe('草稿不出现在用户端', () => {
  test('后端即使下发了草稿，大厅也不渲染它', async () => {
    // 说了算的是后端那句 `WHERE status <> 'draft'`（Go 侧
    // TestHallPhaseNeverLeaksDraft 守着）。这一条守的是第二道：
    // 列表查询被重构成"顺手也带上草稿"时，用户端这一屏仍然不许显示。
    const leaked = activity({
      act_no: 'LA-draft',
      status: 'draft',
      title: '还没发布的草稿场',
    })
    const hall = await mountHall({ live: [...LIVE, leaked], ended: ENDED })
    const screen = hall.text()

    assert.ok(
      screen.includes('正在开放的一场'),
      '同一次响应里的正常活动应当照常渲染 —— 否则下面那条断言只是"整页没渲染"'
    )
    assert.ok(
      !screen.includes('还没发布的草稿场'),
      '草稿漏给了用户：那是一份还没有承诺、随时可能被改掉的规则'
    )
  })
})

/*
 * ── 变异验证（逐条改产品代码、实测这些用例会不会红。baseline 5 pass / 0 fail）──
 *
 *  M1  `phase: scope` 写回 `status: scope`        → 0 pass / 5 fail
 *  M2  「进行中」那颗 TabsTrigger 的 value 写回 'open'
 *                                                  → 5 pass / 0 fail（**存活**）
 *  M2b `onValueChange` 里的三元改回比较 'done'     → 2 pass / 3 fail
 *  M2c 「已结束」那颗 TabsTrigger 的 value 写回 'done'
 *                                                  → 2 pass / 3 fail
 *  M3  `qyLotActivityBadgeStatus` 去掉 outcome 分支（finished 一律 success）
 *                                                  → 4 pass / 1 fail
 *  M4  `OUTCOME_BADGE.cancelled` 也染成 'success'  → 4 pass / 1 fail
 *  M5  去掉 hall-list 里的草稿过滤                  → 4 pass / 1 fail
 *  M6  改掉一条 `qy_lot_outcome_*` 的键名           → 4 pass / 1 fail
 *  M7  两张标签都发 `phase: 'live'`（分区形同虚设） → 2 pass / 3 fail
 *
 * M2 存活是已知且刻意接受的：它只让「进行中」那颗按钮在初次渲染时不高亮
 * （Tabs 的 value 是 'live'，没有一颗 trigger 认领它），列表数据、请求参数、
 * 切换行为全都不变。要钉住它得断言按钮的 `data-selected` 属性，那是 Base UI
 * 的内部实现细节，换一版组件库就会假红 —— 换来的只是一条纯样式的保护。
 * 功能性的那一半由 M2b / M2c 两条覆盖：value 与三元只要有一处漂移，
 * 「点了已结束却还在看进行中」立刻变红。
 *
 * ── 2026-08 文案精简之后的复核 ──
 *
 * 「全额退款的那几场不许挂成功色的徽章」那一条改了判据：从数屏幕上出现几次
 * 「成功」二字，改成数有几枚 `[data-slot="status-badge"]` 带着 `text-success`。
 * 起因是状态与结局合成了一枚徽章（此前一场取消的活动上并排写着「已取消」与
 * 「已取消(全额退款)」），「成功」二字于是一次都不出现，用例红了 —— 而它声称
 * 守护的那件事一点没变。M3 / M4 两条变异在新判据下实测仍然 4 pass / 1 fail，
 * 也就是说换掉的只是替身，抓力没丢。
 *
 * 另外记一条**方法论**上的教训：这些用例最初是直接调宿主的 setState 来换
 * 分段的，M2b 在那一版下 5 pass 全绿 —— 因为测试根本没经过 `onValueChange`。
 * 改成去 DOM 里找按钮、真的点它之后才抓住。绕开真实交互的"渲染测试"，
 * 测的是自己搭的那条线。
 */
