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
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import {
  formValuesToPlanPayload,
  planToFormValues,
  saleTimeToUnix,
} from '@/features/subscriptions/lib/plan-form'
import {
  formatSaleCountdown,
  formatSaleTime,
  isPlanPurchasable,
  planSaleBadge,
  planSaleState,
  secondsUntilSaleStart,
} from '@/features/subscriptions/lib/sale-window'
import type { SubscriptionPlan } from '@/features/subscriptions/types'

/**
 * 前端的时间窗判定必须与后端 `model.PlanSaleWindowError` **逐字一致**。
 *
 * 两侧写岔的表现是最难查的那一种：界面上写着"在售"、按钮亮着，点下去被接口
 * 拒了，而两边各自看都言之凿凿。所以这里的用例与
 * `model/subscription_sale_window_test.go` 的 TestPlanSaleWindowError 是**同一张
 * 真值表**，包括 0 的两个方向与左闭右开的两个边界。改任何一侧都必须同时改另一侧。
 */

// 固定时刻，不取 Date.now()：真实时钟会让"未开售"与"已停售"这两档在某一秒
// 变成对方，而那种失败每天只出现几次、永远复现不了。
const NOW_MS = 1_700_000_000_000
const NOW = 1_700_000_000

type Window = { sale_start_at?: number; sale_end_at?: number }

describe('planSaleState —— 与后端同一张真值表', () => {
  const cases: [string, Window, string][] = [
    ['两端都不限：随时可买（存量套餐迁移后的取值）', {}, 'on_sale'],
    [
      '窗口内',
      { sale_start_at: NOW - 3600, sale_end_at: NOW + 3600 },
      'on_sale',
    ],
    ['未开售', { sale_start_at: NOW + 1, sale_end_at: NOW + 3600 }, 'upcoming'],
    ['已停售', { sale_start_at: NOW - 3600, sale_end_at: NOW - 1 }, 'ended'],
    // 左闭：开售那一秒就能买。
    [
      '边界：此刻正好是开售时刻',
      { sale_start_at: NOW, sale_end_at: NOW + 3600 },
      'on_sale',
    ],
    // 右开：停售那一秒已经买不了。与后端 `now >= end` 同口径。
    [
      '边界：此刻正好是停售时刻',
      { sale_start_at: NOW - 3600, sale_end_at: NOW },
      'ended',
    ],
    // **这一条是本文件最重要的一行。** sale_end_at 的 0 若没被特判成"不限"，
    // `now >= 0` 恒真 —— 全站每一个没配停售时间的套餐会在这一屏上集体显示成
    // "已停售"，购买按钮全部变灰。
    ['只设开售、停售不限', { sale_start_at: NOW - 1 }, 'on_sale'],
    ['只设开售、停售不限：开售前', { sale_start_at: NOW + 1 }, 'upcoming'],
    ['只设停售、开售不限：停售前', { sale_end_at: NOW + 1 }, 'on_sale'],
    ['只设停售、开售不限：停售后', { sale_end_at: NOW }, 'ended'],
    // 后端 ValidatePlanSaleWindow 会挡住这种配置，但存量脏数据绕得过去。
    // 与后端同样先判未开售：这个套餐一天都没卖过。
    [
      '脏数据：停售早于发售',
      { sale_start_at: NOW + 10, sale_end_at: NOW - 10 },
      'upcoming',
    ],
    // 字段整个缺失（后端还没升级的实例不下发它们）。undefined 参与比较得到
    // NaN，而 NaN 的每次比较都是 false —— 结果碰巧也是 on_sale，但那是巧合
    // 而不是设计。归 0 之后它明确落在"不限制"那一档。
    ['字段缺失（旧后端）', {}, 'on_sale'],
  ]

  for (const [name, window, expected] of cases) {
    test(name, () => {
      assert.equal(planSaleState(window, NOW_MS), expected)
    })
  }

  test('plan 为 null 时按不限制处理', () => {
    assert.equal(planSaleState(null, NOW_MS), 'on_sale')
  })
})

describe('isPlanPurchasable —— enabled 与时间窗是「与」的关系', () => {
  const onSale = { sale_start_at: NOW - 10, sale_end_at: NOW + 10 }
  const ended = { sale_start_at: 0, sale_end_at: NOW - 10 }

  test('启用 + 窗口内 = 可买', () => {
    assert.equal(isPlanPurchasable({ enabled: true, ...onSale }, NOW_MS), true)
  })

  test('已下架 + 窗口内 = 不可买', () => {
    assert.equal(
      isPlanPurchasable({ enabled: false, ...onSale }, NOW_MS),
      false
    )
  })

  // 这一条是"或"的关系会写反的那一半：若写成或，一个被手动下架的套餐会在
  // 开售时间到达时自己重新上架 —— 运营主动下架的东西又出现在货架上。
  test('启用 + 已停售 = 不可买', () => {
    assert.equal(isPlanPurchasable({ enabled: true, ...ended }, NOW_MS), false)
  })

  test('已下架 + 已停售 = 不可买', () => {
    assert.equal(isPlanPurchasable({ enabled: false, ...ended }, NOW_MS), false)
  })
})

describe('planSaleBadge —— 手动下架的优先级高于时间窗', () => {
  // 反过来（时间窗优先）会让一个已下架的套餐在管理端列表上显示成"在售"，
  // 那是这一列能犯的最严重的错。
  test('已下架 + 在售窗口 → 显示已下架', () => {
    const badge = planSaleBadge(
      { enabled: false, sale_start_at: NOW - 10, sale_end_at: NOW + 10 },
      NOW_MS
    )
    assert.equal(badge.labelKey, 'qy_plan_sale_state_disabled')
  })

  // 这一条才是"优先级"真正被钉住的地方：上一条在两种优先级下都返回已下架
  // （时间窗那一档说的是"在售"，压不过任何东西）。只有当**两个维度同时说不**
  // 时，两种优先级才给出不同答案 —— 反过来的实现会在这里显示"已停售"，
  // 而运营据此去改停售时间，改完发现还是卖不出去（真正的原因是开关关着）。
  test('已下架 + 已停售 → 仍然显示已下架', () => {
    const badge = planSaleBadge(
      { enabled: false, sale_start_at: 0, sale_end_at: NOW - 10 },
      NOW_MS
    )
    assert.equal(badge.labelKey, 'qy_plan_sale_state_disabled')
  })

  test('已下架 + 未开售 → 仍然显示已下架', () => {
    const badge = planSaleBadge(
      { enabled: false, sale_start_at: NOW + 10 },
      NOW_MS
    )
    assert.equal(badge.labelKey, 'qy_plan_sale_state_disabled')
  })

  test('启用 + 未开售 → 显示未开售', () => {
    const badge = planSaleBadge(
      { enabled: true, sale_start_at: NOW + 10 },
      NOW_MS
    )
    assert.equal(badge.labelKey, 'qy_plan_sale_state_upcoming')
  })

  test('启用 + 已停售 → 显示已停售', () => {
    const badge = planSaleBadge(
      { enabled: true, sale_end_at: NOW - 10 },
      NOW_MS
    )
    assert.equal(badge.labelKey, 'qy_plan_sale_state_ended')
  })

  test('启用 + 不限制 → 显示在售', () => {
    const badge = planSaleBadge({ enabled: true }, NOW_MS)
    assert.equal(badge.labelKey, 'qy_plan_sale_state_on_sale')
  })
})

describe('倒计时', () => {
  test('未到开售时间：返回剩余秒数', () => {
    assert.equal(secondsUntilSaleStart({ sale_start_at: NOW + 90 }, NOW_MS), 90)
  })

  // null 表示"没有倒计时可言"，调用方据此退回静态的"敬请期待"。
  // 返回 0 的话，界面会显示"距开售还有 0 秒"并且永远停在那里。
  test('没配开售时间：null', () => {
    assert.equal(secondsUntilSaleStart({}, NOW_MS), null)
  })

  test('已经过了开售时间：null', () => {
    assert.equal(secondsUntilSaleStart({ sale_start_at: NOW }, NOW_MS), null)
  })

  const t = ((key: string) =>
    ({ days: '天', hours: '小时', minutes: '分', seconds: '秒' })[key] ??
    key) as never

  test('只显示前两个非零单位', () => {
    // 3 天 4 小时 12 分 5 秒 —— 分与秒被折掉。每秒都在跳的两位对"还剩多久"
    // 这个决定毫无帮助，反而看不出量级。
    const total = 3 * 86400 + 4 * 3600 + 12 * 60 + 5
    assert.equal(formatSaleCountdown(total, t), '3 天 4 小时')
  })

  test('不足一小时自然降到分与秒', () => {
    assert.equal(formatSaleCountdown(12 * 60 + 5, t), '12 分 5 秒')
  })

  test('不足一秒显示 0 秒而不是空串', () => {
    // 空串会让整行消失，而这一刻恰恰是最该看到"马上就开了"的时候。
    assert.equal(formatSaleCountdown(0, t), '0 秒')
  })
})

describe('formatSaleTime', () => {
  test('0 返回空串（不限制），调用方据此整行不渲染', () => {
    // 刻意不是 '-'：破折号会被读成"没读到"，而这里的事实是"没有限制"。
    assert.equal(formatSaleTime(0), '')
    assert.equal(formatSaleTime(undefined), '')
  })

  test('越界时间戳返回空串而不是 Invalid Date', () => {
    // 毫秒粘进秒字段的典型脏数据。后端有上界校验，但存量行绕得过去，
    // 而 dayjs 的 format() 会把 "Invalid Date" 四个字直接糊在界面上。
    assert.equal(formatSaleTime(1e18), '')
  })
})

describe('表单 ↔ 时间戳的往返', () => {
  test('空串 ↔ 0（不限制）', () => {
    assert.equal(saleTimeToUnix(''), 0)
    assert.equal(saleTimeToUnix(undefined), 0)
    assert.equal(saleTimeToUnix('   '), 0)
  })

  test('解析不出来的输入归 0 而不是 NaN', () => {
    // NaN 落库会让后端的 JSON 解析直接失败（或者更糟：被当成 0 之外的某个值），
    // 而运营看到的只是一次没有说明的"参数错误"。
    assert.equal(saleTimeToUnix('not a date'), 0)
  })

  /*
    ⚠ 这一条**在 `bun test` 里验不出时区错误**，如实记下来。

    `bun test` 强制把进程时区设成 UTC（实测 `new Date().getTimezoneOffset()`
    在测试进程里恒为 0）。在 UTC 下，"用本地时间字段拼串"与
    `toISOString().slice(0,16)` 的输出**完全相同** —— 把 saleTimeToInput 改成
    后者，这条用例照样全绿（已做变异验证确认）。

    它仍然值得留着：它钉住的是**往返恒等**这条契约（补零、跨月、跨年），
    那部分在 UTC 下同样有效。真正的时区正确性只能在非 UTC 进程里验证，
    本轮是手工跑 `bun -e` 比对的（UTC-7 下，正确实现给 10:00、UTC 实现给 17:00）。
  */
  test('datetime-local 按本地时区解析，与回填互为逆运算', () => {
    // 用 UTC 解析的话，运营填的 10:00 保存后再打开会变成别的时间（东八区差
    // 8 小时），而那种偏移只有真正去比对才看得出来。
    const input = '2026-08-17T10:00'
    const unix = saleTimeToUnix(input)
    assert.equal(unix, Math.floor(new Date(input).getTime() / 1000))

    const plan = {
      ...basePlan,
      sale_start_at: unix,
      sale_end_at: 0,
    }
    assert.equal(planToFormValues(plan).sale_start_at_input, input)
  })

  // 这一条钉的是"清空能真的清掉"。上游 AdminUpdateSubscriptionPlan 是显式 map
  // 全量覆盖，两列恒在 map 里 —— 前端提交 0 就是真的把时间窗清回不限制。
  // 若这里漏掉了转换（比如把 `_input` 字段原样透传），Go 侧会静默丢弃未知键，
  // 而表单照样弹"保存成功"，时间窗压根没进库。
  test('提交时把 _input 字段换成时间戳，且不把 _input 透传给后端', () => {
    const values = {
      ...planToFormValues(basePlan),
      sale_start_at_input: '2026-08-17T10:00',
      sale_end_at_input: '',
    }
    const payload = formValuesToPlanPayload(values)
    assert.equal(
      payload.plan.sale_start_at,
      Math.floor(new Date('2026-08-17T10:00').getTime() / 1000)
    )
    assert.equal(payload.plan.sale_end_at, 0)
    assert.ok(!('sale_start_at_input' in payload.plan))
    assert.ok(!('sale_end_at_input' in payload.plan))
  })
})

const basePlan: SubscriptionPlan = {
  id: 1,
  title: '月付',
  price_amount: 10,
  currency: 'USD',
  duration_unit: 'month',
  duration_value: 1,
  quota_reset_period: 'never',
  enabled: true,
  sort_order: 0,
  allow_balance_pay: true,
  allow_wallet_overflow: true,
  max_purchase_per_user: 0,
  total_amount: 0,
  no_quota: false,
  upgrade_group: '',
  downgrade_group: '',
}
