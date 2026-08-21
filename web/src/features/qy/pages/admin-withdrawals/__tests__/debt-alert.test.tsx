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
 * 「收款人挂着冲正欠账」这件事，必须出现在审核人正在看的那张单上。
 *
 * # 守什么
 *
 * 冲正欠账只在【提交提现】那一刻被拦一次，而冲正按设计只吃 available、吃不到
 * 这笔申请已经冻住的 frozen。于是「先提现冻住 → 下线退款触发冲正 → 管理员照常
 * 审批放款」是一条完整的、无告警的通路；而 approve / mark-paid 正是这笔钱最后
 * 一次还能被拦回来的地方（驳回与标记发放失败都会把 frozen 退回可用池，退回后的
 * 那一桶正是下一次结算能吃到的）。
 *
 * 信号在系统里本来就存在（佣金余额页有 debt_blocked 徽标与筛选），只是不在
 * 审核人正在看的那一屏上。所以这里断言的是**真实 DOM 上有没有那句话**，而不是
 * 后端有没有下发字段（那条在 Go 侧的 review_response_fidelity_test.go）。
 *
 * 语言包用真的：键缺翻译时 i18next 回落成键名本身，`text !== key` 把它钉住。
 */
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import { Window } from 'happy-dom'

import type { QyAdminWithdrawal } from '../../withdraw/types'

const domWindow = new Window({ height: 900, width: 1280 })
for (const key of [
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
] as const) {
  const value = domWindow[key as keyof Window]
  if (value === undefined) continue
  Object.defineProperty(globalThis, key, { configurable: true, value })
}

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
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

const { useAuthStore } = await import('@/stores/auth-store')
const { ROLE } = await import('@/lib/roles')
const { ReviewDialog } = await import('../components/review-dialog')
const { qyAdminWithdrawalQuery } = await import('../api')
const { formatQyQuotaLedger } = await import('../../../lib/format')

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

const DEBT_TITLE_KEY = 'qy_wd_a_debt_title'

function withdrawalWith(debt: {
  debt_blocked: boolean
  unsettled_amount: string
}): QyAdminWithdrawal {
  return {
    id: 1,
    withdraw_no: 'WD-DEBT-1',
    method: 'quota',
    status: 'pending',
    quota: 8000000,
    currency: '',
    frozen_quota_per_unit: '500000',
    frozen_fx_rate: '1',
    gross_amount: '0',
    fee_amount: '0',
    net_amount: '0',
    fee_bps: 0,
    payee_channel: '',
    payee_masked: '',
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
    ...debt,
  } as QyAdminWithdrawal
}

async function renderReview(withdrawal: QyAdminWithdrawal): Promise<string> {
  useAuthStore.setState((state) => ({
    ...state,
    auth: {
      ...state.auth,
      user: { id: 1, username: 'probe', role: ROLE.ADMIN, status: 1 },
      accessToken: 'probe',
    },
  }))
  document.body.innerHTML = ''
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
  // 预置缓存而不是等请求回来：首帧就带着单据渲染，否则读到的是加载态。
  queryClient.setQueryData(qyAdminWithdrawalQuery(1).queryKey, withdrawal)
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(
      <QueryClientProvider client={queryClient}>
        <ReviewDialog withdrawalId={1} onClose={() => {}} onReveal={() => {}} />
      </QueryClientProvider>
    )
  })
  await act(async () => {})
  // 弹窗走 portal，内容不在 container 里。
  const text = document.body.textContent ?? ''
  await act(async () => root.unmount())
  container.remove()
  return text
}

describe('提现审核弹窗上的冲正欠账提示', () => {
  test('收款人挂着欠账时，审核人这一屏上必须看到那句话', async () => {
    const text = await renderReview(
      withdrawalWith({ debt_blocked: true, unsettled_amount: '-7000000' })
    )
    assert.ok(
      text.includes('WD-DEBT-1'),
      `单据没渲染出来，下面的断言就不能算数：${text}`
    )
    const title = i18next.t(DEBT_TITLE_KEY)
    assert.notEqual(title, DEBT_TITLE_KEY, '这句提示在 qy 语言包里不存在')
    assert.ok(
      text.includes(title),
      `挂着欠账却一个字都没提示，审核人只能照常点通过：${text}`
    )
    // 断言的是**格式化之后**的那个数：额度不许裸渲染（同目录的
    // lib/__tests__/commission-amount-display.test.ts 守的就是这一条），
    // 而这条用例真正要的是"差额给出了具体数字"，两者并不冲突。
    assert.ok(
      text.includes(formatQyQuotaLedger('-7000000')),
      '差额必须给出具体数字，否则运营无法判断这笔该不该发'
    )
  })

  test('没有欠账的单子不许平白多一条红色告警', async () => {
    const text = await renderReview(
      withdrawalWith({ debt_blocked: false, unsettled_amount: '0' })
    )
    assert.ok(text.includes('WD-DEBT-1'))
    assert.ok(
      !text.includes(i18next.t(DEBT_TITLE_KEY)),
      `绝大多数单子都没有欠账，天天见红等于没有提示：${text}`
    )
  })
})
