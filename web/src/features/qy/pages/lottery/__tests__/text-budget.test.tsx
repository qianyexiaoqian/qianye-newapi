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
 * 用户端抽奖竞猜「一屏上有多少字」的上限，以及"精简掉的是解释、不是决策依据"。
 *
 * # 被投诉的那个形状
 *
 * 项目方原话：「用户端的抽奖竞猜，UI 展现，字太多了，你换位思考一下，你是用户
 * 你是否有耐心看完这一大堆字？」实测（同一套夹具、同一批数据）改造前：
 *
 *   抽奖大厅 3 张卡 195 字 · 竞猜大厅 1 张卡 91 字 · 活动详情(开放中) 486 字
 *   活动详情(已结束·双色球) 1548 字 · 我的参与 2 行 268 字
 *
 * 一场已结束的双色球，用户要在 1548 个字里找"我中了没有"。
 *
 * # 这份测试守两件互相拉扯的事
 *
 * 1. **字数上限**。折叠位（`QyLotFinePrint`）用的是 Base UI 的 Collapsible，
 *    收起时面板**不挂载** —— 所以"折起来了"这件事可以直接从 `textContent`
 *    上量出来。把折叠拆掉、或者往一屏上再堆一段说明，这里就红。
 * 2. **决策必需的东西一个都不许少**。光有上限的话，把「参与费」那一行删掉
 *    也能让字数变好看 —— 那正是精简最容易走歪的方向。所以每一屏都逐条断言
 *    用户拿来决定"要不要花这笔钱"的量还在：多少钱、能赢多少、还剩多久、
 *    多少人参加了、什么情况下退款。
 * 3. **折起来的必须能打开**。断言收起时不在 DOM 里之后，还要真的去点那颗
 *    触发器，确认原文一字不差地回来了 —— 否则"折叠"与"删掉"在测试眼里一样。
 *
 * 期望值一律在本文件里独立写出，或者从 `src/i18n/qy/zh.json` 取原文，
 * 不从被测组件回读。
 *
 * 变异实验见文件末尾。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import enKeys from '@/i18n/qy/en.json'

import {
  cleanupQyLotScreens,
  mountQyLotScreen,
  qyLotBriefFixture,
  qyLotDetailFixture,
  zhKeys,
} from './screen-harness'

after(cleanupQyLotScreens)

/** React `act` 的异步等待让单个用例超过 bun 默认的 5 秒。 */
const SLOW = { timeout: 120_000 }

const NOW = Math.floor(Date.now() / 1000)
const OPEN = { open_at: NOW - 3600, close_at: NOW + 3600, draw_at: NOW + 7200 }

const BALL = {
  draw_mode: 'ball' as const,
  issue_no: 12,
  series_no: 'SSQ-2026',
  pool_open_quota: 12_000_000,
  ball_red_pool: 33,
  ball_red_pick: 6,
  ball_blue_pool: 16,
  ball_blue_pick: 1,
}

/** 一档浮动奖 + 一档固定奖：奖金形态那一列的两种写法都要渲染到。 */
const BALL_SPEC = [
  {
    tier: 1,
    name: '一等奖',
    amount_quota: 0,
    count: 0,
    win_ppm: 0,
    red_match: 6,
    blue_match: 1,
    pool_share_bps: 5000,
  },
  {
    tier: 2,
    name: '二等奖',
    amount_quota: 2_000_000,
    count: 3,
    win_ppm: 0,
    red_match: 6,
    blue_match: 0,
  },
]

const HALL_ITEMS = [
  qyLotBriefFixture({ act_no: 'LA-1', title: '春季回馈抽奖', ...OPEN }),
  qyLotBriefFixture({
    act_no: 'LA-2',
    title: '第 12 期双色球',
    ...OPEN,
    ...BALL,
  }),
  qyLotBriefFixture({
    act_no: 'LA-3',
    title: '已经买过两张的那场',
    my_entry_count: 2,
    ...OPEN,
  }),
]

async function mountHall(kind: 'draw' | 'guess', width?: number) {
  const module =
    kind === 'draw'
      ? await import('../index')
      : await import('../../lottery-guess')
  const Body =
    'QyLotteryDrawBody' in module
      ? module.QyLotteryDrawBody
      : module.QyLotteryGuessBody
  return mountQyLotScreen({
    width,
    element: (
      <Body
        page={1}
        scope='live'
        onPageChange={() => {}}
        onScopeChange={() => {}}
      />
    ),
    respond: (url) =>
      url.includes('/lottery/activities')
        ? { items: HALL_ITEMS, total: HALL_ITEMS.length, p: 1, page_size: 12 }
        : undefined,
  })
}

async function mountDetail(
  activity: unknown,
  proof: unknown,
  width?: number
): Promise<Awaited<ReturnType<typeof mountQyLotScreen>>> {
  const { QyLotteryDetail } = await import('../detail')
  return mountQyLotScreen({
    width,
    path: '/qy/lottery/LA-1/',
    element: <QyLotteryDetail />,
    respond: (url) => {
      if (url.includes('/eligibility')) return { eligible: true, missing: [] }
      if (url.includes('/proof')) return proof
      if (url.includes('/lottery/activities/')) return activity
      return undefined
    },
  })
}

/**
 * 已结束的双色球，而且**我买过两张**（一张中、一张没中）。
 *
 * 带上 `my_tickets` 才是这一屏真实的样子：项目方投诉的正是这一屏
 * （「已开奖的抽奖为什么不显示双色球号码」），而改造前它连我的号都没有。
 * 用一个没有票的夹具来量字数，量的是一个用户永远看不到的版本。
 */
const FINISHED_BALL = qyLotDetailFixture({
  ...BALL,
  status: 'finished',
  outcome: 'drawn',
  title: '第 12 期双色球',
  spec: BALL_SPEC,
  ball_result: '03,09,12,17,22,30|05',
  my_entry_count: 2,
  my_tickets: [
    {
      entry_no: 'LE-WIN',
      seq: 1,
      pick: '03,09,12,17,22,31|05',
      status: 'success',
      amount: 500000,
      won_kind: 'prize',
      won_tier: 2,
      won_amount: 2_000_000,
    },
    {
      entry_no: 'LE-MISS',
      seq: 2,
      pick: '01,02,04,05,06,07|01',
      status: 'success',
      amount: 500000,
      won_kind: '',
      won_tier: 0,
      won_amount: 0,
    },
  ],
})

/** 平台自陈的证据边界。它由后端下发，所以在这里是一段独立的假数据。 */
const PROOF_NOTICE = '本份证据不能证明种子是真随机的，也不能把标识还原成真人。'

/*
  公开名单一页真的渲染 20 行（lottery-roster-card 的 PAGE_SIZE），而真实的
  `entry_no` 是 27 字、`user_ref` 是 32 字十六进制 —— 每行成本约 80 字。

  夹具此前只有 1 条**玩具标识**的名单（E-1 / u-abc，3+6 字），于是
  「活动详情 ≤1000 字」那道守卫只在"恰好一个人参加、而且标识短得不真实"
  这种场次上成立：实测真实标识下第 2 个参与者就把它顶穿，满页 20 行是 2507 字。
  换成真实规模之后这道守卫才真的在量东西 —— 它现在能过，靠的是名单表被折起来。
*/
const ROSTER_ENTRIES = Array.from({ length: 20 }, (_, i) => ({
  entry_no: `LE20260824-${(i + 1).toString(16).padStart(16, '0')}`,
  seq: i + 1,
  user_ref: (i + 1).toString(16).padStart(32, '0'),
  opt_no: 0,
  amount: 500000,
  status: 'confirmed',
  pick: '01,02,03,04,05,06|07',
}))

const FINISHED_BALL_PROOF = {
  algo: 'lot-v2',
  act_no: 'LA-1',
  entries: ROSTER_ENTRIES,
  total: ROSTER_ENTRIES.length,
  roster_hash: 'c'.repeat(64),
  roster_count: ROSTER_ENTRIES.length,
  seed: 'd'.repeat(64),
  commit_hash: 'a'.repeat(64),
  chain_head: 'e'.repeat(64),
  spec: BALL_SPEC,
  locked_at: NOW - 600,
  revealed_at: NOW - 300,
  settled_at: NOW - 100,
  notice: PROOF_NOTICE,
  ...BALL,
}

/* ── 大厅 ─────────────────────────────────────────────────────────── */

describe('大厅首屏：先看到活动，不是先看到一段免责声明', () => {
  test('抽奖大厅三张卡的可见文字不超过 220 字', SLOW, async () => {
    const hall = await mountHall('draw')
    // 改造前 195（含顶部那块「参与费不退」横幅）。上限留在 220，是为了给
    // 卡片上真正的内容（更长的活动标题、更大的金额）留出余量，而不是给
    // 说明文字留余量：再加一段解释就会越界。
    assert.ok(
      hall.chars <= 220,
      `抽奖大厅一屏 ${hall.chars} 字，超过 220：${hall.text}`
    )
  })

  test('四个决策量在每张卡上都看得到', SLOW, async () => {
    const hall = await mountHall('draw')
    const cards = Array.from(
      hall.container.querySelectorAll('[data-slot="card"]')
    )
    assert.equal(cards.length, 3, '三张卡没有全部渲染出来')

    /*
      **逐张卡**断言，不是在整屏文本里找关键词。

      这一条最初写成 `hall.text.includes('参与费')`，而分段栏上那枚风险徽章写的
      正是「参与费不退」—— 于是把卡片上那一整行参与费删掉之后，测试仍然全绿
      （实测那次变异 15 pass / 0 fail，存活）。在一屏文本里找一个四处都出现的
      词，测的是"这个词在页面上有没有"，不是"每张卡上有没有"。
    */
    const expectations: [string, string][] = [
      ['要花多少', zhKeys['qy_lot_stake']],
      ['能赢多少', zhKeys['qy_lot_pool']],
      ['多少人参加了', zhKeys['qy_lot_entries_count']],
      ['还剩多久', zhKeys['qy_lot_countdown_close']],
    ]
    for (const [index, card] of cards.entries()) {
      const text = (card.textContent ?? '').replace(/\s+/g, ' ')
      for (const [why, label] of expectations) {
        assert.ok(
          text.includes(label),
          `第 ${index + 1} 张卡上没有「${label}」（${why}）：${text}`
        )
      }
    }

    // 三张卡各自的标题都在：上面那几条不能靠"渲染了三个空壳"蒙混过关。
    for (const title of [
      '春季回馈抽奖',
      '第 12 期双色球',
      '已经买过两张的那场',
    ]) {
      assert.ok(hall.text.includes(title), `卡片「${title}」没渲染`)
    }
    // 金额本身也要出现，而不只是标签。`stake_quota` 是 500000，站内口径 $1。
    const withStake = cards.filter((card) =>
      (card.textContent ?? '').includes('$1')
    )
    assert.equal(withStake.length, 3, '有卡片只写了标签没写金额')
  })

  test('风险提示仍在，且两张标签各说各的', SLOW, async () => {
    const draw = await mountHall('draw')
    assert.ok(
      draw.text.includes(zhKeys['qy_lot_risk_badge_stake_lost']),
      '抽奖大厅丢了「参与费不退」——两类活动的代价差一个量级，这句话不能没有'
    )
    const guess = await mountHall('guess')
    assert.ok(
      guess.text.includes(zhKeys['qy_lot_risk_badge_may_lose_principal']),
      '竞猜大厅丢了「猜错会亏本金」'
    )
    assert.ok(
      !guess.text.includes(zhKeys['qy_lot_risk_badge_stake_lost']),
      '竞猜大厅挂上了抽奖那句风险提示：两者的最坏结果不是一回事'
    )
  })

  test('没参加过的卡不写「尚未参与」，参加过的写次数', SLOW, async () => {
    const hall = await mountHall('draw')
    // 「尚未参与」是零信息量的一句话，而大厅里绝大多数卡都是这个状态，
    // 于是每张卡上都挂着同一个四字标签，真正有内容的那张反而淹没在里面。
    assert.ok(
      !hall.text.includes('尚未参与'),
      '卡片上又出现了「尚未参与」这种零信息量的占位文字'
    )
    assert.ok(
      hall.text.includes('已参与 2 次'),
      '已经买过两张的那场没写出次数：重复下单正是这里要防的错'
    )
  })
})

/* ── 活动详情 ─────────────────────────────────────────────────────── */

describe('活动详情：决策的留在明面上，解释的折起来', () => {
  test('已结束的双色球详情不超过 1000 字', SLOW, async () => {
    const detail = await mountDetail(FINISHED_BALL, FINISHED_BALL_PROOF)
    /*
      改造前 1548 → 精简到 750（那一版的夹具里没有"我的票"）→ 现在带上
      「本期开奖 · 我中了没有」那张卡。

      现在是 861（名单换成真实规模的 20 条之后仍然是这个数——它们被折起来了；
      不折的话同一屏 2507 字）。

      上限**有意从 850 抬到 1000**，抬的是这一块：两组号各七颗球（球本身就是
      可见字符）、每张票的规范化串、命中几红几蓝、中了哪一档赔多少 /「未中奖」，
      以及期次的结转与下一期。它们全部属于"用户不看就不知道自己中没中"的那一类
      —— 而这一屏被投诉的原因恰恰是这句话答不出来，不是字太多。

      抬上限最容易变成一张遮羞布，所以下面那条「决策必需的数字全都在明面上」
      同步加了逐条断言：把新加的这些删掉能让字数变好看，但那条会当场红。
    */
    assert.ok(
      detail.chars <= 1000,
      `活动详情一屏 ${detail.chars} 字，超过 1000：${detail.text}`
    )
  })

  test('花钱与拿钱要用到的数字全都在明面上', SLOW, async () => {
    const detail = await mountDetail(FINISHED_BALL, FINISHED_BALL_PROOF)
    const expected = [
      // 开奖号：点进来第一件事就是"开的是哪几个号"。
      '03,09,12,17,22,30|05',
      // 我的号 —— 与开奖号在同一屏。少了它"我中了没有"就答不出来，
      // 而那正是项目方投诉这一屏的原因。
      '03,09,12,17,22,31|05',
      '01,02,04,05,06,07|01',
      // 命中了几红几蓝、中的是哪一档、这一档赔了多少。
      '红球 5 个、蓝球 1 个',
      '第 2 档',
      // 没中也要**明说**，不能只是不显示。
      zhKeys['qy_lot_ball_not_won'],
      // 每一档的命中门槛与中奖概率。
      '需红 6 蓝 1',
      '需红 6 蓝 0',
      // 摊薄这件事的结论。它决定用户愿不愿意花这笔钱。
      zhKeys['qy_lot_ball_split_note'],
      // 号池 = 中奖难度。
      '红球 33 选 6',
      // 流局退款口径。
      '不足 20 份将流局并全额退款',
    ]
    for (const piece of expected) {
      assert.ok(detail.text.includes(piece), `详情页少了决策必需的「${piece}」`)
    }
  })

  test('解释性的段落默认不占位置', SLOW, async () => {
    const detail = await mountDetail(FINISHED_BALL, FINISHED_BALL_PROOF)
    // 这四段都是"它为什么可信"而不是"我要不要花这笔钱"。收起时 Base UI 的
    // Collapsible 不挂载面板，所以它们真的不在 DOM 里 —— 屏幕阅读器与页内
    // 搜索也读不到，而不是靠 CSS 藏起来。
    const folded = [
      zhKeys['qy_lot_ball_result_verify_note'],
      zhKeys['qy_lot_dedup_ip_note'],
      zhKeys['qy_lot_vf_local_note'],
      PROOF_NOTICE,
      'a'.repeat(64),
    ]
    for (const piece of folded) {
      assert.ok(
        !detail.text.includes(piece),
        `这一段本该默认收起，却出现在首屏：${piece.slice(0, 30)}`
      )
    }
  })

  test('公开名单默认折起来，点开之后 20 条一个不少', SLOW, async () => {
    const detail = await mountDetail(FINISHED_BALL, FINISHED_BALL_PROOF)
    const first = ROSTER_ENTRIES[0]!
    const last = ROSTER_ENTRIES[ROSTER_ENTRIES.length - 1]!

    // 折起来时那 20 行真的不在 DOM 里 —— 这就是详情页字数不再随参与人数
    // 线性增长的原因。卡片标题与「共 N 条」仍在明面上，所以它没有"消失"。
    assert.ok(
      !detail.text.includes(first.entry_no),
      '名单摊在首屏上了：真实标识下每行约 80 字，满页 20 行就是 2500 字'
    )
    assert.ok(
      detail.text.includes(zhKeys['qy_lot_roster_title']),
      '名单卡的标题不能跟着一起折掉，否则用户不知道有这么一份东西'
    )

    const opened = await detail.click(
      `展开全场名单(共 ${ROSTER_ENTRIES.length} 条)`
    )
    assert.ok(opened, '找不到「展开全场名单」那颗折叠触发器')
    const after = detail.read()
    for (const row of [first, last]) {
      assert.ok(
        after.text.includes(row.entry_no) && after.text.includes(row.user_ref),
        `展开之后名单里少了 ${row.entry_no} —— 折叠把它折没了`
      )
    }
  })

  test('点开「证据摘要」，四串哈希一个不少地回来', SLOW, async () => {
    const detail = await mountDetail(FINISHED_BALL, FINISHED_BALL_PROOF)
    const opened = await detail.click(zhKeys['qy_lot_evidence_digest'])
    assert.ok(opened, '找不到「证据摘要」那颗折叠触发器')

    const after = detail.read()
    for (const [name, hash] of [
      ['承诺哈希', 'a'.repeat(64)],
      ['名单哈希', 'c'.repeat(64)],
      ['随机种子', 'd'.repeat(64)],
      ['链尾哈希', 'e'.repeat(64)],
    ] as const) {
      assert.ok(
        after.text.includes(hash),
        `展开之后 ${name} 仍然不在页面上 —— 折叠把它折没了`
      )
    }
    // 展开确实是"多出来一屏"，而不是替换掉了别的内容。
    assert.ok(after.chars > detail.chars - 1, '展开之后可见文字反而变少了')
  })

  test('点开「这份证据证明了什么」，平台自陈的边界回来', SLOW, async () => {
    const detail = await mountDetail(FINISHED_BALL, FINISHED_BALL_PROOF)
    const opened = await detail.click(zhKeys['qy_lot_vf_scope_label'])
    assert.ok(opened, '找不到「这份证据证明了什么」那颗折叠触发器')

    const after = detail.read()
    assert.ok(
      after.text.includes(zhKeys['qy_lot_vf_local_note']),
      '「复算全在你的浏览器里」这句话展开后仍然没有'
    )
    assert.ok(
      after.text.includes(PROOF_NOTICE),
      '后端随证据链下发的 notice 展开后仍然没有：它是协议的一部分，不能丢'
    )
  })
})

/* ── 参与弹窗 ─────────────────────────────────────────────────────── */

describe('参与弹窗：一句话说清多少钱、退不退', () => {
  test('弹窗净增的文字不超过 260 字，且带着具体金额', SLOW, async () => {
    const activity = qyLotDetailFixture({
      ...OPEN,
      ...BALL,
      title: '第 12 期双色球',
      spec: BALL_SPEC,
      stake_quota: 500_000,
    })
    const screen = await mountDetail(activity, {
      entries: [],
      total: 0,
      roster_hash: '',
      seed: '',
      chain_head: '',
      spec: BALL_SPEC,
      notice: '',
      ...BALL,
    })
    const before = screen.chars
    const opened = await screen.click(zhKeys['qy_lot_join_title'])
    assert.ok(opened, '「参与活动」按钮点不开')

    const after = screen.read()
    const dialogChars = after.chars - before
    // 弹窗里有 33 + 16 个号码球（近 100 个字符），那是选号器本身，删不得；
    // 上限管的是它之外的说明文字。改造前扣费提示是一个标题加一段 38 字的
    // 说明，两者说的是同一件事。
    assert.ok(dialogChars <= 260, `参与弹窗净增 ${dialogChars} 字，超过 260`)

    // 决定按不按这颗按钮的是两个具体的量：多少钱、什么情况下退。
    const warn = zhKeys['qy_lot_join_warn_line'].replace('{{amount}}', '$1')
    assert.ok(
      after.text.includes(warn),
      `扣费提示里没有带上金额，或措辞漂了：期望「${warn}」`
    )
  })
})

/* ── 我的参与 ─────────────────────────────────────────────────────── */

describe('我的参与：表底下的脚注折起来', () => {
  /*
    两行里必须有一张**双色球**票。

    lottery-records 的「你的选号 / 本期开奖号」两列由 `hasPick`（pick 非空）
    决定要不要渲染 —— 夹具此前两行都写 `pick: ''`，于是本轮新加的这两列被
    夹具自己关掉了，220 这个数一个新内容都没约束到。守卫全绿，而真实用户
    看到的是另一屏。
  */
  const rows = [
    {
      entry_no: 'LE20260824-69dbcf45d7d51875',
      act_no: 'LA-1',
      title: '第 12 期双色球',
      kind: 'draw',
      draw_mode: 'ball',
      amount: 500000,
      status: 'confirmed',
      created_at: NOW - 86400,
      chain_hash: 'b'.repeat(64),
      pick: '03,09,12,17,22,31|05',
      ball_result: '03,09,12,17,22,30|05',
      won: { kind: 'prize', amount: 1000000 },
    },
    {
      entry_no: 'LE20260824-7f2a1c9e40b3d618',
      act_no: 'LA-3',
      title: '下个版本会不会涨价',
      kind: 'guess',
      amount: 500000,
      status: 'confirmed',
      created_at: NOW - 43200,
      chain_hash: 'f'.repeat(64),
      pick: '',
      won: null,
    },
  ]

  async function mountRecords() {
    const { QyLotteryRecordsBody } = await import('../../lottery-records')
    return mountQyLotScreen({
      element: <QyLotteryRecordsBody />,
      respond: (url) =>
        url.includes('/lottery/my-entries')
          ? { items: rows, total: rows.length, p: 1, page_size: 20 }
          : undefined,
    })
  }

  test('两行记录的一屏不超过 320 字，脚注默认收起', SLOW, async () => {
    const screen = await mountRecords()
    /*
      改造前 268，其中 85 字是压在一张两行的表底下的两条脚注 —— 比表本身还长；
      折起来之后 183（那一版夹具两行都是 `pick: ''`，两列压根没渲染）。
      换成真实的双色球票之后是 297。

      上限**从 220 抬到 320**，抬的全是号码本身：两个表头 9 字，加上每一张
      双色球票的球（14 颗 × 2 位）与规范化串。它们属于"用户不看就不知道自己
      中没中"的那一类，而这一屏正是项目方投诉的落点。抬上限容易变成遮羞布，
      所以下面一条同步钉死了这两列的内容 —— 把号码删掉能让字数好看，但那条
      会当场红。
    */
    assert.ok(
      screen.chars <= 320,
      `我的参与一屏 ${screen.chars} 字，超过 320：${screen.text}`
    )
    for (const key of [
      'qy_lot_records_chain_note',
      'qy_lot_finished_means_funds_only',
    ] as const) {
      assert.ok(
        !screen.text.includes(zhKeys[key]),
        `脚注 ${key} 又摊在表底下了`
      )
    }
  })

  test('双色球那一行的我的号与开奖号都在明面上', SLOW, async () => {
    const screen = await mountRecords()
    for (const piece of [
      '03,09,12,17,22,31|05', // 我买的那一组（规范化串，进哈希链的那份字节）
      '030912172230|05', // 本期开出的号（这一列只画球，球本身就是可见字符）
      zhKeys['qy_lot_ball_my_pick'],
      zhKeys['qy_lot_ball_result'],
    ]) {
      assert.ok(
        screen.text.includes(piece),
        `我的参与少了「${piece}」——这一屏就答不出"我中了没有"`
      )
    }
  })

  test('点开脚注，两条都在', SLOW, async () => {
    const screen = await mountRecords()
    const opened = await screen.click(zhKeys['qy_lot_records_notes_label'])
    assert.ok(opened, '找不到脚注那颗折叠触发器')
    const after = screen.read()
    for (const key of [
      'qy_lot_records_chain_note',
      'qy_lot_finished_means_funds_only',
    ] as const) {
      assert.ok(
        after.text.includes(zhKeys[key]),
        `展开之后 ${key} 仍然不在：折叠把它折没了`
      )
    }
  })

  test('两类票的风险徽章仍然逐行挂着', SLOW, async () => {
    const screen = await mountRecords()
    assert.ok(
      screen.text.includes(zhKeys['qy_lot_risk_badge_stake_lost']),
      '抽奖那一行丢了「参与费不退」'
    )
    assert.ok(
      screen.text.includes(zhKeys['qy_lot_risk_badge_may_lose_principal']),
      '竞猜那一行丢了「猜错会亏本金」——两者的最坏结果差一个量级'
    )
  })
})

/* ── 语言包 ───────────────────────────────────────────────────────── */

describe('精简没有在语言包里留下孤儿键', () => {
  /*
    这些键的最后一个引用在本轮被删掉了（拆成两段的旧文案、合成一枚徽章之后
    多出来的那一枚、以及「尚未参与」这种零信息量的占位）。留着不会有任何信号：
    typecheck 绿、`qy i18n key coverage` 那份测试只查"用到的键在不在"，不查
    "在的键有没有人用"，于是它们会一直躺在两份语言包里，下一个人照着抄。
  */
  const RETIRED = [
    'qy_lot_not_joined',
    'qy_lot_elig_ok_desc',
    'qy_lot_join_warn_title',
    'qy_lot_join_warn_desc',
    'qy_lot_receipt_keep_title',
    'qy_lot_receipt_keep_desc',
    'qy_lot_tl_commit_desc',
    'qy_lot_tl_freeze_desc',
    'qy_lot_tl_reveal_desc',
  ]

  test('两份语言包里都没有它们', () => {
    const leftover = RETIRED.filter(
      (key) => key in zhKeys || key in (enKeys as Record<string, string>)
    )
    assert.deepEqual(leftover, [], `语言包里留下了孤儿键: ${leftover}`)
  })

  test('顶替它们的新键两份语言包里都有', () => {
    const added = [
      'qy_lot_fine_print',
      'qy_lot_join_warn_line',
      'qy_lot_receipt_keep_line',
      'qy_lot_evidence_digest',
      'qy_lot_vf_scope_label',
      'qy_lot_records_notes_label',
      'qy_lot_ball_split_detail',
    ]
    const missing = added.filter(
      (key) => !(key in zhKeys) || !(key in (enKeys as Record<string, string>))
    )
    assert.deepEqual(missing, [], `新键没落全: ${missing}`)
  })
})

/*
 * ── 变异验证（逐条改产品代码、实测这些用例会不会红。baseline 15 pass / 0 fail）──
 *
 *  M1  卡片上整行「参与费」删掉                     → 14 pass / 1 fail
 *  M2  `QyLotFinePrint` 的面板改成 keepMounted
 *      （折叠形同虚设，收起时内容仍在 DOM 里）      → 12 pass / 3 fail
 *  M3  扣费提示不带金额（amount 传空串）            → 14 pass / 1 fail
 *  M4  「我的参与」两条脚注放回表底下的明面上       → 13 pass / 2 fail
 *  M5  卡片上恢复「尚未参与」占位                   → 14 pass / 1 fail
 *  M6  折叠面板渲染 null（点开也是空的）            → 12 pass / 3 fail
 *  M7  大厅分段栏上的风险徽章删掉                   → 14 pass / 1 fail
 *  M8  详情页去掉「不足 N 份将流局并全额退款」      → 14 pass / 1 fail
 *  M9  奖金摊薄那句结论换成通用的「说明」二字
 *      （结论被折进去，明面上只剩一个中性触发器）  → 14 pass / 1 fail
 *  M10 语言包里把 `qy_lot_not_joined` 加回去        → 14 pass / 1 fail
 *  M11 「这份证据证明了什么」里不再渲染本地复算说明 → 14 pass / 1 fail
 *
 * 十一条全部 KILLED。
 *
 * 一条**方法论**上的教训记在这里：「四个决策量」那条最初写的是
 * `hall.text.includes(zhKeys['qy_lot_stake'])` —— 在整屏文本里找「参与费」
 * 三个字。而分段栏上那枚风险徽章写的正是「参与费不退」，于是 M1（把卡片上整行
 * 参与费删掉）跑出来 15 pass / 0 fail，**存活**。改成逐张卡 `querySelectorAll`
 * 之后才抓住。在一屏文本里找一个页面上到处都有的词，测的是"这个词在不在页面上"，
 * 不是"它在不在该在的地方"。
 */
