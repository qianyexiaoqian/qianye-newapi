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
 * 「结算台」三合一（日消费明细 / 佣金审核 / 佣金用户）在**真实渲染**上的形状（D-15）。
 *
 * # 这里守的三件事
 *
 *  1. **不可见的标签一个请求都不发**：一进页面只有日消费明细那一条（主库大表
 *     聚合）；切到佣金审核才打计佣流水 + 结算调度两条；切到佣金用户才打用户列表。
 *     只要有人给 `QyPageTabs` 加上 `keepMounted`，一进页面就是三份查询。
 *  2. 三张标签的标题都在标签栏上，切标签把 hash 写进地址栏。
 *  3. 三张标签的屏幕上一个字都不许出现「提现 / 打款 / 现金 / 元」——
 *     第三张此前叫「提现审核」，它连同提现模块永久删除。
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
import { QyAdminSettlementHub } from '../hub'

after(cleanupQyStardustScreens)

const EMPTY_PAGE = { items: [], total: 0, p: 1, page_size: 20 }

const ACCRUALS = [
  {
    id: 1,
    accrual_no: 'CA-9001',
    idem_scope: 'consume',
    idem_key: 'k1',
    inviter_id: 7,
    invitee_id: 42,
    source_type: 'consume',
    source_ref: 'L-1',
    base_quota: 3_700_000,
    base_money: '7.4000000000',
    rate_bps: 500,
    rate_group: 'vip',
    gross_amount: '185000.0000000000',
    settled_amount: '185000.0000000000',
    usd_rate: '1',
    status: 'settled',
    risk_flags: '',
    mature_at: 1_787_200_000,
    bucket_date: '20260903',
    remark: '',
    created_at: 1_787_000_000,
    relation_blocked: false,
  },
]

const USERS = [
  {
    user_id: 7,
    username: 'bob',
    user_resolved: true,
    available_quota: 2_500_000,
    frozen_quota: 0,
    credited_quota: 10_000_000,
    total_earned_quota: 12_500_000,
    total_clawback_quota: 0,
    derived_available_quota: 2_500_000,
    ledger_drift: 0,
    unsettled_amount: '0.0000000000',
    debt_blocked: false,
    invitee_count: 3,
    last_settled_at: 1_787_000_000,
    updated_at: 1_787_000_000,
    display_name: '',
    email: 'bob@example.com',
    user_group: 'vip',
    inviter_id: 0,
    inviter_username: '',
    inviter_resolved: false,
    inviter_blocked: false,
    inviter_commission_quota: 0,
    blocked_invitee_count: 1,
    has_balance_row: true,
  },
]

function respond(request: QyProbeRequest) {
  if (request.url.endsWith('/admin/invite/daily-consume')) {
    return {
      data: {
        ...EMPTY_PAGE,
        index_ready: true,
        accrual_users_without_logs: 0,
        range: {
          start_date: '20260903',
          end_date: '20260903',
          days: 1,
          max_days: 31,
        },
        summary: {
          user_count: 0,
          request_count: 0,
          consume_quota: 0,
          invite_base_quota: 0,
          uncounted_quota: 0,
        },
      },
    }
  }
  if (request.url.endsWith('/admin/commission/records')) {
    return { data: { ...EMPTY_PAGE, items: ACCRUALS, total: ACCRUALS.length } }
  }
  if (request.url.endsWith('/admin/commission/health')) {
    return {
      data: {
        daily_settle: {
          today: '20260905',
          day_offset_minutes: 480,
          next_run_after: 1_787_328_000,
          max_attempts: 5,
          payout_day_offset: 8,
          ran_today: false,
        },
      },
    }
  }
  if (request.url.endsWith('/admin/commission/users')) {
    return {
      data: {
        ...EMPTY_PAGE,
        items: USERS,
        total: USERS.length,
        totals: {
          user_count: 1,
          available_quota: 2_500_000,
          credited_quota: 10_000_000,
          invitee_count: 3,
        },
      },
    }
  }
  return undefined
}

async function mountHub(hash?: string) {
  await setQyProbeRole(ROLE.SUPER_ADMIN)
  return mountQyCommissionScreen({
    element: <QyAdminSettlementHub />,
    path: '/qy/admin/settlement',
    initial:
      hash == null ? '/qy/admin/settlement' : `/qy/admin/settlement#${hash}`,
    features: { invite: true, commission: true },
    respond,
  })
}

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

describe('结算台的三张标签', () => {
  test('一进页面只有第一张标签在取数；三张标签的标题都在标签栏上', async () => {
    const screen = await mountHub()

    assert.deepEqual(
      qyUrls(screen.sent),
      ['/api/qy/admin/invite/daily-consume'],
      '佣金审核 / 佣金用户在后台偷偷取了数：一进页面就是三份查询'
    )
    for (const key of [
      'qy_dc_title',
      'qy_nav_a_commission_records',
      'qy_nav_a_commission_users',
    ]) {
      assert.ok(
        [...screen.container.querySelectorAll('button')].some(
          (node) => node.textContent?.trim() === zh[key]
        ),
        `标签栏上少了「${zh[key]}」`
      )
    }
    assertNoForbiddenWords(screen.text())
  })

  test('切到佣金审核：hash 跟着变，只打计佣流水 + 结算调度两条', async () => {
    const screen = await mountHub()
    screen.sent.length = 0

    assert.ok(
      await screen.click(zh.qy_nav_a_commission_records),
      '标签栏上没有「佣金审核」'
    )

    assert.equal(
      screen.router.state.location.hash,
      qyTabHash('/qy/admin/commission-records')
    )
    assert.deepEqual(qyUrls(screen.sent), [
      '/api/qy/admin/commission/health',
      '/api/qy/admin/commission/records',
    ])
    const text = screen.text()
    assert.ok(text.includes('#42'), '计佣行的下线没渲染')
    assert.ok(text.includes('$0.37'), '佣金金额没按额度口径印')
    // 撤掉「立即结算」之后，什么时候到账要写在同一屏上；到星辉那一跳也要说清。
    assert.ok(text.includes('T+8'), '自动结算的 T+N 没渲染')
    assert.ok(
      text.includes(zh.qy_cm_auto_credit_note),
      '没有说明已结算的钱怎么进星辉'
    )
    assertNoForbiddenWords(text)
  })

  test('切到佣金用户：只打用户列表，已入账那一列在、旧的那一列不在', async () => {
    const screen = await mountHub()
    screen.sent.length = 0

    assert.ok(
      await screen.click(zh.qy_nav_a_commission_users),
      '标签栏上没有「佣金用户」'
    )

    assert.equal(
      screen.router.state.location.hash,
      qyTabHash('/qy/admin/commission-users')
    )
    assert.deepEqual(qyUrls(screen.sent), ['/api/qy/admin/commission/users'])
    const text = screen.text()
    assert.ok(text.includes('bob'), '用户没渲染')
    assert.ok(text.includes(zh.qy_cb_credited), '「已入账」列不见了')
    assert.ok(text.includes('$20'), '已入账金额没按额度口径印')
    assertNoForbiddenWords(text)
  })

  test('带 hash 打开佣金用户时不先问第一张标签', async () => {
    const screen = await mountHub(qyTabHash('/qy/admin/commission-users'))
    assert.deepEqual(qyUrls(screen.sent), ['/api/qy/admin/commission/users'])
  })
})
