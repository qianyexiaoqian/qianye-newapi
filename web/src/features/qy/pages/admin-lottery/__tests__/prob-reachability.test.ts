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
  qyLotDraftToInput,
  qyLotEffectiveAllowMultiWin,
  qyLotEmptyDraft,
  qyLotValidateDraft,
  type QyLotDraft,
} from '../lib/draft'

/**
 * 「概率制在界面上走不通」的守卫。
 *
 * 这是「找不到双色球」的同形缺陷，而且更隐蔽：概率制**选得到、填得完、四步
 * 全绿**，点确认必定吃一个 400 —— 后端对 prob 强制 `win_ppm ∈ (0, 1e6]`，
 * 而向导从来不发这个字段，界面上也没有任何一格可以用来填它。
 * typecheck 与全部单测在缺这一格时照样全绿（`win_ppm` 是可选字段），
 * 所以这里既按契约守（提交体里必须有它），也按源码守（表单里必须有那一格）。
 */

const wizard = readFileSync(
  join(
    dirname(fileURLToPath(import.meta.url)),
    '..',
    'components',
    'lottery-create-wizard.tsx'
  ),
  'utf8'
)

function probDraft(): QyLotDraft {
  const base = qyLotEmptyDraft(500)
  return {
    ...base,
    draw_mode: 'prob',
    title: '概率制',
    stake_quota: 1000,
    max_total_entries: 100,
    tiers: [
      { tier: 1, name: '一等奖', amount_quota: 5000, count: 1, win_ppm: 20000 },
      {
        tier: 2,
        name: '二等奖',
        amount_quota: 1000,
        count: 3,
        win_ppm: 100000,
      },
    ],
  }
}

describe('概率制的可达性', () => {
  test('提交体里必须带上每一档的 win_ppm', () => {
    const input = qyLotDraftToInput(probDraft())
    assert.deepEqual(
      input.prizes.map((prize) => prize.win_ppm),
      [20000, 100000],
      'win_ppm 没进请求体 = 概率制的每一次提交都会被后端 400 顶回来'
    )
  })

  test('rank 与 ball 一律发 0：后端对填了它的请求是直接 400，不是忽略', () => {
    const rank = qyLotDraftToInput({ ...probDraft(), draw_mode: 'rank' })
    assert.deepEqual(
      rank.prizes.map((prize) => prize.win_ppm),
      [0, 0]
    )
  })

  test('普通抽奖不许把双色球的三列带出去', () => {
    // 静默忽略的后果是运营从双色球切回普通抽奖后，以为占池比例还在生效。
    const rank = qyLotDraftToInput({
      ...probDraft(),
      draw_mode: 'rank',
      tiers: [
        {
          tier: 1,
          name: '一等奖',
          amount_quota: 5000,
          count: 1,
          red_match: 3,
          blue_match: 1,
          pool_share_bps: 5000,
        },
      ],
    })
    assert.deepEqual(
      [
        rank.prizes[0].red_match,
        rank.prizes[0].blue_match,
        rank.prizes[0].pool_share_bps,
      ],
      [0, 0, 0]
    )
  })

  test('概率没填、超 100%、预算不够摊都要在按钮之前说出来', () => {
    const yaml = { max_total_entries_hard: 50000 } as never

    const zero = qyLotValidateDraft(
      {
        ...probDraft(),
        tiers: [{ tier: 1, name: '一等奖', amount_quota: 5000, count: 1 }],
      },
      yaml,
      50_000_000,
      2000
    )
    assert.ok(zero.includes('qy_lot_v_win_ppm_range'))

    const over = qyLotValidateDraft(
      {
        ...probDraft(),
        tiers: [
          {
            tier: 1,
            name: 'a',
            amount_quota: 5000,
            count: 100,
            win_ppm: 600000,
          },
          {
            tier: 2,
            name: 'b',
            amount_quota: 5000,
            count: 100,
            win_ppm: 600000,
          },
        ],
      },
      yaml,
      50_000_000,
      2000
    )
    assert.ok(over.includes('qy_lot_v_win_ppm_sum'))

    // count × amount < 全场参与上限 → 超募时会有人被摊薄到 0 额度而拿不到钱。
    const thin = qyLotValidateDraft(
      {
        ...probDraft(),
        max_total_entries: 100,
        tiers: [
          { tier: 1, name: 'a', amount_quota: 10, count: 1, win_ppm: 1000 },
        ],
      },
      yaml,
      50_000_000,
      2000
    )
    assert.ok(thin.includes('qy_lot_v_prob_budget_short'))

    // 配对的正例：一份合法的概率制草稿必须能通过。
    assert.deepEqual(
      qyLotValidateDraft(probDraft(), yaml, 50_000_000, 2000),
      []
    )
  })

  test('表单里真的有那一格，而且只在概率制下出现', () => {
    assert.ok(
      wizard.includes("t('qy_lot_win_ppm_field')"),
      '概率制的每档概率没有输入框 = 这条路走不通'
    )
    assert.ok(wizard.includes('win_ppm:'), '输入框必须真的写回草稿')
    assert.ok(wizard.includes("draft.draw_mode === 'prob'"))
  })

  test('新建草稿的奖档自带 win_ppm 这一列', () => {
    // 缺了它，概率制下第一档永远是 undefined，而 `win_ppm` 是可选字段 ——
    // typecheck 不会报，提交时后端才 400。
    const [first] = qyLotEmptyDraft(500).tiers
    assert.equal(typeof first.win_ppm, 'number')
  })
})

describe('「允许多次中奖」显示的是生效值', () => {
  test('rank 之外的两个玩法恒为真', () => {
    const base = { ...qyLotEmptyDraft(500), allow_multi_win: false }
    assert.equal(
      qyLotEffectiveAllowMultiWin({ ...base, draw_mode: 'rank' }),
      false
    )
    assert.equal(
      qyLotEffectiveAllowMultiWin({ ...base, draw_mode: 'prob' }),
      true,
      '后端对 prob 无条件置真，而这个字段进 commit 原像 —— 复核屏显示草稿值就是撒谎'
    )
    assert.equal(
      qyLotEffectiveAllowMultiWin({ ...base, draw_mode: 'ball' }),
      true
    )
  })

  test('复核屏用的是生效值而不是草稿值', () => {
    assert.ok(wizard.includes('qyLotEffectiveAllowMultiWin'))
  })
})
