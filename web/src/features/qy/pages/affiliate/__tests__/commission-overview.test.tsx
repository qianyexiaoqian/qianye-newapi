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
 * 「我的推广」在**佣金开着**时的形状（D-15）。
 *
 * # 这里守的三件事
 *
 *  1. 概览一屏同时有**星辉三格**（待结算 / 可用（待入账）/ 已入账）与**星屑五种
 *     来源的分布**：两条线并行是项目方追问后的拍板，少一半就是 D-14 或 D-13 的
 *     形状回来了。三格按额度口径印（`$…`），分布按星屑口径印（`… 星屑`）。
 *  2. 「佣金明细」标签：逐笔那一张打 `/commission/records`，按来源筛选把
 *     `source_type` 带进请求并回到第一页；入账记录那一张打 `/commission/credits`。
 *  3. 整屏（概览 + 佣金明细两张标签）不出现「提现 / 打款 / 现金 / 元」。
 *
 * 文案来自 `zh.json` + `pending-commission.zh.json`（见 `__tests__/commission-screen.ts`）。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import {
  QY_COMMISSION_FORBIDDEN_WORDS,
  ROLE,
  cleanupQyStardustScreens,
  commissionZh as zh,
  mountQyCommissionScreen,
  setQyProbeRole,
  type QyProbeRequest,
} from '../../../__tests__/commission-screen'
import { qyTabHash } from '../../../lib/pages'
import { QyInviteHub } from '../hub'

after(cleanupQyStardustScreens)

const INVITE_SUMMARY = {
  invitee_count: 3,
  blocked_count: 0,
  totals: {
    invite_consume: 600,
    invite_topup: 300,
    invite_redeem: 100,
    invite_register: 200,
    plan_inviter: 34,
    all: 1234,
  },
  yesterday: { base_quota: 3_700_000, granted: 7, held: 2 },
  pending_today_base_quota: 1_000_000,
  rate: {
    group: 'vip',
    invite_consume_bps: 500,
    invite_topup_bps: 1000,
    invite_redeem_bps: 0,
    invite_register_stardust: 50,
  },
  compliance_confirmed: true,
  day_offset_minutes: 480,
}

const COMMISSION_SUMMARY = {
  invitee_count: 3,
  available_quota: 2_500_000,
  frozen_quota: 0,
  credited_quota: 10_000_000,
  total_earned_quota: 13_000_000,
  total_clawback_quota: 0,
  unsettled_amount: '0.4700000000',
  pending_mature_quota: '500000.0000000000',
  debt_blocked: false,
  last_settled_at: 1_787_000_000,
  next_credit_at: 1_787_100_000,
  min_credit_quota: 500_000,
  rate: {
    topup_bps: 1000,
    consume_bps: 500,
    redemption_bps: 1000,
    redemption_follows_topup: true,
    group: 'vip',
    group_matched: true,
    global_topup_percent: '5',
    global_consume_percent: '3',
    global_redemption_percent: '',
  },
  pending_earliest_mature_at: 1_787_200_000,
  policy: {
    holding_days: 7,
    min_settle_quota: 500_000,
    settle_interval_seconds: 300,
    settle_daily: true,
    payout_day_offset: 8,
    day_offset_minutes: 480,
    exclude_redemption: true,
    exclude_subscription: false,
  },
}

const RECORDS = [
  {
    accrual_no: 'CA-1001',
    source_type: 'consume',
    source_ref: '***9',
    invitee_ref: 'u-42',
    invitee_masked_name: 'zh***ng',
    base_quota: 3_700_000,
    rate_bps: 500,
    gross_amount: '185000.0000000000',
    settled_amount: '185000.0000000000',
    status: 'settled',
    mature_at: 1_787_200_000,
    bucket_date: '20260903',
    created_at: 1_787_000_000,
  },
  {
    accrual_no: 'CA-1002',
    source_type: 'topup',
    source_ref: '***7',
    invitee_ref: 'u-43',
    invitee_masked_name: 'li***i',
    base_quota: 5_000_000,
    rate_bps: 1000,
    gross_amount: '500000.0000000000',
    settled_amount: '0.0000000000',
    status: 'accrued',
    mature_at: 1_787_300_000,
    bucket_date: '20260904',
    created_at: 1_787_000_100,
  },
]

const CREDITS = [
  {
    credit_no: 'CC-2001',
    quota: 5_000_000,
    fund_order_no: 'CC20260904001',
    status: 'done',
    created_at: 1_787_000_000,
    finished_at: 1_787_000_050,
    remark: '',
  },
  {
    credit_no: 'CC-2002',
    quota: 5_000_000,
    fund_order_no: 'CC20260905001',
    status: 'held',
    created_at: 1_787_090_000,
    finished_at: 0,
    remark: '',
  },
]

function respond(request: QyProbeRequest) {
  if (request.url.endsWith('/invite/summary')) return { data: INVITE_SUMMARY }
  if (request.url.endsWith('/commission/summary')) {
    return { data: COMMISSION_SUMMARY }
  }
  if (request.url.endsWith('/commission/records')) {
    const items =
      request.params.source_type == null
        ? RECORDS
        : RECORDS.filter(
            (row) => row.source_type === request.params.source_type
          )
    return { data: { items, total: items.length, p: 1, page_size: 20 } }
  }
  if (request.url.endsWith('/commission/credits')) {
    return {
      data: { items: CREDITS, total: CREDITS.length, p: 1, page_size: 20 },
    }
  }
  // 上游那张「推荐计划」卡读的三条主库接口：给最小合法数据。
  if (request.url.endsWith('/api/user/aff')) return { data: 'ABC123' }
  if (request.url.endsWith('/api/user/self')) {
    return { data: { id: 1, aff_quota: 0, aff_history_quota: 0, aff_count: 0 } }
  }
  if (request.url.includes('/api/user/topup/info')) {
    return {
      data: {
        pay_methods: [],
        amount_options: [],
        discount: {},
        creem_products: [],
        stripe_min_topup: 0,
        payment_compliance_confirmed: true,
      },
    }
  }
  return undefined
}

async function mountHub(hash?: string) {
  await setQyProbeRole(ROLE.USER)
  return mountQyCommissionScreen({
    element: <QyInviteHub />,
    path: '/qy/affiliate',
    initial: hash == null ? '/qy/affiliate' : `/qy/affiliate#${hash}`,
    features: { invite: true, commission: true },
    respond,
  })
}

/** 只数 qy 的请求路径（上游主库接口不算）。 */
function qyUrls(sent: QyProbeRequest[]): string[] {
  return sent
    .filter((row) => row.url.startsWith('/api/qy/'))
    .map((row) => row.url)
    .sort()
}

function assertNoForbiddenWords(text: string) {
  for (const word of QY_COMMISSION_FORBIDDEN_WORDS) {
    assert.ok(!text.includes(word), `出现了「${word}」：${text}`)
  }
}

describe('概览：星辉三格与星屑分布同屏', () => {
  test('一进页面打两条 summary；三格与五种 kind 的分布都在，各印各的单位', async () => {
    const screen = await mountHub()

    assert.deepEqual(qyUrls(screen.sent), [
      '/api/qy/commission/summary',
      '/api/qy/invite/summary',
    ])

    const text = screen.text()
    // 星辉三格：标签与额度口径的数字。
    for (const key of [
      'qy_aff_pending_settle',
      'qy_aff_available_credit',
      'qy_aff_credited',
    ]) {
      assert.ok(text.includes(zh[key]), `缺三格之一「${zh[key]}」`)
    }
    // 可用 2,500,000 额度 = $5；已入账 10,000,000 = $20。绝不能印成星屑。
    assert.ok(text.includes('$5'), `可用没按额度口径印：${text}`)
    assert.ok(text.includes('$20'), `已入账没按额度口径印：${text}`)
    assert.ok(!text.includes('2,500,000 星屑'), '可用被当成星屑渲染了')
    // 下次入账那句话带着门槛（500,000 额度 = $1）。
    assert.ok(
      text.includes(zh.qy_aff_next_credit_value.slice(0, 5)),
      '下次入账那句话没渲染'
    )

    // 星屑那半：五种 kind 各占一格。
    const segments = [
      ...document.body.querySelectorAll<HTMLElement>('[data-segment]'),
    ]
    assert.deepEqual(
      segments.map((node) => node.dataset.segment),
      [
        'invite_consume',
        'invite_topup',
        'invite_redeem',
        'invite_register',
        'plan_inviter',
      ],
      '星屑分布的五格不见了 —— 上半有了佣金，下半的星屑不能少'
    )
    assert.ok(text.includes('1,234 星屑'), '星屑总数没带单位名渲染')

    // 两个区段的顺序：星辉在上、星屑在下。
    const sections = [
      ...document.body.querySelectorAll<HTMLElement>('[data-section]'),
    ].map((node) => node.dataset.section)
    assert.deepEqual(sections, ['commission', 'stardust'])

    assert.ok(!text.includes('qy_aff_'), `有 qy_aff_ 键没翻译：${text}`)
    assertNoForbiddenWords(text)
  })

  test('佣金关掉时只剩星屑那半，佣金接口一次都不打', async () => {
    await setQyProbeRole(ROLE.USER)
    const screen = await mountQyCommissionScreen({
      element: <QyInviteHub />,
      path: '/qy/affiliate',
      features: { invite: true, commission: false },
      respond,
    })
    assert.deepEqual(qyUrls(screen.sent), ['/api/qy/invite/summary'])
    assert.ok(!screen.text().includes(zh.qy_aff_xh_title))
    assert.ok(
      !screen.text().includes(zh.qy_nav_commission_records),
      '佣金关掉了，佣金明细那张标签还在标签栏上'
    )
  })
})

describe('佣金明细标签', () => {
  test('切到佣金明细：只发 /commission/records，hash 写进地址栏，金额按额度口径印', async () => {
    const screen = await mountHub()
    screen.sent.length = 0

    assert.ok(
      await screen.click(zh.qy_nav_commission_records),
      '标签栏上没有「佣金明细」'
    )

    assert.deepEqual(qyUrls(screen.sent), ['/api/qy/commission/records'])
    assert.equal(
      screen.router.state.location.hash,
      qyTabHash('/qy/commission-records')
    )
    const asked = screen.sent.find((row) =>
      row.url.endsWith('/commission/records')
    )
    assert.ok(asked != null)
    assert.equal(asked.params.p, 1)
    assert.equal(asked.params.page_size, 20)
    assert.equal(asked.params.source_type, undefined, '不筛来源时不该发空串')

    const text = screen.text()
    assert.ok(text.includes('zh***ng'), '脱敏下线名没渲染')
    assert.ok(text.includes('li***i'), '第二行没渲染')
    // gross 185,000 额度 = $0.37。
    assert.ok(text.includes('$0.37'), `佣金金额没按额度口径印：${text}`)
    assertNoForbiddenWords(text)
  })

  test('按来源筛选把 source_type 带进请求并回到第一页', async () => {
    const screen = await mountHub(qyTabHash('/qy/commission-records'))
    screen.sent.length = 0

    assert.ok(await screen.select(zh.qy_aff_source, 'topup'), '找不到来源下拉')

    const asked = screen.sent.filter((row) =>
      row.url.endsWith('/commission/records')
    )
    assert.equal(asked.length, 1)
    assert.equal(asked[0].params.source_type, 'topup')
    assert.equal(asked[0].params.p, 1, '换筛选必须回到第一页')
    const text = screen.text()
    assert.ok(text.includes('li***i'))
    assert.ok(!text.includes('zh***ng'), '筛掉的那一种还在列表里')
  })

  test('入账记录次级标签：打 /commission/credits，held 与 done 分成两种说法', async () => {
    const screen = await mountHub(qyTabHash('/qy/commission-records'))
    screen.sent.length = 0

    assert.ok(await screen.click(zh.qy_aff_tab_credits), '找不到「入账记录」')

    assert.deepEqual(qyUrls(screen.sent), ['/api/qy/commission/credits'])
    const asked = screen.sent[0]
    assert.equal(asked.params.p, 1)
    assert.equal(asked.params.page_size, 20)

    const text = screen.text()
    assert.ok(text.includes('CC-2001'))
    assert.ok(text.includes(zh.qy_aff_credit_st_done), 'done 没翻译')
    assert.ok(text.includes(zh.qy_aff_credit_st_held), 'held 没翻译')
    assert.ok(text.includes('+$10'), '入账金额没带 + 号按额度口径印')
    assertNoForbiddenWords(text)
  })
})
