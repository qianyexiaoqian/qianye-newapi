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

import enKeys from '@/i18n/qy/en.json'
import zhKeys from '@/i18n/qy/zh.json'

import { formatQyQuotaBound, parseQyQuota } from '../../../../lib/format'
import type { QyLotYamlReadonly } from '../../types'
import {
  QY_LOT_BET_MAX_MULTIPLE,
  qyLotEntriesCap,
  qyLotPoolShareHeadroom,
  qyLotRecommendedBetMax,
  qyLotRecommendedMinEntries,
  qyLotTierAmountFloor,
  qyLotTierBudgetShort,
  qyLotTierCountFloor,
  qyLotWinPpmHeadroom,
} from '../advice'
import { qyLotEmptyDraft, qyLotValidateDraft, type QyLotDraft } from '../draft'

/**
 * 「别再让人猜，给推荐值」这件事在前端的可执行判据。
 *
 * 项目方原话：「创建活动，你不告诉我要怎么设置推荐值？一堆这种『固定奖级的
 * 预算（额度 × 份数）必须不小于全场参与上限，否则超募时会有中奖者被摊薄到 0
 * 而拿不到钱』很烦啊」
 *
 * 一个推荐值只有满足一条才有价值：**它自己必须能过校验**。"界面填好、提交被
 * 拒"比不给推荐值更糟 —— 那会让人从此不信任何一个自动填。所以下面每一条都是
 * 把 `*Floor` 算出来的数**喂回 `qyLotValidateDraft`**（提交前那份跨步校验，
 * 与字段旁的实时提示同源），而不是断言某个函数返回了某个常量。
 *
 * 后端那一半在 `qianye/modules/lottery/advice_test.go`：同一条不等式、同一个
 * 向上取整。
 */

const YAML: QyLotYamlReadonly = {
  enabled: true,
  proof_public: true,
  pay_password_threshold_quota: 100_000,
  entry_close_grace_seconds: 60,
  reveal_delay_seconds: 60,
  payout_max_attempts: 8,
  max_total_entries_hard: 50_000,
  max_prize_tiers: 10,
  max_options: 12,
  max_stake_quota: 0,
  system_max_quota: 2_147_483_647,
  spend_max_lookback_days: 30,
  spend_ready_from: 20_260_101,
}

/** 一份除了奖档之外处处合法的概率制草稿。 */
function probDraft(patch: Partial<QyLotDraft> = {}): QyLotDraft {
  const base = qyLotEmptyDraft(500)
  return {
    ...base,
    draw_mode: 'prob',
    title: '推荐值验收',
    stake_quota: 500_000,
    max_total_entries: 100,
    ...patch,
  }
}

function tiersOf(amount: number, count: number) {
  return [
    {
      tier: 1,
      name: '一等奖',
      amount_quota: amount,
      count,
      win_ppm: 10_000,
      red_match: 0,
      blue_match: 0,
      pool_share_bps: 0,
    },
  ]
}

function budgetErrors(draft: QyLotDraft): string[] {
  return qyLotValidateDraft(draft, YAML, 0, 2000).filter(
    (key) => key === 'qy_lot_v_prob_budget_short'
  )
}

describe('单份下限：推荐值恰好是校验能接受的最小值', () => {
  const cases: [
    name: string,
    entriesCap: number,
    count: number,
    want: number,
  ][] = [
    ['整除', 100, 10, 10],
    ['除不尽必须向上取整', 101, 10, 11],
    ['只发一份', 50_000, 1, 50_000],
    ['份数比票数还多', 10, 100, 1],
  ]

  for (const [name, entriesCap, count, want] of cases) {
    test(name, () => {
      const floor = qyLotTierAmountFloor(entriesCap, count)
      assert.equal(floor, want)

      const draft = probDraft({
        max_total_entries: entriesCap,
        tiers: tiersOf(floor, count),
      })
      assert.deepEqual(
        budgetErrors(draft),
        [],
        '推荐值自己过不了校验 —— 界面填好、提交被拒，那会让人从此不信自动填'
      )

      if (floor <= 1) return
      const justBelow = probDraft({
        max_total_entries: entriesCap,
        tiers: tiersOf(floor - 1, count),
      })
      assert.deepEqual(
        budgetErrors(justBelow),
        ['qy_lot_v_prob_budget_short'],
        '推荐值不是最小可行值：比它小一个单位居然也过了'
      )
    })
  }
})

describe('份数下限：同一条不等式的另一个解', () => {
  const cases: [
    name: string,
    entriesCap: number,
    amount: number,
    want: number,
  ][] = [
    ['整除', 100, 10, 10],
    ['除不尽必须向上取整', 100, 30, 4],
    ['单份已经比全场还大', 100, 500_000, 1],
  ]

  for (const [name, entriesCap, amount, want] of cases) {
    test(name, () => {
      const floor = qyLotTierCountFloor(entriesCap, amount)
      assert.equal(floor, want)

      const draft = probDraft({
        max_total_entries: entriesCap,
        tiers: tiersOf(amount, floor),
      })
      assert.deepEqual(budgetErrors(draft), [])

      if (floor <= 1) return
      const justBelow = probDraft({
        max_total_entries: entriesCap,
        tiers: tiersOf(amount, floor - 1),
      })
      assert.deepEqual(budgetErrors(justBelow), ['qy_lot_v_prob_budget_short'])
    })
  }
})

describe('零值：算不出来的时候不许编一个数出来', () => {
  test('另一格是空的就返回 0，界面据此不渲染那一行', () => {
    assert.equal(qyLotTierAmountFloor(100, 0), 0)
    assert.equal(qyLotTierAmountFloor(0, 10), 0)
    assert.equal(qyLotTierCountFloor(100, 0), 0)
    assert.equal(qyLotTierCountFloor(0, 10), 0)
    // 恒等于 0，而不是 NaN / Infinity —— 后两者会原样渲染到界面上。
    assert.ok(Number.isInteger(qyLotTierAmountFloor(100, 0)))
    assert.ok(Number.isInteger(qyLotTierCountFloor(100, 0)))
  })

  test('全场参与上限填 0 时按系统硬上限算，不是按 0 算', () => {
    // 后端 `buildActivity` 把 max_total_entries=0 归一成硬上限（名单冻结必须
    // 有上界）。按 0 算会给出一个"提交必被拒"的推荐值。
    assert.equal(
      qyLotEntriesCap(probDraft({ max_total_entries: 0 }), 50_000),
      50_000
    )
    assert.equal(
      qyLotEntriesCap(probDraft({ max_total_entries: 200 }), 50_000),
      200
    )
  })
})

describe('实时提示与提交校验同源', () => {
  /*
    字段旁边的红字用 `qyLotTierBudgetShort`，提交前那份跨步校验也用它
    （`draft.ts` 已改成调用同一个函数）。这条测试把两侧摆在一起对：任何一侧
    单独改口径，这里立刻红。

    只断言"两个都为真/都为假"是不够的——两侧同时错也会通过。所以还钉住了
    `qyLotTierAmountFloor` 这个第三方基准：判据的边界必须恰好落在推荐值上。
  */
  /*
    两个 entriesCap 缺一不可，各自钉住一种改错：

      · 97 除不尽 7 —— 向上取整改成向下取整时，推荐值会掉到 13，而 13×7=91
        仍然不足；
      · 98 恰好整除 —— 判据从 `<` 改成 `≤` 时，推荐值 14 会被自己判成"不足"。
        只用 97 的话这个变异存活：98 那个等号是它唯一的落点。
  */
  for (const [entriesCap, count, floor] of [
    [97, 7, 14],
    [98, 7, 14],
  ] as const) {
    test(`逐点对齐（全场 ${entriesCap} 张票 / ${count} 份）`, () => {
      assert.equal(qyLotTierAmountFloor(entriesCap, count), floor)

      for (const amount of [1, 13, floor - 1, floor, floor + 1, 1000]) {
        const short = qyLotTierBudgetShort(entriesCap, count, amount)
        const draft = probDraft({
          max_total_entries: entriesCap,
          tiers: tiersOf(amount, count),
        })
        assert.equal(
          budgetErrors(draft).length > 0,
          short,
          `单份 ${amount} 时两侧判定不一致 —— 界面与提交会给出两个答案`
        )
        assert.equal(short, amount < floor, `单份 ${amount} 的边界不在推荐值上`)
      }
    })
  }
})

describe('剩余额度类的推荐上界', () => {
  test('中奖概率：这一格能填多大由其余各档决定', () => {
    const draft = probDraft({
      tiers: [
        { ...tiersOf(1000, 1)[0]!, tier: 1, win_ppm: 300_000 },
        { ...tiersOf(1000, 1)[0]!, tier: 2, win_ppm: 200_000 },
      ],
    })
    // 算自己那一档的余量时必须**排除自己**，否则填过的值会把自己的余量吃掉，
    // 表现是一格填完之后立刻显示"超了"。
    assert.equal(qyLotWinPpmHeadroom(draft, 1), 800_000)
    assert.equal(qyLotWinPpmHeadroom(draft, 2), 700_000)
    // 新增的一档（还不在表里）拿到的是全部已用之外的余量。
    assert.equal(qyLotWinPpmHeadroom(draft, 3), 500_000)
  })

  test('占池比例：同一条口径，分母是 10000', () => {
    const draft = probDraft({
      draw_mode: 'ball',
      tiers: [
        { ...tiersOf(0, 1)[0]!, tier: 1, pool_share_bps: 5000 },
        { ...tiersOf(0, 1)[0]!, tier: 2, pool_share_bps: 3000 },
      ],
    })
    assert.equal(qyLotPoolShareHeadroom(draft, 1), 7000)
    assert.equal(qyLotPoolShareHeadroom(draft, 2), 5000)
    assert.equal(qyLotPoolShareHeadroom(draft, 3), 2000)
  })
})

describe('最低成场人数的推荐值 = 保本参与人数', () => {
  test('⌈奖品总额 ÷ 参与费⌉，两个量表单上都有', () => {
    const draft = probDraft({
      stake_quota: 500_000,
      tiers: tiersOf(300_000, 7), // 总额 2_100_000
    })
    assert.equal(qyLotRecommendedMinEntries(draft, 50_000), 5) // ⌈2.1⌉
  })

  test('双色球不给推荐值：浮动奖的额度恒为 0，算出来是个假数', () => {
    const ball = probDraft({ draw_mode: 'ball', tiers: tiersOf(300_000, 7) })
    assert.equal(qyLotRecommendedMinEntries(ball, 50_000), 0)
  })

  test('参与费还没填时不给推荐值', () => {
    const draft = probDraft({ stake_quota: 0, tiers: tiersOf(300_000, 7) })
    assert.equal(qyLotRecommendedMinEntries(draft, 50_000), 0)
  })
})

describe('竞猜单注上限：给一个非零推荐值，而不是只加一句提醒', () => {
  test('推荐值 = 单注额 × 倍数，且倍数写在文案里', () => {
    assert.equal(
      qyLotRecommendedBetMax(500_000, 0),
      500_000 * QY_LOT_BET_MAX_MULTIPLE
    )
    // 单注额还没填时不编一个数出来。
    assert.equal(qyLotRecommendedBetMax(0, 0), 0)
    // 倍数必须出现在提示文案里：一个说不出理由的推荐值等于一个魔数。
    assert.ok(
      zhKeys.qy_lot_advice_bet_max.includes('{{multiple}}'),
      '推荐倍数没有出现在中文文案里'
    )
    assert.ok(
      (enKeys as Record<string, string>).qy_lot_advice_bet_max!.includes(
        '{{multiple}}'
      ),
      '推荐倍数没有出现在英文文案里'
    )
  })

  test('填 0 仍然合法：0 = 不限是后端的 wire 语义，改不得', () => {
    const draft: QyLotDraft = {
      ...qyLotEmptyDraft(500),
      kind: 'guess',
      title: '不限单注的竞猜',
      stake_quota: 500_000,
      bet_max_quota: 0,
      options: [
        { id: 'a', label: '甲', is_catch_all: false },
        { id: 'b', label: '乙', is_catch_all: false },
        { id: 'c', label: '以上都不是', is_catch_all: true },
      ],
    }
    const errors = qyLotValidateDraft(draft, YAML, 0, 2000)
    assert.ok(
      !errors.some((key) => key.startsWith('qy_lot_v_bet')),
      `单注上限填 0 被当成了错误：${errors.join(', ')}`
    )
  })
})

describe('推荐值与区间的文案两份语言包里都有', () => {
  const keys = [
    'qy_lot_advice_apply',
    'qy_lot_range_physical',
    'qy_lot_range_policy_stake',
    'qy_lot_range_policy_stake_unlimited',
    'qy_lot_range_policy_issue_cap',
    'qy_lot_range_policy_issue_cap_unlimited',
    'qy_lot_advice_tier_amount',
    'qy_lot_advice_tier_count',
    'qy_lot_range_count',
    'qy_lot_range_win_ppm',
    'qy_lot_range_pool_share',
    'qy_lot_range_min_entries',
    'qy_lot_advice_min_entries',
    'qy_lot_range_fee_bps',
    'qy_lot_range_bet_min',
    'qy_lot_advice_bet_min',
    'qy_lot_range_bet_max',
    'qy_lot_bet_max_zero_note',
    'qy_lot_advice_bet_max',
    'qy_lot_v_ball_cap_over_physical',
    'qy_lot_v_ball_cap_over_policy',
  ]

  test('zh / en 一个都不缺', () => {
    const zh = zhKeys as Record<string, string>
    const en = enKeys as Record<string, string>
    const missing = keys.filter((key) => zh[key] == null || en[key] == null)
    assert.deepEqual(missing, [], `语言包缺键: ${missing}`)
  })

  test('物理上限与策略上限的文案不许读起来一样', () => {
    const zh = zhKeys as Record<string, string>
    // 前者说"改任何配置都放不开"，后者必须点名是哪一项配置 —— 混成一句的
    // 表现是运营跑去配置页找一个根本不存在的开关。
    assert.ok(zh.qy_lot_range_physical!.includes('改任何配置都放不开'))
    assert.ok(!zh.qy_lot_range_physical!.includes('lottery.max_'))
    // 理由也必须经得起查：额度列在 MySQL / PostgreSQL 上是 bigint、SQLite 的
    // INTEGER 也是 8 字节，说它是"数据库列的宽度"会让运营一查表就不再相信
    // 整条解释。它是全站额度换算的整数上界，写死在代码里。
    assert.ok(!zh.qy_lot_range_physical!.includes('数据库'))
    assert.ok(!zh.qy_lot_v_ball_cap_over_physical!.includes('数据库'))
    assert.ok(zh.qy_lot_range_policy_stake!.includes('lottery.max_stake_quota'))
    assert.ok(
      zh.qy_lot_range_policy_issue_cap!.includes(
        'lottery.max_total_prize_quota'
      )
    )
  })
})

describe('念给运营听的那个数，照着填回去必须仍然合法', () => {
  /** 模拟"照着界面上那行字手打一遍"：只留下数字与小数点。 */
  function retype(shown: string): number {
    return parseQyQuota(Number(shown.replaceAll(/[^\d.]/g, '')))
  }

  test('上限：界面念出来的那个数，填回去不许越过上限', () => {
    const ceiling = YAML.system_max_quota!
    // 账本口径最多印 2 位小数，$4,294.97 填回去是 2147485000 —— 比真上限多
    // 1353 额度，于是"照着界面写的那个数填"必然吃一个后端 400。
    assert.ok(
      retype(formatQyQuotaBound(ceiling)) <= ceiling,
      `界面念出来的上限 ${formatQyQuotaBound(ceiling)} 填回去越过了 ${ceiling}`
    )
    assert.equal(retype(formatQyQuotaBound(ceiling)), ceiling)
  })

  test('下限：推荐值念出来的那个数，填回去不许还是"预算太小"', () => {
    // ⌈50000 / count⌉ 不是 50 额度的整数倍时，4 位小数会把它抹到下限之下：
    // count=3 → 16667 印成 $0.0333 → 填回去 16650 → 16650 × 3 < 50000。
    const cap = 50_000
    for (const count of [1, 2, 3, 7, 9, 12, 14, 20]) {
      const floor = qyLotTierAmountFloor(cap, count)
      const retyped = retype(formatQyQuotaBound(floor))
      assert.equal(retyped, floor, `份数 ${count}：推荐值印出来填不回去`)
      assert.equal(
        qyLotTierBudgetShort(cap, count, retyped),
        false,
        `份数 ${count}：照着推荐值手打一遍，界面自己的红字又亮了`
      )
    }
  })

  test('整数金额仍旧短着印，不许平白拖出一串零', () => {
    // 边界值展示只在**需要**的时候加小数位。$10 印成 $10.000000 是纯噪声。
    assert.equal(formatQyQuotaBound(5_000_000), formatQyQuotaBound(5_000_000))
    assert.ok(!formatQyQuotaBound(5_000_000).includes('.'))
  })
})

describe('推荐值不许算出一个提交必被拒的数', () => {
  test('竞猜单注上限：夹在这一格真正能填的上界之内', () => {
    const ceiling = YAML.system_max_quota!
    // 参与费 > 上界 ÷ 20 时，单注额 × 20 会越过上界，后端 applyBetBounds 直接拒。
    const bigStake = 150_000_000
    assert.ok(bigStake * QY_LOT_BET_MAX_MULTIPLE > ceiling)
    assert.equal(qyLotRecommendedBetMax(bigStake, ceiling), ceiling)
    // 夹完之后仍然 ≥ 单注额，也就是仍然是一个可用的上限（不会反过来小于下限）。
    assert.ok(qyLotRecommendedBetMax(bigStake, ceiling) >= bigStake)
    // 够不着上界时倍数照旧是 20 —— 夹持不许改变正常场次的推荐值。
    assert.equal(
      qyLotRecommendedBetMax(500_000, ceiling),
      500_000 * QY_LOT_BET_MAX_MULTIPLE
    )
  })

  test('最低成场人数：保本人数够不到全场票数上界时不给推荐值', () => {
    // 参与费 $0.02、奖品总额 $2000：保本要 100000 人，而全场最多只能有
    // 50000 张票。填进去 = 一键造出一场必然流局的活动，而 min_entries_to_hold
    // 进承诺哈希、发布之后改不了。
    const draft = probDraft({
      draw_mode: 'rank',
      stake_quota: 10_000,
      // 不填全场上限 = 后端归一成硬上限，推荐值也必须按硬上限算。
      max_total_entries: 0,
      tiers: tiersOf(100_000_000, 10),
    })
    const cap = qyLotEntriesCap(draft, YAML.max_total_entries_hard)
    assert.equal(cap, 50_000)
    assert.equal(
      qyLotRecommendedMinEntries(draft, cap),
      0,
      '算得出来但达不到的成场线不是推荐值，是一场空开'
    )

    // 够得到的时候照常给：这条夹持不许把正常场次的推荐值也抹掉。
    const fine = probDraft({
      draw_mode: 'rank',
      stake_quota: 500_000,
      tiers: tiersOf(300_000, 7),
    })
    assert.equal(qyLotRecommendedMinEntries(fine, cap), 5)
  })
})
