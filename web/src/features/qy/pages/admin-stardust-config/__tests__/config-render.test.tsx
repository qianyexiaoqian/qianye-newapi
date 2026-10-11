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
 * 星屑配置页在**真实渲染**上的四件事：
 *
 *  1. 上线返利不在这一页：注册奖搬去了佣金配置，这一页只留一句指路；消费返照常可写。
 *  2. 只 PUT 改动键：改一格 → 保存 → 确认弹窗复述 `旧 → 新` → 确认 → 请求体里
 *     只有那一个键。全量提交会污染审计。
 *  3. 分组比例表：`null` 印成「沿用全站」而不是 0，数字带百分数；只能选用户分组。
 *  4. 套餐返还：选套餐 → 取那一份定义 → 勾一个来源 → 保存的 sources 按闭集顺序。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import {
  ROLE,
  act,
  cleanupQyStardustScreens,
  mountQyStardustScreen,
  setQyProbeRole,
  zh,
  type QyProbeRequest,
} from '../../../__tests__/stardust-screen'
import { QyAdminStardustConfig } from '../index'

after(cleanupQyStardustScreens)

const CONFIG = {
  effective: {
    name: '星屑',
    show_entry: 1,
    consume_bps: 10_000,
    invite_register_stardust: 0,
    held_alert_days: 7,
  },
  overrides: { consume_bps: '10000' },
  editable_keys: [
    'name',
    'show_entry',
    'consume_bps',
    'invite_register_stardust',
    'held_alert_days',
  ],
  bounds: {
    show_entry: { lo: 0, hi: 1 },
    consume_bps: { lo: 0, hi: 10_000_000 },
    invite_register_stardust: { lo: 0, hi: 8_796_093_022_208 },
    held_alert_days: { lo: 0, hi: 365 },
  },
  yaml_readonly: {
    quota_per_unit: 500_000,
    settle_delay_minutes: 30,
    settle_interval_seconds: 300,
    exclude_subscription_consume: true,
    max_manual_adjust: 100_000,
    compliance_confirmed: false,
  },
}

const GROUP_RATES = {
  items: [
    {
      user_group: 'vip',
      consume_bps: 12_000,
      enabled: true,
      updated_at: 1_787_000_000,
      operator_id: 1,
    },
    {
      user_group: 'svip',
      consume_bps: null,
      enabled: true,
      updated_at: 1_787_000_000,
      operator_id: 1,
    },
    // 以前从混着模型分组的下拉里误配的一行:后端以 stale_groups 标出。
    {
      user_group: '反重力的哈基米',
      consume_bps: 0,
      enabled: true,
      updated_at: 1_787_000_000,
      operator_id: 1,
    },
  ],
  // 只有用户分组(登记表 ∪ 在用的 users.group),没有模型分组。
  groups: ['default', 'svip', 'vip', '浅梦'],
  stale_groups: ['反重力的哈基米'],
}

const PLANS = [
  { plan: { id: 3, title: '月卡', enabled: true } },
  { plan: { id: 4, title: '季卡', enabled: false } },
]

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
  if (request.url.endsWith('/api/subscription/admin/plans')) {
    return { data: PLANS }
  }
  if (request.url.includes('/admin/stardust/plan-rewards/3')) {
    if (request.method === 'PUT') {
      return { data: { ...(request.body as object), plan_id: 3, exists: true } }
    }
    return {
      data: {
        plan_id: 3,
        buyer_bps: 10_000,
        sources: ['order', 'balance'],
        exists: false,
      },
    }
  }
  return undefined
}

async function mountConfig() {
  await setQyProbeRole(ROLE.SUPER_ADMIN)
  return mountQyStardustScreen({
    element: <QyAdminStardustConfig />,
    path: '/qy/admin/stardust-config',
    respond,
  })
}

/** 按 label 文案找到那一格输入框。 */
function inputByLabel(label: string): HTMLInputElement | null {
  const node = [...document.body.querySelectorAll('label')].find(
    (item) => (item.textContent ?? '').trim() === label
  )
  const id = node?.getAttribute('for')
  if (id == null) return null
  // useId 生成的 id 带冒号，`#id` 选择器会解析失败，按属性匹配。
  const field = document.querySelector(`[id="${id}"]`)
  return field instanceof HTMLInputElement ? field : null
}

describe('上线返利搬去佣金配置', () => {
  test('注册奖不在这一页渲染，指向佣金配置；消费返照常可写', async () => {
    const screen = await mountConfig()
    const text = screen.text()

    assert.equal(
      inputByLabel(zh.qy_sdadm_cfg_k_invite_register_stardust),
      null,
      '注册奖是给上线的返利，已搬去佣金配置'
    )
    const consume = inputByLabel(zh.qy_sdadm_cfg_k_consume_bps)
    assert.ok(consume != null)
    assert.equal(consume.disabled, false, '消费返不受合规门约束')
    // 被运营覆盖过的键要说出来。
    assert.ok(text.includes(zh.qy_sdadm_cfg_overridden), '覆盖提示没渲染')
    // 一切给上线的返利只在佣金配置：这一页要指过去，而不是自己再给一套比例。
    assert.ok(
      text.includes(zh.qy_sdadm_cfg_invite_moved),
      '缺「上线返利在佣金配置里」的说明'
    )
  })
})

describe('只提交改动键', () => {
  test('改消费返 → 保存 → 确认弹窗复述旧→新 → PUT 请求体只含 consume_bps', async () => {
    const screen = await mountConfig()

    assert.ok(
      await screen.type(zh.qy_sdadm_cfg_k_consume_bps, '12000'),
      '找不到消费返输入框'
    )
    assert.ok(await screen.click(zh.qy_sdadm_cfg_save), '找不到保存键')

    const text = screen.text()
    assert.ok(text.includes(zh.qy_sdadm_cfg_confirm_title), '确认弹窗没打开')
    assert.ok(
      text.includes('10000(100%) → 12000(120%)'),
      `确认弹窗没有复述旧值 → 新值：${text}`
    )

    screen.sent.length = 0
    // 上游 ConfirmDialog 的确认键是 t('Continue')；测试语言包里没有上游键，原样渲染。
    assert.ok(await screen.click('Continue'), '找不到确认键')

    const puts = screen.sent.filter(
      (row) =>
        row.method === 'PUT' && row.url.endsWith('/admin/stardust/config')
    )
    assert.equal(puts.length, 1, '应当恰好发一次 PUT')
    assert.deepEqual(puts[0].body, { consume_bps: 12_000 })
  })

  test('什么都没改时保存键禁用', async () => {
    await mountConfig()
    const save = [...document.body.querySelectorAll('button')].find(
      (node) => (node.textContent ?? '').trim() === zh.qy_sdadm_cfg_save
    )
    assert.ok(save != null)
    assert.equal(save.disabled, true)
  })
})

describe('分组比例', () => {
  test('null 印成「沿用全站」，数字带百分数，误配在模型分组上的行标红', async () => {
    const screen = await mountConfig()
    const text = screen.text()
    assert.ok(text.includes('vip'), '分组名没渲染')
    assert.ok(text.includes('12000(120%)'), '覆盖的比例没带百分数')
    assert.ok(
      text.includes(zh.qy_sdadm_gr_inherit),
      'null 没有印成「沿用全站」'
    )
    assert.ok(text.includes('0(0%)'), '显式的 0 必须与 null 分开印')
    assert.ok(
      text.includes(zh.qy_sdadm_gr_stale),
      '挂在非用户分组上的行没有标出来'
    )
  })

  test('新增一档时下拉只给还没配过的用户分组，没有模型分组', async () => {
    const screen = await mountConfig()
    assert.ok(await screen.click(zh.qy_sdadm_gr_add), '找不到新增按钮')
    // 页面上还有套餐返还的下拉：只看新增弹窗里的那一个。
    const select = document.body.querySelector('[role="dialog"] select')
    assert.ok(select != null, '新增弹窗里没有分组下拉')
    const options = [...select.querySelectorAll('option')].map(
      (node) => node.value
    )
    assert.deepEqual(
      options,
      ['default', '浅梦'],
      'vip / svip 已配过，模型分组不在清单里'
    )
    assert.ok(
      screen.text().includes(zh.qy_sdadm_gr_group_hint),
      '缺「只列用户分组」的说明'
    )
  })
})

describe('套餐返还', () => {
  test('从清单里选套餐 → 取那一份定义 → 勾上 admin → PUT 的 sources 按闭集顺序', async () => {
    const screen = await mountConfig()

    assert.ok(await screen.select(zh.qy_sdadm_pr_pick, '3'), '找不到套餐下拉')
    const gets = screen.sent.filter(
      (row) =>
        row.method === 'GET' &&
        row.url.endsWith('/admin/stardust/plan-rewards/3')
    )
    assert.equal(gets.length, 1, '选了套餐却没有去取它的返还定义')
    assert.ok(
      screen.text().includes(zh.qy_sdadm_pr_state_default),
      '没配过的套餐要标成默认口径'
    )

    // 四个复选框按闭集顺序渲染：order / balance / admin / redemption。
    //
    // 点的是 Base UI 藏在按钮旁边的那个原生 checkbox：它的 `onChange` 才是产品代码
    // 里 `onCheckedChange` 的入口。真浏览器里点按钮会把 click 转发给它，happy-dom
    // 没有实现这一段转发，直接 `input.click()` 走的是同一条 onChange。
    const boxes = [...document.body.querySelectorAll('[role="checkbox"]')]
    assert.equal(boxes.length, 4, '来源复选框不是四个')
    const adminInput = boxes[2].nextElementSibling
    assert.ok(
      adminInput instanceof HTMLInputElement,
      '按钮旁边没有那个原生 checkbox'
    )
    await act(async () => {
      adminInput.click()
    })
    await screen.settle()
    assert.equal(boxes[2].getAttribute('aria-checked'), 'true', 'admin 没勾上')

    screen.sent.length = 0
    const saves = [...document.body.querySelectorAll('button')].filter(
      (node) => (node.textContent ?? '').trim() === zh.qy_sdadm_cfg_save
    )
    // 页面上有两颗「保存」：可写段那一颗（无改动、禁用）与套餐编辑器这一颗。
    const enabled = saves.find((node) => !node.disabled)
    assert.ok(enabled != null, '套餐编辑器的保存键没有亮起来')
    await act(async () => {
      enabled.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await screen.settle()

    const puts = screen.sent.filter(
      (row) =>
        row.method === 'PUT' &&
        row.url.endsWith('/admin/stardust/plan-rewards/3')
    )
    assert.equal(puts.length, 1)
    assert.deepEqual(puts[0].body, {
      buyer_bps: 10_000,
      sources: ['order', 'balance', 'admin'],
    })
  })
})
