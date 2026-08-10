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
import { after, describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import {
  DEFAULT_CURRENCY_CONFIG,
  useSystemConfigStore,
  type CurrencyConfig,
} from '@/stores/system-config-store'

import { qyLotMissingValues } from '../display'

/**
 * 「我为什么不能参加」里的 need / have 必须是站内余额。
 *
 * 后端把这两个数一律按整数下发，单位是什么只有条件本身知道。四条余额口径的
 * 条件原样渲染出来会变成「余额未达门槛（需 5000000，当前 4999998）」——
 * 一串用户在钱包页从来没见过的大整数：他既算不出还差多少，也不知道该充多少。
 * 而这一屏存在的全部理由就是告诉他「差在哪、差多少」。
 *
 * 天数与次数**不能**跟着换算：把 `account_age` 的 30 天格式化成 `$0.00006`
 * 是另一种同样严重的胡说。所以这里正反两面都要钉。
 */

const here = dirname(fileURLToPath(import.meta.url))

/** 站点默认换算率：1 USD = 500000 额度。下面的期望值按它手算。 */
const QUOTA_PER_UNIT = 500000

function setCurrency(patch: Partial<CurrencyConfig>) {
  const state = useSystemConfigStore.getState()
  state.setConfig({
    ...state.config,
    currency: { ...DEFAULT_CURRENCY_CONFIG, ...patch },
  })
}

after(() => setCurrency({}))

describe('qyLotMissingValues 把额度口径的缺失项换算成站内余额', () => {
  test('货币展示下四条额度条件都不再露出原始 quota', () => {
    setCurrency({ quotaDisplayType: 'USD', quotaPerUnit: QUOTA_PER_UNIT })

    // 5000000 / 500000 = 10 USD、2500000 / 500000 = 5 USD。
    // |v| >= 1 走 digitsLarge = 2，而末尾零由 Intl 的 minimumFractionDigits: 0
    // 吃掉，所以是 `$10` 而不是 `$10.00`。
    for (const code of ['balance', 'stake', 'used_quota', 'recent_spend']) {
      const got = qyLotMissingValues({ code, need: 5000000, have: 2500000 })
      assert.equal(got.need, '$10', `${code} 的 need 未走额度换算`)
      assert.equal(got.have, '$5', `${code} 的 have 未走额度换算`)
      assert.ok(
        !String(got.need).includes('5000000'),
        `${code} 的文案里仍然出现了原始 quota`
      )
    }
  })

  test('不足 1 美元的额度走 4 位小数，不会被舍成 0', () => {
    setCurrency({ quotaDisplayType: 'USD', quotaPerUnit: QUOTA_PER_UNIT })

    // 1250 / 500000 = 0.0025，|v| < 1 走 digitsSmall = 4。
    // 「差 0.0025」是可行动的信息，「差 0」会让用户以为自己其实够。
    const got = qyLotMissingValues({ code: 'balance', need: 1250, have: 0 })
    assert.equal(got.need, '$0.0025')
    assert.equal(got.have, '$0')
  })

  test('TOKENS 展示下回落成原始点数', () => {
    setCurrency({ quotaDisplayType: 'TOKENS', quotaPerUnit: QUOTA_PER_UNIT })

    // 关掉货币展示的站点，用户本来就是按点数理解余额的，这时露出整数才是对的
    // —— 「换算成站内余额」的意思是跟着站点设置走，不是无条件加货币符号。
    const got = qyLotMissingValues({ code: 'balance', need: 5000000, have: 0 })
    assert.equal(got.need, '5000000')
    assert.equal(got.have, '0')
  })

  test('天数与次数原样透传，绝不当成额度换算', () => {
    setCurrency({ quotaDisplayType: 'USD', quotaPerUnit: QUOTA_PER_UNIT })

    assert.deepEqual(
      qyLotMissingValues({ code: 'account_age', need: 30, have: 3 }),
      { need: 30, have: 3 }
    )
    assert.deepEqual(
      qyLotMissingValues({ code: 'violation_hits', need: 2, have: 5 }),
      { need: 2, have: 5 }
    )
  })

  test('缺省值回落成空串而不是 0', () => {
    setCurrency({ quotaDisplayType: 'USD', quotaPerUnit: QUOTA_PER_UNIT })

    // 「需 0，当前 0」是一句会误导人的假话：那两个位置本来就没有数。
    assert.deepEqual(qyLotMissingValues({ code: 'balance' }), {
      need: '',
      have: '',
    })
    assert.deepEqual(qyLotMissingValues({ code: 'group', need: null }), {
      need: '',
      have: '',
    })
  })
})

/**
 * 源码级守卫。
 *
 * 上面那组断言只证明 `qyLotMissingValues` 本身是对的 —— 有人把卡片改回
 * 直接插值 `missing.need` 时它们照样全绿，而用户看到的又变回大整数。
 * 这一条钉的是调用点。
 */
describe('资格卡片必须经由 qyLotMissingValues 插值', () => {
  const card = readFileSync(
    join(here, '..', '..', 'components', 'lottery-eligibility-card.tsx'),
    'utf8'
  )

  test('不得把 need / have 原样交给 t()', () => {
    assert.ok(
      !/need:\s*missing\.need/.test(card),
      '资格卡片又在直接插值原始 need，额度会退回大整数'
    )
    assert.ok(
      !/have:\s*missing\.have/.test(card),
      '资格卡片又在直接插值原始 have，额度会退回大整数'
    )
  })

  test('必须调用 qyLotMissingValues', () => {
    assert.match(card, /qyLotMissingValues\(missing\)/)
  })
})
