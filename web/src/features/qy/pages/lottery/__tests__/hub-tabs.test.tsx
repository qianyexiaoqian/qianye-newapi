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
 * 娱乐大厅拆成四张玩法选择夹（抽奖 / 竞猜 / 双色球 / 星屑转盘）之后的整页行为。
 *
 * 项目方原话：「把双色球和竞猜分开选择夹，抽奖-竞猜-双色球。
 * （每个入口都可以单独被隐藏或显示）」；2026-09-05 追加：「星屑转盘的页面移动
 * 到抽奖竞猜里面去。」
 *
 * # 这里守的四件事，每一件都只在"真的挂起来"时才看得见
 *
 *  1. **玩法归类不串**。三张大厅标签发的是三个不同的 `lane`，而 `lane` 的取值与
 *     `kind` 长得一样却不同义（`lane='draw'` 排除双色球）。写回 `kind` 在两侧
 *     都编译得过、类型都对，后果是双色球标签里长出普通抽奖 —— 只有"发出去的
 *     那个请求"能说明问题，所以这里数请求、也读屏幕。转盘那张不发 `lane`、
 *     发 `draw_mode=wheel`（两者同给是 400）—— 同样只有请求能说明问题。
 *  2. **不可见的标签一个请求都不发**。只要有人给 `QyPageTabs` 加上
 *     `keepMounted`（Base UI 支持，而且"切回来不闪"看起来像个改进），五张标签
 *     会同时挂载 —— 一进页面就是四份列表查询 + 一份我的参与。界面上没有任何
 *     症状，只有服务器知道。
 *  3. **隐藏组合**。五个玩法开关 × 四张标签的合并口径：抽奖那张底下压着两种
 *     玩法，两种都关掉时它才消失；双色球、竞猜、转盘各自只压一种。反向的错法是
 *     "标签开着、底下两种玩法都关"——一张永远空的列表，而运营看到的是"已开启"。
 *  4. **已参与的人不受影响**。这是这一整条改动里唯一不能让步的一条：任何隐藏
 *     组合下，「我的参与」都必须还在、还能查到自己那一票、还能把文本奖领出来。
 *     所以最后一组用例在**每一种**组合下都真的点开那张标签、点开那颗领奖按钮，
 *     并断言兑换码出现在屏幕上。
 *
 * # 为什么挂真的路由器
 *
 * 标签状态存在 URL hash 里（`useQyTabHash` 用的是 `useLocation`/`useNavigate`）。
 * 假掉路由器就等于假掉被测的那一半，而"刷新回到同一张标签"正是要测的行为。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { after, describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { Window } from 'happy-dom'

import type { QyLotPlays } from '@/features/qy/lib/types'

const here = dirname(fileURLToPath(import.meta.url))
// __tests__ -> lottery -> pages -> qy -> features -> src
const srcDir = join(here, '..', '..', '..', '..', '..')

const domWindow = new Window({
  height: 900,
  url: 'http://localhost/qy/lottery',
  width: 1280,
})
for (const key of [
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
] as const) {
  const value = domWindow[key as keyof Window]
  if (value === undefined) continue
  Object.defineProperty(globalThis, key, { configurable: true, value })
}
// happy-dom 没实现 scrollTo，而路由器每次导航都会调它 —— 缺了它导航会在半路
// 抛错，整棵树掉进 CatchBoundary，看起来像是断言写错了。
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
  Outlet,
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
} = await import('@tanstack/react-router')
const i18next = (await import('i18next')).default
const { initReactI18next } = await import('react-i18next')

const zh = JSON.parse(
  readFileSync(join(srcDir, 'i18n', 'qy', 'zh.json'), 'utf8')
) as Record<string, string>

await i18next.use(initReactI18next).init({
  interpolation: { escapeValue: false },
  lng: 'zh',
  nsSeparator: false,
  resources: { zh: { translation: zh } },
})

const { api } = await import('@/lib/api')
const { ROLE } = await import('@/lib/roles')
const { useAuthStore } = await import('@/stores/auth-store')
const { QY_API_PREFIX } = await import('@/features/qy/lib/api')
const { QY_DISABLED_CONFIG } = await import('@/features/qy/lib/config-query')
const { qyKeys } = await import('@/features/qy/lib/query-keys')
const { qyTabHash } = await import('@/features/qy/lib/pages')
const { QyLotteryHub } = await import('../hub')

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

/** 五张标签的中文标题。手写，不从 QY_PAGES 回读。 */
const TAB_DRAW = '抽奖'
const TAB_GUESS = '竞猜'
const TAB_BALL = '双色球'
const TAB_WHEEL = '星屑转盘'
const TAB_RECORDS = '我的参与'

const ALL_ON: QyLotPlays = {
  draw_ball: true,
  draw_prob: true,
  draw_rank: true,
  guess: true,
  wheel: true,
}

/**
 * 三个 lane 与转盘各一场活动，标题互不相同。
 *
 * 桩**只按 `lane` 分流**（转盘按 `draw_mode=wheel`）：前端要是把参数名写错
 * （或写回 `kind`），几张标签会一起落到 undefined 那一支上，屏幕上出现的东西
 * 立刻不对。
 */
const BY_LANE: Record<string, { act_no: string; title: string }> = {
  ball: { act_no: 'H-ball', title: '第 7 期双色球' },
  draw: { act_no: 'H-draw', title: '一场普通抽奖' },
  guess: { act_no: 'H-guess', title: '一场竞猜' },
  wheel: { act_no: 'H-wheel', title: '一场星屑转盘' },
}

function activityFor(lane: string) {
  const base = BY_LANE[lane]
  if (base == null) return []
  if (lane === 'wheel') {
    // 转盘卡片要画"各档剩余 / 初始"，读的是 `tiers`；其余字段与大厅卡同形。
    return [
      {
        act_no: base.act_no,
        active_count: 0,
        close_at: 4_102_444_800,
        currency: 'stardust',
        draw_at: 4_102_444_801,
        draw_mode: 'wheel',
        kind: 'draw',
        my_entry_count: 0,
        open_at: 0,
        outcome: '',
        pool_quota: 0,
        prize_total_quota: 500,
        stake_quota: 100,
        status: 'published',
        tiers: [
          {
            amount_quota: 500,
            count: 2,
            name: '头奖',
            prize_type: 'quota',
            stock_left: 1,
            tier: 1,
            win_ppm: 300_000,
          },
          { name: '谢谢参与', prize_type: 'none', tier: 2, win_ppm: 700_000 },
        ],
        title: base.title,
      },
    ]
  }
  return [
    {
      act_no: base.act_no,
      active_count: 0,
      ball_blue_pick: 1,
      ball_blue_pool: 16,
      ball_red_pick: 6,
      ball_red_pool: 33,
      ball_result: '',
      close_at: 0,
      draw_at: 0,
      draw_mode: lane === 'ball' ? 'ball' : 'rank',
      issue_no: lane === 'ball' ? 7 : 0,
      kind: lane === 'guess' ? 'guess' : 'draw',
      my_entry_count: 0,
      open_at: 0,
      outcome: '',
      pool_open_quota: 0,
      pool_quota: 0,
      prize_total_quota: 0,
      series_no: '',
      stake_quota: 1000,
      status: 'published',
      title: base.title,
    },
  ]
}

/** 我那张已经中了文本奖的票。任何隐藏组合下它都必须还查得到、还领得走。 */
const MY_ENTRY = {
  act_no: 'H-ball',
  amount: 1000,
  ball_result: '01,02,03,04,05,06|07',
  chain_hash: 'abcdef0123456789abcdef',
  created_at: 1787000000,
  draw_mode: 'ball',
  entry_no: 'E-mine-1',
  kind: 'draw',
  opt_no: 0,
  pick: '01,02,03,04,05,06|07',
  seq: 1,
  status: 'success',
  title: '第 7 期双色球',
  user_ref: 'u-1',
  won: {
    amount: 0,
    fulfilled: true,
    kind: 'text',
    payout_no: 'P-mine-1',
    tier: 1,
  },
}

const MY_PRIZE_SECRET = 'QY-CDK-8FN2-TEST'
const MY_PRIZE = {
  act_no: 'H-ball',
  fulfilled_at: 1787000100,
  name: '一等奖',
  note: '',
  notice: '',
  payout_no: 'P-mine-1',
  secret: MY_PRIZE_SECRET,
  status: 'fulfilled',
  text_desc: '一张兑换码',
  tier: 1,
  title: '第 7 期双色球',
}

/** 每条出去的 qy 请求记一笔：路径 + 查询串。本文件的主要判据。 */
let sent: { params: Record<string, unknown>; url: string }[] = []

api.defaults.adapter = async (config) => {
  const url = String(config.url ?? '')
  const params = (config.params ?? {}) as Record<string, unknown>
  if (url.startsWith(QY_API_PREFIX)) sent.push({ params, url })
  let data: unknown = {}
  if (url.includes('/lottery/activities')) {
    const items = activityFor(
      params.draw_mode === 'wheel' ? 'wheel' : String(params.lane ?? '')
    )
    data = { items, p: 1, page_size: 12, total: items.length }
  } else if (url.includes('/lottery/my-entries')) {
    data = { items: [MY_ENTRY], p: 1, page_size: 20, total: 1 }
  } else if (url.includes('/lottery/my/prizes/')) {
    data = MY_PRIZE
  }
  return {
    config,
    data: { data, message: '', success: true },
    headers: {},
    status: 200,
    statusText: 'OK',
  }
}

/**
 * 列表请求带的 lane，按发出顺序。转盘那张不发 lane、发 `draw_mode=wheel`，
 * 记成 `'wheel'`；两者同给（后端 400）记成 `'lane+wheel'`，好让断言当场红。
 */
function hallCalls(): string[] {
  return sent
    .filter((row) => row.url.includes('/lottery/activities'))
    .map((row) => {
      const wheel = row.params.draw_mode === 'wheel'
      if (row.params.lane == null) return wheel ? 'wheel' : '(none)'
      return wheel ? 'lane+wheel' : String(row.params.lane)
    })
}

function calls(fragment: string): number {
  return sent.filter((row) => row.url.includes(fragment)).length
}

const cleanups: (() => Promise<void>)[] = []
after(async () => {
  for (const fn of cleanups) await fn()
})

type Mounted = {
  click: (label: string) => Promise<void>
  labels: () => string[]
  router: { state: { location: { hash: string } } }
  text: () => string
}

async function mountHub(
  plays: QyLotPlays,
  options?: { hash?: string }
): Promise<Mounted> {
  sent = []
  useAuthStore.setState((state) => ({
    ...state,
    auth: {
      ...state.auth,
      accessToken: 'probe',
      // 刻意不是管理员：大厅空态给管理员额外一颗「去创建活动」的按钮，
      // 那会把标签栏之外的按钮混进断言。
      user: { id: 7, role: ROLE.USER, status: 1, username: 'qy-probe-user' },
    },
  }))

  const queryClient = new QueryClient({
    defaultOptions: { queries: { gcTime: Infinity, retry: false } },
  })
  // 扩展配置直接塞进缓存：它是 `useQyConfig` 的数据源，不预置的话每次挂载都会
  // 多一条 `/config` 请求，而那条与"标签发了几个请求"无关。
  queryClient.setQueryData(qyKeys.config(), {
    ...QY_DISABLED_CONFIG,
    available: true,
    enabled: true,
    features: { ...QY_DISABLED_CONFIG.features, lottery: true },
    lottery: { plays, proof_public: true, show_entry: true },
  })

  const rootRoute = createRootRoute({ component: Outlet })
  const hubRoute = createRoute({
    component: QyLotteryHub,
    getParentRoute: () => rootRoute,
    path: '/qy/lottery',
  })
  const router = createRouter({
    history: createMemoryHistory({
      initialEntries: ['/qy/lottery' + (options?.hash ?? '')],
    }),
    routeTree: rootRoute.addChildren([hubRoute]),
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
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 60))
  })

  return {
    click: async (label) => {
      const target = [...container.querySelectorAll('button')].find(
        (node) => (node.textContent ?? '').trim() === label
      )
      assert.ok(target != null, '屏幕上找不到写着「' + label + '」的按钮')
      await act(async () => {
        target.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      })
      await act(async () => {
        await new Promise((resolve) => setTimeout(resolve, 60))
      })
    },
    /**
     * **外层**标签栏上的标题。
     *
     * 只取第一个 `role=tablist` 里的项：这一页有两级标签，每张大厅标签内部
     * 还有一条「进行中 / 已结束」分段栏，而它同样是 `role=tab`。全局扫
     * `[role="tab"]` 会把内层那两项一起算进来，于是"标签栏上有哪几张"这个
     * 断言在标签数正确时也会红 —— 而红的原因与被测的事无关。
     */
    labels: () => {
      const bar = container.querySelector('[role="tablist"]')
      if (bar == null) return []
      return [...bar.querySelectorAll('[role="tab"]')].map((node) =>
        (node.textContent ?? '').trim()
      )
    },
    router: router as unknown as Mounted['router'],
    text: () => container.textContent ?? '',
  }
}

describe('娱乐大厅的五张标签', () => {
  test('标签栏就是「抽奖-竞猜-双色球-星屑转盘-我的参与」，顺序逐字对应项目方那两句话', async () => {
    const hub = await mountHub(ALL_ON)
    assert.deepEqual(hub.labels(), [
      TAB_DRAW,
      TAB_GUESS,
      TAB_BALL,
      TAB_WHEEL,
      TAB_RECORDS,
    ])
  })

  test('一进页面只有第一张标签取数，另外四张一条请求都没发', async () => {
    const hub = await mountHub(ALL_ON)

    assert.deepEqual(
      hallCalls(),
      ['draw'],
      '一进页面就该只问「抽奖」那一夹；多一条就是有标签在后台偷偷取数'
    )
    assert.equal(
      calls('/lottery/my-entries'),
      0,
      '「我的参与」没被选中却在取数：keepMounted 把四张标签一起挂上了'
    )
    assert.equal(
      sent.length,
      1,
      '一进页面只该有一条 qy 请求，实际 ' + sent.length + ' 条'
    )

    const screen = hub.text()
    assert.ok(screen.includes('一场普通抽奖'), '抽奖那一夹的活动没渲染出来')
    assert.ok(
      !screen.includes('第 7 期双色球'),
      '双色球出现在了「抽奖」标签里 —— lane 没有真的分开'
    )
    assert.ok(!screen.includes('一场竞猜'), '竞猜出现在了「抽奖」标签里')
  })

  test('切到双色球：只发 lane=ball，屏幕只剩双色球那一场', async () => {
    const hub = await mountHub(ALL_ON)
    sent = []

    await hub.click(TAB_BALL)

    assert.deepEqual(
      hallCalls(),
      ['ball'],
      '切标签必须真的换一个 lane 出去；切走的那张不许重打'
    )
    assert.equal(
      hub.router.state.location.hash,
      qyTabHash('/qy/lottery-ball'),
      '切标签没写进地址栏：刷新会回到第一张，也没法把这一屏发给同事'
    )
    const screen = hub.text()
    assert.ok(screen.includes('第 7 期双色球'))
    assert.ok(
      !screen.includes('一场普通抽奖'),
      '「双色球」标签里混进了普通抽奖'
    )
  })

  test('切到竞猜：只发 lane=guess', async () => {
    const hub = await mountHub(ALL_ON)
    sent = []

    await hub.click(TAB_GUESS)

    assert.deepEqual(hallCalls(), ['guess'])
    assert.equal(hub.router.state.location.hash, qyTabHash('/qy/lottery-guess'))
    assert.ok(hub.text().includes('一场竞猜'))
  })

  test('切到转盘：只发 draw_mode=wheel、不带 lane，屏幕只剩那一场转盘', async () => {
    const hub = await mountHub(ALL_ON)
    sent = []

    await hub.click(TAB_WHEEL)

    assert.deepEqual(
      hallCalls(),
      ['wheel'],
      '转盘标签发的必须是 draw_mode=wheel 且不带 lane：两者同给是 400，只发 lane 会拿到普通抽奖'
    )
    // `/qy/wheel` 直达时重定向到的正是这个 hash（routes/_authenticated/qy/wheel）。
    assert.equal(hub.router.state.location.hash, qyTabHash('/qy/wheel'))
    const screen = hub.text()
    assert.ok(screen.includes('一场星屑转盘'), '转盘那一场没渲染出来')
    assert.ok(!screen.includes('一场普通抽奖'), '「转盘」标签里混进了普通抽奖')
    assert.ok(!screen.includes('第 7 期双色球'), '「转盘」标签里混进了双色球')
  })

  test('带 hash 打开（刷新 / 转发链接）直接落在那张标签上', async () => {
    const hub = await mountHub(ALL_ON, {
      hash: '#' + qyTabHash('/qy/lottery-ball'),
    })

    assert.deepEqual(
      hallCalls(),
      ['ball'],
      '带 hash 打开时仍然先问了第一张标签 —— 那是一条白打的请求，屏幕还会闪一下'
    )
    assert.ok(hub.text().includes('第 7 期双色球'))
  })

  test('每张大厅标签内部仍然分「进行中 / 已结束」', async () => {
    const hub = await mountHub(ALL_ON, {
      hash: '#' + qyTabHash('/qy/lottery-ball'),
    })
    sent = []

    await hub.click(zh['qy_lot_tab_done'])

    assert.deepEqual(
      sent.map((row) => [row.params.lane, row.params.phase]),
      [['ball', 'ended']],
      '标签内的分段没有带着当前这一夹一起发出去'
    )
  })
})

/**
 * 隐藏组合矩阵。
 *
 * 五个玩法开关 → 四张玩法标签。抽奖那张底下压着两种玩法，所以"抽奖底下两个
 * 玩法都关"必须单列一行：那正是"标签的可见性由它底下至少一个玩法可见决定"
 * 这条口径唯一会出错的地方。转盘并入之后多两行：只关转盘、只留转盘。
 */
const HIDDEN_CASES: {
  firstLane: string | null
  name: string
  plays: QyLotPlays
  tabs: string[]
}[] = [
  {
    firstLane: 'draw',
    name: '五个玩法全开：四张玩法标签都在',
    plays: ALL_ON,
    tabs: [TAB_DRAW, TAB_GUESS, TAB_BALL, TAB_WHEEL, TAB_RECORDS],
  },
  {
    firstLane: 'draw',
    name: '只关双色球：那张标签消失，另外三张不受影响',
    plays: { ...ALL_ON, draw_ball: false },
    tabs: [TAB_DRAW, TAB_GUESS, TAB_WHEEL, TAB_RECORDS],
  },
  {
    firstLane: 'draw',
    name: '只关竞猜',
    plays: { ...ALL_ON, guess: false },
    tabs: [TAB_DRAW, TAB_BALL, TAB_WHEEL, TAB_RECORDS],
  },
  {
    firstLane: 'draw',
    name: '只关转盘：那张标签消失，三张大厅与「我的参与」不受影响',
    plays: { ...ALL_ON, wheel: false },
    tabs: [TAB_DRAW, TAB_GUESS, TAB_BALL, TAB_RECORDS],
  },
  {
    firstLane: 'guess',
    name: '抽奖底下两个玩法都关：抽奖标签消失，双色球、竞猜、转盘还在',
    plays: { ...ALL_ON, draw_prob: false, draw_rank: false },
    tabs: [TAB_GUESS, TAB_BALL, TAB_WHEEL, TAB_RECORDS],
  },
  {
    firstLane: 'draw',
    name: '抽奖底下只关掉一种：那张标签仍然在',
    plays: { ...ALL_ON, draw_rank: false },
    tabs: [TAB_DRAW, TAB_GUESS, TAB_BALL, TAB_WHEEL, TAB_RECORDS],
  },
  {
    firstLane: 'ball',
    name: '只留双色球',
    plays: {
      draw_ball: true,
      draw_prob: false,
      draw_rank: false,
      guess: false,
      wheel: false,
    },
    tabs: [TAB_BALL, TAB_RECORDS],
  },
  {
    // 转盘是这个选择夹的一张标签，只留它时整页就是转盘 + 我的参与，
    // 而不是"没有大厅可看"。
    firstLane: 'wheel',
    name: '只留转盘',
    plays: {
      draw_ball: false,
      draw_prob: false,
      draw_rank: false,
      guess: false,
      wheel: true,
    },
    tabs: [TAB_WHEEL, TAB_RECORDS],
  },
  {
    firstLane: null,
    name: '五个全关：四张玩法标签都不渲染，只剩「我的参与」',
    plays: {
      draw_ball: false,
      draw_prob: false,
      draw_rank: false,
      guess: false,
      wheel: false,
    },
    tabs: [TAB_RECORDS],
  },
]

describe('每个入口单独隐藏', () => {
  for (const tc of HIDDEN_CASES) {
    test(tc.name, async () => {
      const hub = await mountHub(tc.plays)

      assert.deepEqual(
        hub.labels(),
        tc.tabs,
        '标签栏与玩法开关对不上：多一张 = 点进去是永远空的列表，少一张 = 开着的玩法没有入口'
      )
      assert.deepEqual(
        hallCalls(),
        tc.firstLane == null ? [] : [tc.firstLane],
        '首屏取数的不是第一张可见标签'
      )

      if (tc.firstLane == null) {
        assert.ok(
          hub.text().includes(zh['qy_lot_all_plays_hidden_note']),
          '一种玩法都不开时没有那句说明 —— 用户分不清"这一期没开"与"页面坏了"'
        )
      }
    })
  }
})

/**
 * 已参与的人在**任何**隐藏组合下都能查到自己那一票并领奖。
 *
 * 这是这一整条改动里唯一不能让步的一条：隐藏只影响大厅可见性与新报名。把
 * 「我的参与」也跟着按标签过滤掉，等于把已经收了钱的活动连同用户的凭据一起
 * 藏起来，而界面上不会有任何一处报错 —— 用户只会看到一个少了一张标签的页面。
 *
 * 用例覆盖到"五个玩法全关"这一档：那时四张玩法标签一张都不渲染，而这张必须在。
 * 那张票买的还是双色球（`draw_mode='ball'`），所以"双色球被关掉"这一档同时也
 * 在问：藏掉一个玩法会不会把那个玩法的历史票据一起藏走。
 */
describe('已参与的人不受任何隐藏组合影响', () => {
  for (const tc of HIDDEN_CASES) {
    test(tc.name + ' —— 仍然查得到自己那一票、领得走文本奖', async () => {
      const hub = await mountHub(tc.plays)

      assert.ok(
        hub.labels().includes(TAB_RECORDS),
        '「我的参与」被玩法开关连带藏掉了'
      )
      await hub.click(TAB_RECORDS)

      assert.equal(
        calls('/lottery/my-entries'),
        1,
        '「我的参与」点开了却没有取数'
      )
      const listed = hub.text()
      assert.ok(listed.includes('E-mine-1'), '我的那张票不在列表里')
      assert.ok(
        listed.includes('第 7 期双色球'),
        '票所属的活动标题没渲染 —— 双色球被隐藏时这一行也跟着消失了'
      )

      // 领奖：点开文本奖，兑换码必须真的出现在屏幕上。
      await hub.click(zh['qy_lot_text_view_btn'])
      assert.equal(
        calls('/lottery/my/prizes/'),
        1,
        '点了领奖按钮却没有去取奖品内容'
      )
      assert.ok(
        document.body.textContent?.includes(MY_PRIZE_SECRET),
        '兑换码没有出现在屏幕上 —— 藏掉入口把已经中了的奖一起藏走了'
      )
    })
  }
})
