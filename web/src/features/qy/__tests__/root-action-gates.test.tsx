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
 * 三处超级管理员闸门在**真实 DOM** 里的形状。
 *
 * # 守什么
 *
 * 后端把三个动作提到了超管（`middleware.RootActionRedemptionCreate` /
 * `RootActionLotteryResultSet` / `RootActionWithdrawPayeeReveal`），前端跟着
 * 各挂了一处 `role === ROLE.SUPER_ADMIN`。这三行有两种坏法，而它们在
 * typecheck 与源码 grep 上完全不可见：
 *
 *   1. **判据反了**（`===` 写成 `!==`）：role=10 看得见按钮、点了吃 403，
 *      role=100 反而被告知"你不能做"。实测把三处同时反转跑全量前端测试，
 *      1580 pass / 8 fail / exit 0，与基线逐字相同 —— 整套测试对它是瞎的。
 *   2. **只藏按钮、不给出口**：role=10 看到的是一个没有任何可做动作的页面，
 *      而"该去找谁"一个字都没有。三处的设计口径都是"按钮换成一句话"，
 *      不是 disabled、也不是直接抹掉。
 *
 * 所以每一格都断言两件事：该角色**能不能看到那个按钮**，以及**看不到时那句
 * 解释在不在**。只断言其一都杀不掉上面任何一种坏法。
 *
 * # 为什么三处合在一个文件里
 *
 * 与后端 `qianye/actor_gate_guard_test.go` 同一个理由：分散到三个 feature
 * 目录之后，没有任何一个地方能回答"本轮一共提了几个动作到超管、它们在界面上
 * 长什么样"。清单跨模块放一处，漏接一个才看得出来。
 *
 * # 语言包用真的
 *
 * `lng='zhCN'` + 真 locales + 真 qy bundle：这样"解释文案缺翻译"会当场显形
 * —— 键缺失时 i18next 回落成键名本身，而键名是一整句英文，断言
 * `text !== key` 就把它钉住了（兑换码那一句本轮就是这么漏掉的）。
 * 断言不打在具体译文上，改文案不会把这里弄红。
 */
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import { Window } from 'happy-dom'

import type { QyLotAdminActivityView } from '../pages/admin-lottery/types'
import type { QyAdminWithdrawal } from '../pages/withdraw/types'

const domWindow = new Window({ height: 900, width: 1280 })
const domGlobals = [
  'window',
  'document',
  'navigator',
  'localStorage',
  'sessionStorage',
  'HTMLElement',
  'HTMLInputElement',
  'HTMLTextAreaElement',
  'HTMLSelectElement',
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

// happy-dom 没有 scrollTo，而路由器每次导航都会调它 —— 缺了它导航会中途抛错，
// 整棵树掉进 CatchBoundary，看起来像是断言写错了。
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
const zhLocale = (await import('@/i18n/locales/zh.json')).default
const { registerQyResources } = await import('@/i18n/qy')

await i18next.use(initReactI18next).init({
  interpolation: { escapeValue: false },
  lng: 'zhCN',
  nsSeparator: false,
  resources: { zhCN: zhLocale },
})
registerQyResources(i18next)

const { api } = await import('@/lib/api')
const { useAuthStore } = await import('@/stores/auth-store')
const { ROLE } = await import('@/lib/roles')
const { RedemptionsProvider } =
  await import('@/features/redemption-codes/components/redemptions-provider')
const { RedemptionsPrimaryButtons } =
  await import('@/features/redemption-codes/components/redemptions-primary-buttons')
const { ReviewDialog } =
  await import('../pages/admin-withdrawals/components/review-dialog')
const { QyAdminLotteryDetail } = await import('../pages/admin-lottery/detail')
const { qyAdminLotActivityQuery } = await import('../pages/admin-lottery/api')
const { qyAdminWithdrawalQuery } =
  await import('../pages/admin-withdrawals/api')

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

/** 三处闸门各自那句解释的键。缺翻译时 i18next 会原样回落成它。 */
const REDEMPTION_HINT_KEY =
  'Only the super administrator can create redemption codes'
const LOTTERY_HINT_KEY = 'qy_lot_result_root_only'
const WITHDRAW_HINT_KEY = 'qy_wd_a_reveal_root_only'

/** 一次挂载之后从真实 DOM 上抄下来的快照。 */
type Snapshot = { buttons: string[]; text: string }

function setRole(role: number) {
  useAuthStore.setState((state) => ({
    ...state,
    auth: {
      ...state.auth,
      user: { id: 1, username: 'probe', role, status: 1 },
      accessToken: 'probe',
    },
  }))
}

/** 挂载一棵树，读完 DOM 立刻卸载。 */
async function mount(ui: React.ReactNode): Promise<Snapshot> {
  // 下面读的是整个 document.body（弹窗走 portal，不在 container 里），所以
  // 开工前必须先清干净：上一个用例的 portal 残留会让"role=10 看不到查看明文"
  // 这类断言读到**上一格**的按钮，表现为随机红/绿。
  document.body.innerHTML = ''
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(ui)
  })
  // 路由器与 react-query 在挂载之后还各会通知一轮，冲干净再读 DOM。
  await act(async () => {})

  // 弹窗走 portal，内容不在 container 里 —— 按钮与文案都从整个 document 上取。
  const scope = document.body
  const snapshot: Snapshot = {
    buttons: [...scope.querySelectorAll('button')].map((b) =>
      (b.textContent ?? '').trim()
    ),
    text: scope.textContent ?? '',
  }
  await act(async () => root.unmount())
  container.remove()
  return snapshot
}

/** 只回一份固定信封的假 adapter，按 URL 分派。 */
function useFakeApi(routes: Record<string, unknown>) {
  api.defaults.adapter = async (config) => {
    const url = config.url ?? ''
    const key = Object.keys(routes).find((k) => url.includes(k))
    return {
      config,
      data: {
        success: true,
        message: '',
        data: key == null ? null : routes[key],
      },
      headers: {},
      status: 200,
      statusText: 'OK',
    }
  }
}

function newQueryClient() {
  return new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
}

describe('兑换码创建（redemption.create）', () => {
  const render = () =>
    mount(
      <RedemptionsProvider>
        <RedemptionsPrimaryButtons />
      </RedemptionsProvider>
    )

  test('role=100 有「创建代码」按钮，没有那句解释', async () => {
    setRole(ROLE.SUPER_ADMIN)
    const view = await render()
    assert.equal(
      view.buttons.length,
      2,
      `超管应当同时有「删除无效」与「创建代码」：${view.buttons.join(' | ')}`
    )
    assert.ok(!view.text.includes(REDEMPTION_HINT_KEY))
  })

  test('role=10 没有创建按钮，但有一句说明为什么', async () => {
    setRole(ROLE.ADMIN)
    const view = await render()
    assert.deepEqual(
      view.buttons.map((label) => label),
      [zhLocale.translation['Delete Invalid']],
      '普通管理员这一页只该剩「删除无效」—— 多出来的按钮点了就是 403'
    )
    const hint = zhLocale.translation[REDEMPTION_HINT_KEY]
    assert.ok(hint != null && hint !== '', '这句解释在 zh 语言包里不存在')
    assert.ok(
      view.text.includes(hint),
      `没渲染出那句解释，role=10 只会看到一页没有任何出口的界面：${view.text}`
    )
    assert.notEqual(
      hint,
      REDEMPTION_HINT_KEY,
      '中文界面上渲染的是英文键名本身 —— 这个上游风格键没有补进 locales'
    )
  })
})

describe('提现收款人明文（withdraw.payee.reveal）', () => {
  const withdrawal = {
    id: 1,
    withdraw_no: 'WD-PROBE-1',
    method: 'fiat',
    status: 'pending',
    quota: 500000,
    currency: 'CNY',
    frozen_quota_per_unit: '500000',
    frozen_fx_rate: '7.2',
    gross_amount: '7.20',
    fee_amount: '0.20',
    net_amount: '7.00',
    fee_bps: 0,
    payee_channel: 'bank',
    payee_masked: '622*********1234',
    remark: '',
    has_proof: false,
    reviewed_at: 0,
    reject_reason: '',
    paid_at: 0,
    payout_ref: '',
    fail_reason: '',
    created_at: 1787000000,
    updated_at: 1787000000,
    events: [],
    user_id: 9,
    username: 'probe',
    risk_flags: '',
    reviewer_id: 0,
    reviewer_name: '',
    payout_operator_id: 0,
    payout_operator_name: '',
    payout_note: '',
    client_ip: '127.0.0.1',
    sla_deadline: 0,
    sla_breached: false,
    sla_kind: '',
    debt_blocked: false,
    unsettled_amount: '0',
  } as QyAdminWithdrawal

  const render = async () => {
    useFakeApi({ '/admin/withdraw/1': withdrawal })
    const queryClient = newQueryClient()
    // 预置缓存而不是等请求回来：首帧就带着单据渲染。靠"多冲几轮 act"等异步
    // 数据到位会随机少冲一轮，那时读到的是加载态，断言随机变红。
    queryClient.setQueryData(qyAdminWithdrawalQuery(1).queryKey, withdrawal)
    return mount(
      <QueryClientProvider client={queryClient}>
        <ReviewDialog withdrawalId={1} onClose={() => {}} onReveal={() => {}} />
      </QueryClientProvider>
    )
  }

  test('role=100 有「查看明文」按钮', async () => {
    setRole(ROLE.SUPER_ADMIN)
    const view = await render()
    assert.ok(
      view.text.includes('622*********1234'),
      '单据没渲染出来，下面的断言就不能算数'
    )
    assert.ok(
      view.buttons.some((label) => label === i18next.t('qy_wd_a_reveal')),
      `超管应当看到「查看明文」：${view.buttons.join(' | ')}`
    )
    assert.ok(!view.text.includes(i18next.t(WITHDRAW_HINT_KEY)))
  })

  test('role=10 没有「查看明文」，掩码与解释都还在', async () => {
    setRole(ROLE.ADMIN)
    const view = await render()
    assert.ok(
      !view.buttons.some((label) => label === i18next.t('qy_wd_a_reveal')),
      `普通管理员不该看到「查看明文」：${view.buttons.join(' | ')}`
    )
    assert.ok(
      view.text.includes('622*********1234'),
      '掩码必须原样留着 —— 藏掉的是明文，不是这一行'
    )
    const hint = i18next.t(WITHDRAW_HINT_KEY)
    assert.notEqual(hint, WITHDRAW_HINT_KEY, '这句解释在 qy 语言包里不存在')
    assert.ok(view.text.includes(hint), '没渲染出「打款这一步交给超管」那句话')
    // 四个人工决定不连坐：闸门只收窄看明文这一件事。
    assert.ok(
      view.buttons.some((label) => label === i18next.t('qy_common_approve')),
      `审核按钮被连坐藏掉了：${view.buttons.join(' | ')}`
    )
  })
})

describe('抽奖开奖结果录入（lottery.result.set）', () => {
  const actNo = 'LT-PROBE-0001'
  const view = {
    activity: {
      act_no: actNo,
      kind: 'guess',
      status: 'locked',
      outcome: '',
      title: '探针活动',
      intro: '',
      stake_quota: 1000,
      open_at: 1787000000,
      close_at: 1787000100,
      draw_at: 1787000200,
      settle_deadline: 1787000300,
      commit_hash: 'c'.repeat(64),
      rules_hash: 'r'.repeat(64),
      spec_hash: 's'.repeat(64),
      algo: 'sha256',
      rules_text: '',
      allow_multi_win: false,
      fee_bps: 0,
      min_entries_to_hold: 0,
      max_entries_per_user: 1,
      max_attempts_per_user: 1,
      max_total_entries: 0,
      max_total_users: 0,
      max_per_inviter: 0,
      cooldown_seconds: 0,
      dedup_ip: false,
      bet_min_quota: 0,
      bet_max_quota: 0,
      entry_seq: 0,
      active_count: 0,
      pending_count: 0,
      pool_quota: 0,
      platform_fee_quota: 0,
      payout_quota: 0,
      refund_quota: 0,
      roster_hash: '',
      roster_count: 0,
      chain_head: '',
      win_option_id: 0,
      result_evidence: '',
      result_by: 0,
      cancel_reason: '',
      created_by: 1,
      created_at: 1787000000,
      published_at: 1787000010,
      locked_at: 1787000100,
      revealed_at: 0,
      settled_at: 0,
      hidden_at: 0,
      hidden_by: 0,
      hidden_reason: '',
    },
    prizes: [],
    options: [],
    economics: {
      prize_total_quota: 0,
      break_even_entries: 0,
      income_quota: 0,
      payout_quota: 0,
      refund_quota: 0,
      platform_fee_quota: 0,
      held_quota: 0,
      net_quota: 0,
    },
  } as QyLotAdminActivityView

  const render = async () => {
    useFakeApi({ [`/admin/lottery/activities/${actNo}`]: view })
    const queryClient = newQueryClient()
    queryClient.setQueryData(qyAdminLotActivityQuery(actNo).queryKey, view)
    const rootRoute = createRootRoute({ component: Outlet })
    const authRoute = createRoute({
      getParentRoute: () => rootRoute,
      id: '_authenticated',
      component: Outlet,
    })
    const detailRoute = createRoute({
      getParentRoute: () => authRoute,
      path: '/qy/admin/lottery/$actNo/',
      component: QyAdminLotteryDetail,
    })
    const router = createRouter({
      routeTree: rootRoute.addChildren([authRoute.addChildren([detailRoute])]),
      history: createMemoryHistory({
        initialEntries: [`/qy/admin/lottery/${actNo}/`],
      }),
    })
    return mount(
      <QueryClientProvider client={queryClient}>
        {/* eslint-disable-next-line @typescript-eslint/no-explicit-any */}
        <RouterProvider router={router as any} />
      </QueryClientProvider>
    )
  }

  test('role=100 有「录入竞猜结果」按钮，没有那句解释', async () => {
    setRole(ROLE.SUPER_ADMIN)
    const snapshot = await render()
    assert.ok(
      snapshot.buttons.some(
        (label) => label === i18next.t('qy_lot_result_title')
      ),
      `超管应当看到录入按钮：${snapshot.buttons.join(' | ')}`
    )
    assert.ok(!snapshot.text.includes(i18next.t(LOTTERY_HINT_KEY)))
  })

  test('role=10 没有录入按钮，但被告知该找谁；其余动作不连坐', async () => {
    setRole(ROLE.ADMIN)
    const snapshot = await render()
    assert.ok(
      !snapshot.buttons.some(
        (label) => label === i18next.t('qy_lot_result_title')
      ),
      `普通管理员不该看到录入按钮 —— 点了就是 403：${snapshot.buttons.join(' | ')}`
    )
    const hint = i18next.t(LOTTERY_HINT_KEY)
    assert.notEqual(hint, LOTTERY_HINT_KEY, '这句解释在 qy 语言包里不存在')
    assert.ok(
      snapshot.text.includes(hint),
      '按钮消失了却没有任何解释：role=10 会看到一场没有任何可做动作的活动'
    )
    // 封盘态仍然可以取消、可以换封面 —— 闸门只收窄"录结果"这一件事。
    assert.ok(
      snapshot.buttons.some(
        (label) => label === i18next.t('qy_lot_cancel_title')
      ),
      `取消按钮被连坐藏掉了：${snapshot.buttons.join(' | ')}`
    )
  })
})
