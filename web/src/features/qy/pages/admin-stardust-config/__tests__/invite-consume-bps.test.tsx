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
 * 星屑配置页上「下线消费返」这一格（D-14）：
 *
 *  1. 可写段按后端 `editable_keys` 长出 `invite_consume_bps` 一格，改它 → 保存 →
 *     PUT 请求体只含这一个键（只提交改动键的契约不因新键而破）；
 *  2. 分组比例表多出同名一列，`null` 印「沿用全站」、数字带百分数；新增一档时
 *     PUT 的请求体带 `invite_consume_bps`。
 *
 * 文案来自 `zh.json` + `pending-invite.zh.json`（见 `__tests__/invite-screen.ts`）。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import {
  ROLE,
  cleanupQyStardustScreens,
  inviteZh as zh,
  mountQyInviteScreen,
  setQyProbeRole,
  type QyProbeRequest,
} from '../../../__tests__/invite-screen'
import { QyAdminStardustConfig } from '../index'

after(cleanupQyStardustScreens)

const CONFIG = {
  effective: {
    name: '星屑',
    show_entry: 1,
    consume_bps: 10_000,
    invite_consume_bps: 0,
    invite_topup_bps: 0,
    invite_redeem_bps: 0,
    invite_register_stardust: 0,
    held_alert_days: 7,
  },
  overrides: {},
  editable_keys: [
    'name',
    'show_entry',
    'consume_bps',
    'invite_consume_bps',
    'invite_topup_bps',
    'invite_redeem_bps',
    'invite_register_stardust',
    'held_alert_days',
  ],
  bounds: {
    show_entry: { lo: 0, hi: 1 },
    consume_bps: { lo: 0, hi: 10_000_000 },
    invite_consume_bps: { lo: 0, hi: 10_000_000 },
    invite_topup_bps: { lo: 0, hi: 10_000_000 },
    invite_redeem_bps: { lo: 0, hi: 10_000_000 },
    invite_register_stardust: { lo: 0, hi: 8_796_093_022_208 },
    held_alert_days: { lo: 0, hi: 365 },
  },
  yaml_readonly: {
    quota_per_unit: 500_000,
    settle_delay_minutes: 30,
    settle_interval_seconds: 300,
    exclude_subscription_consume: true,
    exclude_manual_topup: true,
    max_manual_adjust: 100_000,
    compliance_confirmed: true,
  },
}

const GROUP_RATES = {
  items: [
    {
      user_group: 'vip',
      consume_bps: 12_000,
      invite_consume_bps: 500,
      invite_topup_bps: null,
      invite_redeem_bps: 0,
      enabled: true,
      updated_at: 1_787_000_000,
      operator_id: 1,
    },
  ],
  groups: ['default', 'vip'],
}

function respond(request: QyProbeRequest) {
  if (request.url.endsWith('/admin/stardust/config')) {
    if (request.method === 'PUT') {
      return { data: { effective: CONFIG.effective } }
    }
    return { data: CONFIG }
  }
  if (request.url.endsWith('/admin/stardust/group-rates')) {
    return { data: GROUP_RATES }
  }
  if (request.url.includes('/admin/stardust/group-rates/')) {
    return {
      data: {
        ...(request.body as object),
        user_group: 'default',
        updated_at: 1,
        operator_id: 1,
      },
    }
  }
  if (request.url.endsWith('/api/subscription/admin/plans')) {
    return { data: [] }
  }
  return undefined
}

async function mountConfig() {
  await setQyProbeRole(ROLE.SUPER_ADMIN)
  return mountQyInviteScreen({
    element: <QyAdminStardustConfig />,
    path: '/qy/admin/stardust-config',
    respond,
  })
}

describe('全局「下线消费返」', () => {
  test('改这一格 → 保存 → 确认 → PUT 请求体只含 invite_consume_bps', async () => {
    const screen = await mountConfig()
    const label = zh.qy_sdadm_cfg_k_invite_consume_bps
    assert.ok(screen.text().includes(label), '可写段没长出「下线消费返」这一格')

    assert.ok(await screen.type(label, '250'), '找不到下线消费返输入框')
    assert.ok(await screen.click(zh.qy_sdadm_cfg_save), '找不到保存键')
    assert.ok(
      screen.text().includes('0(0%) → 250(2.5%)'),
      `确认弹窗没有复述旧值 → 新值：${screen.text()}`
    )

    screen.sent.length = 0
    // 上游 ConfirmDialog 的确认键是 t('Continue')；测试语言包里没有上游键，原样渲染。
    assert.ok(await screen.click('Continue'), '找不到确认键')

    const puts = screen.sent.filter(
      (row) =>
        row.method === 'PUT' && row.url.endsWith('/admin/stardust/config')
    )
    assert.equal(puts.length, 1, '应当恰好发一次 PUT')
    assert.deepEqual(puts[0].body, { invite_consume_bps: 250 })
  })
})

describe('分组比例表的新列', () => {
  test('列上印出覆盖值；新增一档时 PUT 请求体带 invite_consume_bps', async () => {
    const screen = await mountConfig()
    const text = screen.text()
    assert.ok(
      text.includes(zh.qy_sdadm_cfg_k_invite_consume_bps),
      '分组比例表没有「下线消费返」这一列'
    )
    assert.ok(text.includes('500(5%)'), '覆盖的比例没带百分数')

    assert.ok(await screen.click(zh.qy_sdadm_gr_add), '找不到「新增」')
    assert.ok(
      screen.text().includes(zh.qy_sdadm_gr_dialog_desc),
      '分组比例弹窗没打开'
    )
    const field = document.querySelector<HTMLInputElement>(
      '[id$="-invite_consume_bps"]'
    )
    assert.ok(field != null, '弹窗里没有下线消费返那一格')
    // 弹窗里四个空格子的 placeholder 都是「沿用全站」，按 id 直接对这一格敲字
    //（React 19 的受控值只认原型 setter + input 事件）。
    const setter = Object.getOwnPropertyDescriptor(
      HTMLInputElement.prototype,
      'value'
    )?.set
    const { act } = await import('react')
    await act(async () => {
      setter?.call(field, '800')
      field.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await screen.settle()

    screen.sent.length = 0
    const saves = [...document.body.querySelectorAll('button')].filter(
      (node) =>
        (node.textContent ?? '').trim() === zh.qy_sdadm_cfg_save &&
        !node.disabled
    )
    // 弹窗里那一颗是最后挂上的。
    const save = saves.at(-1)
    assert.ok(save != null, '弹窗的保存键没有亮起来')
    await act(async () => {
      save.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await screen.settle()

    const puts = screen.sent.filter(
      (row) =>
        row.method === 'PUT' && row.url.includes('/admin/stardust/group-rates/')
    )
    assert.equal(puts.length, 1, '应当恰好发一次分组比例 PUT')
    assert.deepEqual(puts[0].body, {
      consume_bps: null,
      invite_consume_bps: 800,
      invite_topup_bps: null,
      invite_redeem_bps: null,
      enabled: true,
    })
  })
})
