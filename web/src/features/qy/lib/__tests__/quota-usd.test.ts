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
import { dirname, join } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import {
  qyFormatQuotaAsUsd,
  qyQuotaDraftText,
  qyQuotaDraftValue,
  qyQuotaToUsdText,
  qyUsdScale,
  qyUsdTextToQuota,
} from '../quota-usd'

/** 站点默认换算率：1 USD = 500000 额度，也就是 1 额度 = 0.000002 USD。 */
const defaultScale = qyUsdScale(500000)

/** 主库额度列是 int32，门槛的上界就是它。 */
const MAX_QUOTA = 2147483647

describe('qyUsdScale 判定换算率是否可用', () => {
  test('1e6 的因数一律可用，microsPerQuota 是 1e6 / quotaPerUnit', () => {
    for (const [per, micros] of [
      [1, 1000000],
      [100, 10000],
      [500000, 2],
      [1000000, 1],
    ] as const) {
      const scale = qyUsdScale(per)
      assert.equal(scale.usable, true, `quotaPerUnit=${per} 应当可用`)
      assert.equal(scale.microsPerQuota, micros)
    }
  })

  test('除不尽 1e6 的换算率不可用：那种站点必须退回额度单位录入', () => {
    // 300000：1 额度 = 0.00000333… USD，6 位小数表示不了，显示出来就是失真值。
    // 2000000：1 额度 = 0.5 微元，比本模块的最小刻度还细。
    for (const per of [3, 300000, 2000000, 7]) {
      assert.equal(qyUsdScale(per).usable, false, `quotaPerUnit=${per}`)
    }
  })

  test('缺失/非法的 quotaPerUnit 不可用，而不是当成某个默认值', () => {
    for (const per of [0, -500000, 2.5, Number.NaN, null, undefined]) {
      assert.equal(qyUsdScale(per).usable, false, `quotaPerUnit=${String(per)}`)
    }
  })

  test('不可用时 configuredRate 仍然说出站点真实配置的那个数', () => {
    // 顶部那条红色告警的原话是「站点配置的是 1 USD = {{rate}} 额度，这个换算率
    // 没法把每个额度值都无损写成 USD」。它拿的必须是站点**真实**配置的值 ——
    // 早先它读的是被归零后的 quotaPerUnit，于是这句话变成「1 USD = 0 额度」，
    // 而运营在系统设置里看到的是 2.5，没有任何一处能解释这个 0 从哪来。
    // 整页唯一一处必须说真话的汇率数字，那条分支上说的是假话。
    //
    // 2.5 / 300000 都是真实可达的配置：系统设置的 schema 是
    // `z.coerce.number().min(0)` + step 0.01，没有 .int()。
    for (const per of [2.5, 300000, 3, 500000.5]) {
      const scale = qyUsdScale(per)
      assert.equal(scale.usable, false, `quotaPerUnit=${per}`)
      assert.equal(
        scale.configuredRate,
        per,
        '告警里播出去的必须是站点真实配置值'
      )
      assert.equal(scale.quotaPerUnit, 0, '换算用的那个字段仍然必须归零')
    }
  })

  test('根本没有配出正数时 configuredRate 是 null，界面据此换一句话说', () => {
    // 这一档不能和上面那档共用文案：「这个换算率没法无损表示」对一个
    // 压根不存在的换算率是答非所问，运营会去找一个不存在的数字。
    for (const per of [
      0,
      -1,
      Number.NaN,
      Number.POSITIVE_INFINITY,
      null,
      undefined,
    ]) {
      assert.equal(
        qyUsdScale(per).configuredRate,
        null,
        `quotaPerUnit=${String(per)}`
      )
    }
  })

  test('可用时 configuredRate 与 quotaPerUnit 一致', () => {
    const scale = qyUsdScale(500000)
    assert.equal(scale.configuredRate, 500000)
    assert.equal(scale.quotaPerUnit, 500000)
  })
})

describe('额度 → USD 文本', () => {
  test('去掉尾随零，最小刻度是 1 额度', () => {
    for (const [quota, text] of [
      [0, '0'],
      [1, '0.000002'],
      [15000, '0.03'],
      [500000, '1'],
      [50000000, '100'],
      [200000000, '400'],
      [MAX_QUOTA, '4294.967294'],
    ] as const) {
      assert.equal(qyQuotaToUsdText(quota, defaultScale), text)
    }
  })

  test('换算率不可用时返回 null，不返回一个四舍五入过的数', () => {
    assert.equal(qyQuotaToUsdText(15000, qyUsdScale(300000)), null)
  })

  test('只读展示带 $；换算不可用时回退成额度整数', () => {
    assert.equal(qyFormatQuotaAsUsd(15000, defaultScale), '$0.03')
    assert.equal(qyFormatQuotaAsUsd(15000, qyUsdScale(300000)), '15000')
  })
})

describe('USD 文本 → 额度', () => {
  test('按字面量精确换算，不走浮点乘法', () => {
    for (const [text, quota] of [
      ['0', 0],
      ['1', 500000],
      ['0.03', 15000],
      ['0.000002', 1],
      ['4294.967294', MAX_QUOTA],
      ['0100.5', 50250000],
    ] as const) {
      assert.equal(qyUsdTextToQuota(text, defaultScale), quota)
    }
  })

  test('浮点乘法在这些取值上真的会差一个额度', () => {
    // 这条是本模块存在的理由。`1.001 * 500000` 在 IEEE754 下是
    // 500499.99999999994，按截断口径（也就是后端 AGENTS.md 点名禁掉的裸
    // `int()` 转换）就落到 500499，比运营填的少一个额度。
    assert.equal(Math.trunc(1.001 * 500000), 500499)
    assert.equal(qyUsdTextToQuota('1.001', defaultScale), 500500)

    assert.equal(Math.trunc(0.000498 * 500000), 248)
    assert.equal(qyUsdTextToQuota('0.000498', defaultScale), 249)
  })

  test('小数位超过 6 位判非法，不截断也不四舍五入', () => {
    assert.equal(qyUsdTextToQuota('0.0000001', defaultScale), null)
    assert.equal(qyUsdTextToQuota('1.1234567', defaultScale), null)
  })

  test('当前换算率下除不尽整数额度的金额判非法', () => {
    // 1 微元 = 0.5 额度，存不成整数。四舍五入到 0 或 1 都是替运营改了数。
    assert.equal(qyUsdTextToQuota('0.000001', defaultScale), null)
    assert.equal(qyUsdTextToQuota('0.000003', defaultScale), null)
    assert.equal(qyUsdTextToQuota('0.000004', defaultScale), 2)
  })

  test('负数、科学计数法、空串、千分位一律判非法', () => {
    for (const text of [
      '',
      ' ',
      '-1',
      '1e5',
      '1,000',
      '.5',
      '1.',
      'abc',
      '＄1',
    ]) {
      assert.equal(qyUsdTextToQuota(text, defaultScale), null, `输入 ${text}`)
    }
  })

  test('大到不安全的整数判非法，而不是悄悄丢精度', () => {
    assert.equal(qyUsdTextToQuota('99999999999999999999', defaultScale), null)
  })
})

describe('往返一致性：显示 USD 再存回去，额度必须逐位相同', () => {
  /**
   * 这条不变量决定的是审计的可信度。界面把额度换成 USD 显示，运营什么都没改
   * 直接点保存，换回来只要差一个额度，审计里就会多出一条无中生有的门槛变更 ——
   * 而门槛变更正是事后要追的那条线索。
   */
  test('全部可用换算率 × 取值域端点与实际默认值', () => {
    const quotaValues = [
      0,
      1,
      2,
      99,
      500000, // transfer.min_quota 的 YAML 默认值
      50000000, // transfer.max_per_tx_quota
      200000000, // transfer.daily_max_quota
      123456789,
      MAX_QUOTA - 1,
      MAX_QUOTA,
    ]
    // 1e6 的全部因数，也就是 qyUsdScale 认为可用的全部换算率。
    const usableRates = [
      1, 2, 4, 5, 8, 10, 16, 20, 25, 32, 40, 50, 64, 80, 100, 125, 200, 250,
      500, 1000, 2000, 2500, 5000, 10000, 20000, 50000, 100000, 125000, 200000,
      250000, 500000, 1000000,
    ]

    for (const rate of usableRates) {
      const scale = qyUsdScale(rate)
      assert.equal(scale.usable, true, `quotaPerUnit=${rate} 应当可用`)
      for (const quota of quotaValues) {
        const text = qyQuotaDraftText(quota, scale, true)
        assert.equal(
          qyQuotaDraftValue(text, scale, true),
          quota,
          `quotaPerUnit=${rate} 下 ${quota} 显示成 "${text}" 后换不回原值`
        )
      }
    }
  })

  test('换算率不可用时走整数通道，同样逐位相同', () => {
    const scale = qyUsdScale(300000)
    for (const quota of [0, 1, 500000, MAX_QUOTA]) {
      const text = qyQuotaDraftText(quota, scale, false)
      assert.equal(text, String(quota))
      assert.equal(qyQuotaDraftValue(text, scale, false), quota)
    }
  })

  test('整数通道拒绝小数：那是纯计数字段，不该被当成金额填', () => {
    assert.equal(qyQuotaDraftValue('1.5', defaultScale, false), null)
    assert.equal(qyQuotaDraftValue('10', defaultScale, false), 10)
  })
})

/* ── 三、三个配置页确实接在这一对函数上 ─────────────────────────────── */

// __tests__ → lib → qy
const qyDir = join(dirname(fileURLToPath(import.meta.url)), '..', '..')

/**
 * 往返一致性是一条**跨文件**的契约：只有页面草稿同时经由 `qyQuotaDraftText`
 * 生成、`qyQuotaDraftValue` 读回，上面那些用例才代表页面的真实行为。
 *
 * 少接一头不会崩、不会红，表现只是「运营什么都没改点一下保存，审计里多出
 * 一条门槛变更」—— 而那正是事后要追的那条线索被污染的样子。这条走查就是
 * 给这个没有信号的失败装一个信号。
 */
describe('金额配置页接线', () => {
  const pages = [
    'admin-transfer-config/index.tsx',
    'admin-commission/index.tsx',
    'admin-lottery-config/index.tsx',
  ]

  for (const page of pages) {
    test(`${page} 同时用到 qyQuotaDraftText 与 qyQuotaDraftValue`, () => {
      const source = readFileSync(join(qyDir, 'pages', page), 'utf8')
      for (const symbol of ['qyQuotaDraftText', 'qyQuotaDraftValue']) {
        assert.ok(
          source.includes(symbol),
          `${page} 没有用 ${symbol}：换算的一头断了，往返一致性不再成立`
        )
      }
    })
  }
})
