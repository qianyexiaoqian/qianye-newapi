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

import type { QyLotAdminActivity } from '../../types'
import { qyLotRecommendedMinEntries } from '../advice'
import {
  qyLotDraftForPlay,
  qyLotDraftFromActivity,
  qyLotDraftToInput,
  qyLotEmptyDraft,
  qyLotPlayOf,
  qyLotValidateDraft,
  qyLotWheelNonePpm,
  type QyLotDraft,
} from '../draft'

/**
 * 「转盘在向导里走得通」的守卫（design-15 §7.1）。
 *
 * 与概率制那条同形：转盘选得到、填得完、四步全绿之后，提交体里少任何一条
 * 归一化（`min_entries_to_hold` 必须 0、`win_ppm` 必须发、派生的「谢谢参与」行
 * 必须不发）都是后端一个 400，而界面上没有任何一格能用来修正它。
 * `win_ppm` 是可选字段，typecheck 对这几条全部不响。
 */

const wizard = readFileSync(
  join(
    dirname(fileURLToPath(import.meta.url)),
    '..',
    '..',
    'components',
    'lottery-create-wizard.tsx'
  ),
  'utf8'
)

function wheelDraft(): QyLotDraft {
  return {
    ...qyLotDraftForPlay(qyLotEmptyDraft(500), 'wheel'),
    title: '星屑转盘',
    stake_quota: 100,
    max_total_entries: 1_000,
    tiers: [
      { tier: 1, name: '头奖', amount_quota: 500, count: 2, win_ppm: 300_000 },
      {
        tier: 2,
        name: '二等奖',
        amount_quota: 50,
        count: 10,
        win_ppm: 200_000,
      },
    ],
  }
}

const YAML = {
  max_total_entries_hard: 50_000,
  reveal_delay_seconds: 0,
} as never

describe('转盘是第四张一级玩法卡', () => {
  test('切到转盘：draw_mode=wheel、期次清空、最低成场人数归 0', () => {
    const draft = qyLotDraftForPlay(
      { ...qyLotEmptyDraft(500), min_entries_to_hold: 20, series_no: 'S-1' },
      'wheel'
    )
    assert.equal(draft.kind, 'draw')
    assert.equal(draft.draw_mode, 'wheel')
    assert.equal(draft.series_no, '')
    assert.equal(draft.min_entries_to_hold, 0)
    assert.equal(qyLotPlayOf(draft), 'wheel')
  })

  test('从转盘切回抽奖时 draw_mode 换回 rank，不留一个 wheel 在表单看不见的地方', () => {
    const back = qyLotDraftForPlay(wheelDraft(), 'draw')
    assert.equal(back.draw_mode, 'rank')
    assert.equal(qyLotPlayOf(back), 'draw')
    assert.equal(qyLotDraftToInput(back).draw_mode, 'rank')
  })

  test('向导第一步里真的有那张卡，第二步里有初始库存与派生概率', () => {
    assert.ok(wizard.includes("t('qy_lot_play_wheel')"), '一级选择里缺少转盘')
    assert.ok(wizard.includes("t('qy_lot_wheel_stock_field')"))
    assert.ok(wizard.includes("t('qy_lot_wheel_none_ppm'"))
    // 双色球那条守卫的同款：转盘也不许退回二级下拉。
    assert.ok(!wizard.includes("value='wheel'"))
  })
})

describe('转盘的提交体', () => {
  test('min_entries_to_hold 恒发 0、win_ppm 原样发、双色球三列归 0', () => {
    const input = qyLotDraftToInput({ ...wheelDraft(), min_entries_to_hold: 7 })
    assert.equal(input.draw_mode, 'wheel')
    assert.equal(
      input.min_entries_to_hold,
      0,
      '转盘没有流局这回事，后端对非 0 直接 400'
    )
    assert.deepEqual(
      input.prizes.map((prize) => prize.win_ppm),
      [300_000, 200_000]
    )
    assert.deepEqual(
      input.prizes.map((prize) => [
        prize.red_match,
        prize.blue_match,
        prize.pool_share_bps,
      ]),
      [
        [0, 0, 0],
        [0, 0, 0],
      ]
    )
    assert.equal(input.series_no, '')
  })

  test('派生的「谢谢参与」行不进请求体，也不进编辑表单', () => {
    const draft = wheelDraft()
    draft.tiers.push({
      tier: 3,
      name: '谢谢参与',
      amount_quota: 0,
      count: 0,
      prize_type: 'none',
      win_ppm: 500_000,
    })
    assert.deepEqual(
      qyLotDraftToInput(draft).prizes.map((prize) => prize.tier),
      [1, 2],
      'none 行手填是 400：它由服务端按 1e6 − Σ 派生'
    )

    const activity = {
      kind: 'draw',
      draw_mode: 'wheel',
      rules_text: '{}',
      series_no: '',
      title: '星屑转盘',
      intro: '',
      stake_quota: 100,
      bet_min_quota: 0,
      bet_max_quota: 0,
      open_at: 1,
      close_at: 2,
      draw_at: 3,
      settle_deadline: 0,
      allow_multi_win: true,
      fee_bps: 0,
      min_entries_to_hold: 0,
      max_entries_per_user: 0,
      max_attempts_per_user: 0,
      max_total_entries: 1_000,
      max_total_users: 0,
      max_per_inviter: 0,
      cooldown_seconds: 0,
      dedup_ip: false,
    } as unknown as QyLotAdminActivity
    const edited = qyLotDraftFromActivity(activity, draft.tiers, [])
    assert.deepEqual(
      edited.tiers.map((tier) => tier.tier),
      [1, 2],
      '带回表单的 none 行既不能改也不能删，只会是一行假奖档'
    )
    assert.equal(qyLotPlayOf(edited), 'wheel')
  })
})

describe('转盘的校验', () => {
  test('每档概率必须落在 (0, 1e6]，各档之和不得超过 100%', () => {
    const zero = qyLotValidateDraft(
      {
        ...wheelDraft(),
        tiers: [{ tier: 1, name: '头奖', amount_quota: 500, count: 2 }],
      },
      YAML,
      0,
      2000
    )
    assert.ok(zero.includes('qy_lot_v_win_ppm_range'))

    const over = qyLotValidateDraft(
      {
        ...wheelDraft(),
        tiers: [
          { tier: 1, name: 'a', amount_quota: 5, count: 1, win_ppm: 600_000 },
          { tier: 2, name: 'b', amount_quota: 5, count: 1, win_ppm: 600_000 },
        ],
      },
      YAML,
      0,
      2000
    )
    assert.ok(over.includes('qy_lot_v_win_ppm_sum'))
  })

  test('转盘是硬库存：概率制那条「预算够摊」对它不成立', () => {
    // count × amount = 10 远小于全场参与上限 1000：概率制会拒，转盘必须放行。
    const thin = {
      ...wheelDraft(),
      tiers: [
        { tier: 1, name: '小奖', amount_quota: 10, count: 1, win_ppm: 100_000 },
      ],
    }
    assert.deepEqual(qyLotValidateDraft(thin, YAML, 0, 2000), [])
    assert.ok(
      qyLotValidateDraft(
        { ...thin, draw_mode: 'prob' },
        YAML,
        0,
        2000
      ).includes('qy_lot_v_prob_budget_short'),
      '配对的反例：同一份草稿在概率制下应当被那条判据拒绝'
    )
  })

  test('「谢谢参与」= 1e6 − Σ，越过 100% 时为 0；转盘不推荐最低成场人数', () => {
    assert.equal(qyLotWheelNonePpm(wheelDraft()), 500_000)
    assert.equal(
      qyLotWheelNonePpm({
        ...wheelDraft(),
        tiers: [
          { tier: 1, name: 'a', amount_quota: 5, count: 1, win_ppm: 700_000 },
          { tier: 2, name: 'b', amount_quota: 5, count: 1, win_ppm: 700_000 },
        ],
      }),
      0
    )
    assert.equal(qyLotRecommendedMinEntries(wheelDraft(), 1_000), 0)
  })
})
