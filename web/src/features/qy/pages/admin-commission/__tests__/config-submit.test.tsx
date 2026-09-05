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
 * 佣金配置页（D-15 恢复）的提交体与整屏文案。
 *
 * # 这里守的两件事
 *
 *  1. **提交体里没有任何法币键**：改一个字段、确认保存，PUT `/admin/commission/config`
 *     的 body 恰好只有那一个键，键名里不含 `fiat`。法币折算那一档连同它的"清空
 *     回落"按钮已整体删除，这条断言防的是有人把 HEAD 的 `fiat_rate_default`
 *     顺手抄回来。
 *  2. 整屏不出现「提现 / 打款 / 现金 / 元」：成熟期的说明此前写着"才可提现"，
 *     分组费率的口径说明里提到过法币表 —— 两处都换了键。
 *
 * 字段由后端下发的 `editable_keys` 驱动：这里给的清单含自动入账的两个新参数，
 * 它们必须真的画出来，否则"配置项从界面上消失、而它管的事情还在生效"。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import {
  QY_COMMISSION_FORBIDDEN_WORDS,
  ROLE,
  act,
  cleanupQyStardustScreens,
  commissionZh as zh,
  mountQyCommissionScreen,
  setQyProbeRole,
  type QyProbeRequest,
} from '../../../__tests__/commission-screen'
import { QyAdminCommission } from '../index'

after(cleanupQyStardustScreens)

const CONFIG = {
  effective: {
    topup_rate_percent: '10',
    consume_rate_percent: '5',
    redemption_rate_percent: '',
    redemption_rate_effective_percent: '10',
    redemption_rate_follows_topup: true,
    min_settle_stardust: 500_000,
    max_per_order_stardust: 0,
    holding_days: 7,
    min_credit_stardust: 500_000,
    credit_interval_seconds: 300,
    max_daily_stardust_per_inviter: 0,
    large_accrual_alert_stardust: 0,
    min_invitee_age_hours: 0,
  },
  overrides: {},
  editable_keys: [
    'topup_rate_percent',
    'consume_rate_percent',
    'redemption_rate_percent',
    'min_settle_stardust',
    'max_per_order_stardust',
    'holding_days',
    'min_credit_stardust',
    'credit_interval_seconds',
  ],
  percent_keys: [
    'topup_rate_percent',
    'consume_rate_percent',
    'redemption_rate_percent',
  ],
  nullable_percent_keys: ['redemption_rate_percent'],
  group_rates: [
    {
      group_name: 'vip',
      topup_rate_percent: '12',
      consume_rate_percent: '6',
      redemption_rate_percent: null,
      enabled: true,
      remark: '',
      operator_id: 1,
      updated_at: 1_787_000_000,
    },
  ],
  yaml_readonly: {
    enabled: true,
    topup_rate_percent: '10',
    consume_rate_percent: '5',
    redemption_rate_percent: '',
    exclude_redemption_and_manual: true,
    exclude_subscription_consume: false,
    refund_clawback: true,
    settle_interval_seconds: 300,
    day_offset_minutes: 480,
    topup_scan_interval_seconds: 60,
    topup_scan_lookback_hours: 24,
  },
}

function respond(request: QyProbeRequest) {
  if (request.url.endsWith('/admin/commission/config')) {
    if (request.method === 'PUT') return { data: {} }
    return { data: CONFIG }
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
          ran_today: true,
        },
        credit: {
          pending: 2,
          held: 0,
          held_quota: 0,
        },
      },
    }
  }
  return undefined
}

async function mountPage() {
  await setQyProbeRole(ROLE.SUPER_ADMIN)
  return mountQyCommissionScreen({
    element: <QyAdminCommission />,
    path: '/qy/admin/commission',
    features: { invite: true, commission: true },
    respond,
  })
}

/** 点**最后一个**匹配文案的按钮：确认弹窗走 portal 挂在 body 末尾，与卡片上那颗同名。 */
async function clickLast(label: string): Promise<boolean> {
  const nodes = [...document.body.querySelectorAll('button')].filter(
    (node) => (node.textContent ?? '').trim() === label
  )
  const node = nodes.at(-1)
  if (node == null) return false
  await act(async () => {
    node.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 60))
  })
  return true
}

describe('佣金配置页', () => {
  test('自动入账的两个参数画出来了，整屏没有法币与提现', async () => {
    const screen = await mountPage()
    const text = screen.text()
    assert.ok(text.includes(zh.qy_cm_f_min_credit), '自动入账门槛没画出来')
    assert.ok(text.includes(zh.qy_cm_f_credit_interval), '自动入账周期没画出来')
    assert.ok(
      text.includes(zh.qy_cm_credit_title),
      '结算调度卡上没有自动入账那一段'
    )
    assert.ok(!text.includes('qy_cm_'), `有 qy_cm_ 键没翻译：${text}`)
    for (const word of [...QY_COMMISSION_FORBIDDEN_WORDS, '法币']) {
      assert.ok(!text.includes(word), `出现了「${word}」：${text}`)
    }
  })

  test('改成熟期并确认：PUT 的 body 恰好一个键，键名里没有 fiat', async () => {
    const screen = await mountPage()

    assert.ok(
      await screen.type(zh.qy_cm_f_holding_days, '9'),
      '找不到成熟期输入框'
    )
    assert.ok(await screen.click(zh.qy_cm_save), '找不到「保存改动」')
    // 确认弹窗复述了改动：7 → 9。
    assert.ok(screen.text().includes(zh.qy_cm_confirm_title), '确认弹窗没打开')

    screen.sent.length = 0
    assert.ok(await clickLast(zh.qy_cm_save), '弹窗里找不到确认键')

    const puts = screen.sent.filter(
      (row) =>
        row.method === 'PUT' && row.url.endsWith('/admin/commission/config')
    )
    assert.equal(puts.length, 1, '应当恰好发一次 PUT')
    assert.deepEqual(puts[0].body, { holding_days: '9' })
    for (const key of Object.keys(puts[0].body as Record<string, unknown>)) {
      assert.ok(!key.includes('fiat'), `提交体里出现了法币键 ${key}`)
    }
  })
})
