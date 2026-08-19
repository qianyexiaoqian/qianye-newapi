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
 * 「到账时间」这一格不许再拿配置去反算历史。
 *
 * # 缺陷形状
 *
 * 成熟期(holding_days)是**逐行冻结**的:计佣行落库时算出 mature_at,之后改
 * 配置不追溯已冻结的行。而这一页此前只显示一个数 —— 按**当前配置**算出来的
 * `payout_day_offset = holding_days + 1`。于是运营把 7 改成 0 的那天,一个昨天
 * 就挣到钱的用户看到「T+1 到账」,实际要等到第 8 天;界面与账本差整整一周,
 * 而两边各自都自洽,没有任何东西会报错。
 *
 * # 修法:让显示跟随事实
 *
 * 不追溯改写账本(那违反本模块「冻结不追溯」的一贯口径),也不把成熟期塞进
 * 界面的算式里。改成两个数并排:
 *
 *   · `policy.payout_day_offset`          按当前配置算的 T+N,只对**新产生**的消费成立
 *   · `pending_earliest_mature_at`        账本上写着的、在途佣金最早的成熟时刻
 *
 * 这条测试钉住的正是"两个都要有"。少任何一个都会给出假绿:
 *
 *   · 只有前者 → 就是缺陷本身;
 *   · 只有后者 → 一个还没有任何在途佣金的新用户,这一页对"我以后多久能拿到钱"
 *     一个字都答不出来。
 *
 * 文案里那句「新产生的消费」同样是判据的一部分:少了它,即使两个数都显示,
 * 用户也会把上面那个 T+N 当成自己那笔钱的到账日 —— 而那正是要消灭的误读。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { describe, test } from 'node:test'

import en from '../../../../../i18n/qy/en.json'
import zh from '../../../../../i18n/qy/zh.json'

const INDEX = readFileSync(new URL('../index.tsx', import.meta.url), 'utf8')
const TYPES = readFileSync(new URL('../types.ts', import.meta.url), 'utf8')

describe('到账时间:配置口径与账本事实必须同时出现', () => {
  test('两个数据源都被这一页读到', () => {
    assert.ok(
      INDEX.includes('summary.policy.payout_day_offset'),
      '少了按配置算的 T+N,新用户看不到"以后多久到账"'
    )
    assert.ok(
      INDEX.includes('summary.pending_earliest_mature_at'),
      '少了账本上的成熟时刻,改一次 holding_days 这一页就对所有已有在途佣金的人说错话'
    )
  })

  test('两个数各占一行,不是把一个塞进另一个的文案里', () => {
    assert.ok(
      INDEX.includes("key: 'payout-eta'") && INDEX.includes("key: 'pending-mature'"),
      '两格必须各自成行:合并成一句话之后,"这是配置"与"这是我的钱"再也分不开'
    )
  })

  test('没有在途佣金时不编一个日期出来', () => {
    assert.ok(
      INDEX.includes('summary.pending_earliest_mature_at > 0'),
      '后端用 0 表示"没有需要等的东西",直接格式化 0 会显示 1970-01-01'
    )
    for (const [name, dict] of [
      ['zh', zh],
      ['en', en],
    ] as const) {
      assert.ok(
        (dict as Record<string, string>).qy_aff_pending_mature_none,
        `${name} 缺 qy_aff_pending_mature_none`
      )
    }
  })

  test('T+N 的文案必须写明它只对新产生的消费成立', () => {
    const zhDict = zh as Record<string, string>
    for (const key of [
      'qy_aff_payout_eta_value',
      'qy_aff_payout_eta_value_utc',
    ]) {
      assert.ok(
        zhDict[key].includes('新产生的消费'),
        `${key} 没写明适用范围 —— 用户会拿它推算已经挣到手那笔钱的到账日`
      )
    }
    const enDict = en as Record<string, string>
    for (const key of [
      'qy_aff_payout_eta_value',
      'qy_aff_payout_eta_value_utc',
    ]) {
      assert.ok(
        /newly generated/i.test(enDict[key]),
        `${key} (en) 没写明适用范围`
      )
    }
  })

  test('类型上把两者的分工写死,不让后来者把它们当同一个数', () => {
    assert.ok(
      TYPES.includes('pending_earliest_mature_at'),
      '契约类型里没有这个字段 = 后端下发了但前端看不见'
    )
  })
})
