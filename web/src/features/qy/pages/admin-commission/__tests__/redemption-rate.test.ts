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
import { readFileSync } from 'node:fs'
import { describe, test } from 'node:test'

import en from '../../../../../i18n/qy/en.json'
import zh from '../../../../../i18n/qy/zh.json'
import {
  qyCommissionFieldMeta,
  qyIsValidNullablePercent,
  qyIsValidPercent,
} from '../lib/fields'

/**
 * redemption-rate.test.ts —— 兑换码这一档的「空 ≠ 0」必须在界面上也成立。
 *
 * 后端把这一档做成可空的（全局是 `*int`，分组是可空列），空表示"没单独配，
 * 跟随充值档"，`0` 表示显式 0%。前端只要有一处把两者混起来 —— 空输入框被
 * 当成非法而挡住保存、空被规范化成 `'0'` 提交、表格用真值判断把 `'0'` 画成
 * "跟随" —— 结果都是运营看到的与真正生效的差一整档比例，而账本上每一行
 * 仍然自洽，事后查不出来。
 *
 * 断言直接读源文件的那几条，是因为这些判定散在 JSX 里，没有可导出的纯函数
 * 承载；而它们恰恰是最容易在后续重构中被"简化"掉的形状（`x ? … : …`
 * 读起来永远比 `x != null ? … : …` 顺眼）。
 */

const INDEX = readFileSync(
  new URL('../index.tsx', import.meta.url),
  'utf8'
)

describe('兑换码档的空值语义', () => {
  test('空是合法输入，与 0% 分得开', () => {
    // 空必须被判成合法：它是一个要提交上去的取值（"取消这一档"），
    // 判非法的话保存按钮会被永久禁用，运营根本改不回"跟随"。
    assert.equal(qyIsValidNullablePercent(''), true)
    assert.equal(qyIsValidNullablePercent('   '), true)
    // 而普通百分比键的空仍然非法 —— 充值档/消费档没有"不配"这个状态。
    assert.equal(qyIsValidPercent(''), false)

    // 0 是显式配置，两个校验器都必须放行。
    assert.equal(qyIsValidNullablePercent('0'), true)
    assert.equal(qyIsValidPercent('0'), true)

    // 非法值不因为可空就被放过。
    for (const bad of ['101', '-1', '1.005', 'abc']) {
      assert.equal(
        qyIsValidNullablePercent(bad),
        false,
        `${bad} 必须仍然非法`
      )
    }
  })

  test('字段元数据已登记，界面不会退化成裸 key', () => {
    const meta = qyCommissionFieldMeta('redemption_rate_percent')
    assert.notEqual(meta, null, 'redemption_rate_percent 没有登记元数据')
    assert.equal(meta?.unit, 'percent')
    assert.equal(meta?.min, 0)
    assert.equal(meta?.max, 100)
    // 这一档不是"0 表示不限"那一类：0 就是 0%，界面绝不能标成「不限」。
    assert.notEqual(
      meta?.zeroMeansUnlimited,
      true,
      '兑换码档的 0 是"不返佣"，不是"不限"'
    )
  })

  test('分组表用 != null 判定，不用真值判定', () => {
    // `'0'` 在 JS 里是假值。写成 `rule.redemption_rate_percent ? … : 跟随`
    // 会把一个显式 0% 的分组显示成"跟随充值档"，而那两者差的是一整档比例。
    assert.match(
      INDEX,
      /rule\.redemption_rate_percent != null/,
      '分组表必须用 != null 区分"没配"与"显式 0%"'
    )
    assert.doesNotMatch(
      INDEX,
      /\{\s*rule\.redemption_rate_percent\s*\?/,
      '不得用真值判断：0% 会被误画成"跟随"'
    )
  })

  test('分组保存时显式提交 null，而不是省略字段', () => {
    // 这个接口是整行 upsert。开关关掉却不发字段，与发 null 后端结果相同，
    // 但写出来才能在 diff 里看见"这一次保存把兑换码档取消了"。
    assert.match(
      INDEX,
      /redemption_rate_percent: draft\.redemptionEnabled[\s\S]{0,120}: null,/,
      '开关关闭时必须显式提交 null'
    )
  })

  test('可空键由后端下发，前端不自己写死键名', () => {
    // 前端猜错的方向恰好是把空当成 0 提交上去 —— 一次没有人批准的费率归零。
    assert.match(
      INDEX,
      /config\.nullable_percent_keys/,
      '可空键必须来自接口的 nullable_percent_keys'
    )
  })
})

describe('兑换码档的文案', () => {
  const keys = [
    'qy_cm_f_redemption_rate',
    'qy_cm_f_redemption_rate_hint',
    'qy_cm_f_redemption_follows',
    'qy_cm_gr_redemption_inherit',
    'qy_cm_gr_redemption_toggle',
    'qy_cm_gr_redemption_off_hint',
    'qy_aff_rate_redemption',
    'qy_aff_rate_value_follows_topup',
  ]

  for (const key of keys) {
    test(`${key} 中英文都有`, () => {
      assert.equal(
        typeof (zh as Record<string, string>)[key],
        'string',
        `${key} 缺中文`
      )
      assert.equal(
        typeof (en as Record<string, string>)[key],
        'string',
        `${key} 缺英文`
      )
    })
  }

  test('提示必须把"留空"与"填 0"分别说清楚', () => {
    // 这一句是运营在界面上唯一能读到的口径说明。少了任何一半，
    // "我把它设成 0 了"和"我把它清空了"就会被当成同一个操作。
    const hint = (zh as Record<string, string>)['qy_cm_f_redemption_rate_hint']
    assert.ok(hint.includes('留空'), '提示要说清楚留空是什么意思')
    assert.ok(hint.includes('0'), '提示要说清楚填 0 是什么意思')
  })
})
