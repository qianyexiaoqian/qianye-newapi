/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
/*
 * 结算完的竞猜盘口不许继续显示**前瞻**赔率。
 *
 * # 被抓到的形状
 *
 * 实测活动 LT20260824-ded407dd8a8ab6c3（pool 3000、fee_bps 500、甲 1000、
 * 乙 2000，甲胜）：详情页在「已判定获胜」徽章旁边写着
 *
 *     选项甲 已判定获胜 33.3% $0.002 · 1 注 押中约得 $0.0038 ×1.90
 *
 * 而真正打进账户的是 2850（$0.0057 ×2.85，qy_lot_payout 有行、主库 +2850）。
 * 界面比实付**少 33%**；落选的「选项乙」同屏还挂着一个从未发生过的 ×1.27。
 *
 * 根因是赔率那一列没有任何状态判据：`qyLotGuessQuote` 无条件把"再押一注"
 * 加进 pool 与 winSum 再算，而结果公布之后那个问题已经不成立了。
 *
 * # 期望值在本文件里独立手算，不从被测代码回读
 *
 * 后端 `SplitPool`（qianye/modules/lottery/commit.go）：
 *   fee = trunc(pool × feeBps / 10000)，net = pool − fee，
 *   每笔 pay = trunc(net × amount / winSum)；
 *   winSum == pool（全场押中同一项）或无人押中 → 全额退回、手续费不收。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { formatQyQuotaLedger } from '../../../lib/format'
import {
  cleanupQyLotScreens,
  mountQyLotScreen,
  qyLotDetailFixture,
  zhKeys,
} from './screen-harness'

after(cleanupQyLotScreens)

const SLOW = { timeout: 120_000 }
const NOW = Math.floor(Date.now() / 1000)

/*
 * 与 Go 侧那场真打的账同一组数字，只把刻度放大到站内单注 $1：
 *
 *   甲 1_000_000（$2） · 乙 2_000_000（$4） → 奖池 3_000_000（$6）
 *   单注 500_000（$1）， 手续费 5%（fee_bps = 500）
 *
 * 甲胜之后**已经发生**的结算（不含"再押一注"）：
 *   fee    = trunc(3_000_000 × 500 / 10000) = 150_000
 *   net    = 2_850_000
 *   winSum = 1_000_000
 *   每 $1 押注 pay = trunc(2_850_000 × 500_000 / 1_000_000) = 1_425_000 → ×2.85
 *
 * 而**前瞻**赔率（改动前显示的那个数）会是：
 *   pool' = 3_500_000，winSum' = 1_500_000，fee' = 175_000，net' = 3_325_000
 *   pay'  = trunc(3_325_000 × 500_000 / 1_500_000) = 1_108_333 → ×2.22
 * 两者差 22%，所以这两条断言互相排斥，不可能同时成立。
 */
const SETTLED_PAYOUT = 1_425_000
const FORWARD_PAYOUT = 1_108_333

const SPEC = [
  {
    opt_no: 1,
    label: '会涨',
    is_catch_all: false,
    bet_quota: 1_000_000,
    bet_count: 2,
    is_winner: true,
  },
  {
    opt_no: 2,
    label: '不会涨',
    is_catch_all: true,
    bet_quota: 2_000_000,
    bet_count: 4,
    is_winner: false,
  },
]

function guessFixture(over: Record<string, unknown>) {
  return qyLotDetailFixture({
    kind: 'guess',
    title: '下个版本会不会涨价',
    intro: '',
    open_at: NOW - 7200,
    close_at: NOW - 3600,
    draw_at: NOW - 1800,
    stake_quota: 500_000,
    pool_quota: 3_000_000,
    fee_bps: 500,
    active_count: 6,
    min_entries_to_hold: 0,
    dedup_ip: false,
    spec: SPEC,
    ...over,
  })
}

const EMPTY_PROOF = {
  entries: [],
  total: 0,
  roster_hash: '',
  seed: '',
  chain_head: '',
  spec: SPEC,
  notice: '',
}

async function mountGuess(activity: unknown) {
  const { QyLotteryDetail } = await import('../detail')
  return mountQyLotScreen({
    path: '/qy/lottery/LA-1/',
    element: <QyLotteryDetail />,
    respond: (url) => {
      if (url.includes('/eligibility')) return { eligible: true, missing: [] }
      if (url.includes('/proof')) return EMPTY_PROOF
      if (url.includes('/lottery/activities/')) return activity
      return undefined
    },
  })
}

describe('结算完的竞猜：盘口写的是已经发出去的钱', () => {
  test('获胜选项写实付赔付，而不是"再押一注会拿回多少"', SLOW, async () => {
    const screen = await mountGuess(
      guessFixture({ status: 'finished', outcome: 'drawn', win_opt_no: 1 })
    )

    assert.ok(
      screen.text.includes(zhKeys['qy_lot_guess_paid']),
      `结算之后必须改口说「已按此赔付」：${screen.text}`
    )
    assert.ok(
      !screen.text.includes(zhKeys['qy_lot_guess_pays']),
      '结算之后还在写「押中约得」——那个问题已经不成立了'
    )
    assert.ok(
      screen.text.includes(formatQyQuotaLedger(SETTLED_PAYOUT)),
      `实付赔付 ${formatQyQuotaLedger(SETTLED_PAYOUT)} 不在屏幕上：${screen.text}`
    )
    assert.ok(
      screen.text.includes('×2.85'),
      `倍数必须是已经发生的 ×2.85：${screen.text}`
    )
    assert.ok(
      !screen.text.includes(formatQyQuotaLedger(FORWARD_PAYOUT)) &&
        !screen.text.includes('×2.22'),
      '前瞻赔率还印在结算完的盘口上：中奖者看到的数比到账少 22%'
    )
  })

  test('落选选项不许挂一个从未发生过的赔率', SLOW, async () => {
    const screen = await mountGuess(
      guessFixture({ status: 'finished', outcome: 'drawn', win_opt_no: 1 })
    )
    assert.ok(
      screen.text.includes(zhKeys['qy_lot_guess_lost']),
      `落选那一项必须明说未中：${screen.text}`
    )
    // ×1.27 那一类数字来自"如果现在往落选项再押一注"——它永远不会发生。
    assert.ok(
      !/×1\.\d\d/.test(screen.text),
      `落选项还印着一个赔率：${screen.text}`
    )
  })

  test('全额退回的结局三种选项一律写「原样退回」', SLOW, async () => {
    // 没人押中获胜选项（win_opt_no 指向一个零投注的选项）→ SplitPool 全额退回。
    const screen = await mountGuess(
      guessFixture({
        status: 'finished',
        outcome: 'void_no_winner',
        win_opt_no: 3,
        spec: SPEC.map((option) => ({ ...option, is_winner: false })),
      })
    )
    assert.ok(
      screen.text.includes(zhKeys['qy_lot_guess_refunded']),
      `流局那一场必须写「原样退回」：${screen.text}`
    )
    assert.ok(
      !screen.text.includes(zhKeys['qy_lot_guess_paid']) &&
        !screen.text.includes(zhKeys['qy_lot_guess_pays']),
      '一分钱都没赔出去的场次不该出现任何赔率'
    )
  })

  test(
    '还没开奖时仍然是前瞻赔率——这一改不能把开放期的盘口一起改掉',
    SLOW,
    async () => {
      const screen = await mountGuess(
        guessFixture({
          status: 'published',
          outcome: '',
          win_opt_no: 0,
          open_at: NOW - 3600,
          close_at: NOW + 3600,
          draw_at: NOW + 7200,
        })
      )
      assert.ok(
        screen.text.includes(zhKeys['qy_lot_guess_pays']),
        `开放期必须是「押中约得」：${screen.text}`
      )
      assert.ok(
        screen.text.includes(formatQyQuotaLedger(FORWARD_PAYOUT)),
        `开放期的前瞻赔率算错了：${screen.text}`
      )
      for (const key of ['qy_lot_guess_paid', 'qy_lot_guess_lost'] as const) {
        assert.ok(
          !screen.text.includes(zhKeys[key]),
          `开奖之前出现了 ${key}——结果还没出来`
        )
      }
    }
  )
})
