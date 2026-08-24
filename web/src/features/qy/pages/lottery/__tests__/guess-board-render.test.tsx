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
 * 「竞猜看起来像选择题」这件事的验收。
 *
 * # 被投诉的那个形状
 *
 * 项目方原话：「竞猜功能,怎么感觉就像是选择题一样?这不是给人送钱吗??」
 * 后端从来不可能倒贴（`SplitPool` 断言 Σpay + fee == pool，Go 侧
 * `guess_pool_e2e_db_test.go` 真打了一场核对到主库额度），问题全在呈现：
 * 改造前的详情页是一张三列表格（选项 / 该选项投注额 / 投注人次）加两句话，
 * 参与弹窗是一组单选按钮加一句「已有 N 人投注」。整屏没有任何一处告诉用户
 * 钱从押错的人那里来、押的人越多每份越少。
 *
 * # 这份测试守四件事
 *
 * 1. **分布看得见**：每个选项的占池百分比是可见文字（不是只挂在条子的
 *    CSS 宽度上，那读屏与页内搜索都读不到）。
 * 2. **赔率看得见，而且等于真会到账的那个数**：期望值在本文件里按后端
 *    `SplitPool` 的口径独立手算，不从被测组件回读。
 * 3. **押注之前**就看得到手续费与「全对或全错原样退回」——弹窗上也要有，
 *    那是钱真正动之前的最后一屏。
 * 4. **字数不许反弹**。上一轮刚把详情页从 1548 字压到 750，这一轮加的是
 *    数字与条子，不是说明文字。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import enKeys from '@/i18n/qy/en.json'

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
 * 这一场的数字是**挑过**的，为的是让期望赔率落在一个整齐的金额上，
 * 断言里就不会出现"看起来差不多"的容差比较。站内口径 500000 quota = $1。
 *
 *   已有押注   会涨 $2（1_000_000） · 不会涨 $7（3_500_000） → 奖池 $9
 *   单注       $1（500_000）
 *   手续费     10%（fee_bps = 1000）
 *
 * 押「会涨」这一注之后：
 *   pool   = 4_500_000 + 500_000 = 5_000_000
 *   fee    = trunc(5_000_000 × 1000 / 10000) = 500_000
 *   net    = 4_500_000
 *   winSum = 1_000_000 + 500_000 = 1_500_000
 *   pay    = trunc(4_500_000 × 500_000 / 1_500_000) = 1_500_000  → $3.00 · ×3.00
 *
 * 押「不会涨」：
 *   winSum = 3_500_000 + 500_000 = 4_000_000
 *   pay    = trunc(4_500_000 × 500_000 / 4_000_000) = 562_500     → ×1.13
 *
 * 占池：会涨 1_000_000 / 4_500_000 = 22.2%，不会涨 3_500_000 / 4_500_000 = 77.8%。
 */
const THIN_PAYOUT = 1_500_000
const CROWDED_PAYOUT = 562_500

const GUESS_SPEC = [
  {
    opt_no: 1,
    label: '会涨',
    is_catch_all: false,
    bet_quota: 1_000_000,
    bet_count: 2,
  },
  {
    opt_no: 2,
    label: '不会涨',
    is_catch_all: true,
    bet_quota: 3_500_000,
    bet_count: 7,
  },
]

const OPEN_GUESS = qyLotDetailFixture({
  kind: 'guess',
  title: '下个版本会不会涨价',
  intro: '',
  open_at: NOW - 3600,
  close_at: NOW + 3600,
  draw_at: NOW + 7200,
  stake_quota: 500_000,
  pool_quota: 4_500_000,
  fee_bps: 1000,
  active_count: 9,
  min_entries_to_hold: 0,
  dedup_ip: false,
  spec: GUESS_SPEC,
})

/** 一场还没有任何人下注的竞猜：赔率必须退化成「暂无对手盘」。 */
const EMPTY_GUESS = qyLotDetailFixture({
  ...OPEN_GUESS,
  pool_quota: 0,
  active_count: 0,
  spec: GUESS_SPEC.map((option) => ({
    ...option,
    bet_quota: 0,
    bet_count: 0,
  })),
})

const EMPTY_PROOF = {
  entries: [],
  total: 0,
  roster_hash: '',
  seed: '',
  chain_head: '',
  spec: GUESS_SPEC,
  notice: '',
}

/**
 * 只读**弹窗那一块**的可见文字。
 *
 * 整屏文本里找关键词会被页面别处的同一个词蒙混过去 —— 详情页的统计格里
 * 就有一个「奖池 $9」，于是"弹窗里显示了奖池"这条断言即使把弹窗那一行整个
 * 删掉也照样绿（实测 MF10 13 pass / 0 fail，存活）。这与本仓在
 * `text-budget.test.tsx` 里栽过的那次是同一个形状。
 */
function readQyLotDialog(): string {
  const dialog = document.body.querySelector('[role="dialog"]')
  return (dialog?.textContent ?? '').replace(/\s+/g, ' ')
}

async function mountGuessDetail(activity: unknown) {
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

/* ── 详情页盘口 ───────────────────────────────────────────────────── */

describe('竞猜详情：一屏之内看得出这是彩池而不是选择题', () => {
  test('每个选项的占池比例是可见文字', SLOW, async () => {
    const screen = await mountGuessDetail(OPEN_GUESS)
    for (const [label, pct] of [
      ['会涨', '22.2%'],
      ['不会涨', '77.8%'],
    ] as const) {
      assert.ok(
        screen.text.includes(pct),
        `选项「${label}」的占池比例 ${pct} 没有写成文字 —— ` +
          '只画一根条子的话，读屏与页内搜索都读不到它'
      )
    }
  })

  test('赔率等于按后端口径独立算出的那个数', SLOW, async () => {
    const screen = await mountGuessDetail(OPEN_GUESS)
    const thin = formatQyQuotaLedger(THIN_PAYOUT)
    const crowded = formatQyQuotaLedger(CROWDED_PAYOUT)
    assert.ok(
      screen.text.includes(thin),
      `押「会涨」应当约得 ${thin}（pool 5000000 − fee 500000，按 winSum 1500000 分）` +
        `，页面上没有：${screen.text}`
    )
    assert.ok(
      screen.text.includes(crowded),
      `押「不会涨」应当约得 ${crowded}，页面上没有`
    )
    assert.ok(
      screen.text.includes('×3.00') && screen.text.includes('×1.13'),
      '两个倍数至少要有一个在页面上 —— 倍数才是"押人多的那边赔得少"的直接证据'
    )
  })

  test('押注更集中的一边赔得更少，这件事在页面上摆得出来', SLOW, async () => {
    const screen = await mountGuessDetail(OPEN_GUESS)
    // 77.8% 那一项的倍数必须小于 22.2% 那一项的。两个数都在页面上，
    // 于是这条不需要用户去算 —— 这正是分布条替掉三列表格的全部理由。
    const crowdedAt = screen.text.indexOf('×1.13')
    const thinAt = screen.text.indexOf('×3.00')
    assert.ok(crowdedAt >= 0 && thinAt >= 0, '两个倍数没有同时渲染')
    assert.ok(
      thinAt < crowdedAt,
      '顺序按 opt_no：1 号（会涨，人少、×3.00）应当排在 2 号之前'
    )
  })

  test('手续费与「全对或全错原样退回」在下注之前就写着', SLOW, async () => {
    const screen = await mountGuessDetail(OPEN_GUESS)
    const line = zhKeys['qy_lot_guess_pool_desc'].replace(
      '{{percent}}',
      '10.00'
    )
    assert.ok(
      screen.text.includes(line),
      `彩池口径那一句不在页面上（期望「${line}」）—— ` +
        '手续费与退款规则事后才说，无论怎么处理都会被指控临时改规则'
    )
  })

  test('还没有人下注时说「暂无对手盘」，而不是画一个 ×1.00', SLOW, async () => {
    const screen = await mountGuessDetail(EMPTY_GUESS)
    assert.ok(
      screen.text.includes(zhKeys['qy_lot_guess_no_counterparty']),
      '空盘口没有说清"没有输家就没有奖金"'
    )
    assert.ok(
      !screen.text.includes('×1.00'),
      '×1.00 读起来像"稳赚不赔的一倍"，而真实语义是这一场还不成立'
    )
  })

  test('盘口那一块的字数不许反弹', SLOW, async () => {
    const screen = await mountGuessDetail(OPEN_GUESS)
    // 改造前同一份夹具（三列表格 + 手续费一句 + 全错一句）实测 336 字。
    // 现在是分布条 + 百分比 + 赔率 + 一句口径说明。上限留 400 给更长的
    // 选项文案，而不是给说明文字：再加一段解释就会越界。
    assert.ok(
      screen.chars <= 400,
      `竞猜详情一屏 ${screen.chars} 字，超过 400：${screen.text}`
    )
  })

  test('「选项」这个词换成了盘口，而不是只换了皮', SLOW, async () => {
    const screen = await mountGuessDetail(OPEN_GUESS)
    assert.ok(
      screen.text.includes(zhKeys['qy_lot_options_title']),
      '盘口标题没渲染'
    )
    assert.equal(
      zhKeys['qy_lot_options_title'],
      '押注盘口',
      '标题退回成「竞猜选项」了 —— 一排带标题「选项」的按钮就是一道选择题'
    )
  })
})

/* ── 参与弹窗 ─────────────────────────────────────────────────────── */

describe('押注弹窗：钱动之前的最后一屏也要有分布与赔率', () => {
  test('弹窗里奖池、分布、赔率、手续费四样都在', SLOW, async () => {
    const screen = await mountGuessDetail(OPEN_GUESS)
    const before = screen.chars
    const opened = await screen.click(zhKeys['qy_lot_join_title'])
    assert.ok(opened, '「参与活动」按钮点不开')

    const after = screen.read()
    const dialogChars = after.chars - before
    // 弹窗净增的都是数字：奖池一行、两行占比 + 赔率、一句口径说明。
    assert.ok(
      dialogChars <= 160,
      `押注弹窗净增 ${dialogChars} 字，超过 160：${after.text}`
    )

    const dialog = readQyLotDialog()
    assert.notEqual(dialog, '', '弹窗没有渲染出来')
    for (const [why, piece] of [
      ['奖池就是"赢家分的是谁的钱"的答案', formatQyQuotaLedger(4_500_000)],
      ['人少那一边的赔率', formatQyQuotaLedger(THIN_PAYOUT)],
      ['人多那一边的占比', '77.8%'],
      [
        '押错的钱归了押中的人 + 全对/全错原样退回',
        zhKeys['qy_lot_bet_warn_line'].replace('{{amount}}', '$1'),
      ],
    ] as const) {
      assert.ok(dialog.includes(piece), `弹窗里少了${why}：${piece}`)
    }
  })

  test('竞猜不许套用抽奖那句退款口径', SLOW, async () => {
    /*
      「只有整场取消或流局时才全额退款」对竞猜是**错的**：全场押中同一项、
      或全场都押错时同样原样退回，而那两种既不是取消也不是流局。用同一句话
      盖住两类活动，等于在钱真正动之前的最后一屏给竞猜用户一个假的退款口径。
    */
    const screen = await mountGuessDetail(OPEN_GUESS)
    assert.ok(await screen.click(zhKeys['qy_lot_join_title']))
    assert.ok(
      !readQyLotDialog().includes(
        zhKeys['qy_lot_join_warn_line'].replace('{{amount}}', '$1')
      ),
      '竞猜弹窗上挂着抽奖那句「只有整场取消或流局时才全额退款」'
    )
  })

  test('标签是「押哪一项」而不是「选择你的答案」', SLOW, async () => {
    const screen = await mountGuessDetail(OPEN_GUESS)
    assert.ok(await screen.click(zhKeys['qy_lot_join_title']))
    const dialog = readQyLotDialog()
    assert.ok(
      dialog.includes(zhKeys['qy_lot_pick_option']),
      '选项分组的标签没渲染'
    )
    assert.equal(
      zhKeys['qy_lot_pick_option'],
      '押哪一项',
      '标签退回成「选择你的答案」了 —— 写着"答案"的单选组就是一道选择题'
    )
    assert.ok(!dialog.includes('选择你的答案'), '旧文案还在弹窗上')
  })
})

/* ── 语言包 ───────────────────────────────────────────────────────── */

describe('盘口改造没有在语言包里留下孤儿键', () => {
  const RETIRED = [
    // 被 qy_lot_guess_pool_desc 一句话合并掉的两段
    'qy_lot_fee_desc',
    'qy_lot_no_winner_note',
    // 弹窗里的「已有 N 人投注」——它被占比 + 注数 + 赔率整个取代
    'qy_lot_option_pool',
  ]

  test('两份语言包里都没有它们', () => {
    const leftover = RETIRED.filter(
      (key) => key in zhKeys || key in (enKeys as Record<string, string>)
    )
    assert.deepEqual(leftover, [], `语言包里留下了孤儿键: ${leftover}`)
  })

  test('顶替它们的新键两份语言包里都有', () => {
    const added = [
      'qy_lot_guess_pool_desc',
      'qy_lot_guess_pays',
      'qy_lot_guess_bets',
      'qy_lot_guess_no_counterparty',
      'qy_lot_bet_warn_line',
    ]
    const missing = added.filter(
      (key) => !(key in zhKeys) || !(key in (enKeys as Record<string, string>))
    )
    assert.deepEqual(missing, [], `新键没落全: ${missing}`)
  })

  test('管理端还在用的两个表头没有被顺手删掉', () => {
    // 用户端的三列表格换成盘口之后，这两个键在用户端确实零引用了 ——
    // 但管理端活动详情仍然用同一张 QyLotSpecTable 渲染选项，删掉它们
    // 会让运营那一屏出现两个裸键。
    for (const key of [
      'qy_lot_option_bet_quota',
      'qy_lot_option_bet_count',
    ] as const) {
      assert.ok(key in zhKeys && key in (enKeys as Record<string, string>), key)
    }
  })
})
