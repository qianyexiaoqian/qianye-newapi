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
 * 双色球「我中了没有」在四处界面上到底显示了什么。
 *
 * # 被投诉的那个形状
 *
 * 项目方原话：「关于双色球，已开奖的抽奖为什么不显示双色球号码？」以及
 * 「双色球我想要实现的就是你买彩票一样，中不中。」
 *
 * 在跑着的服务上复现之后，缺的**不是**开奖号（详情页与大厅卡片一直都有），
 * 而是另外半句 —— 两组号从来没有在同一屏出现过：
 *
 *   - 活动详情：有开奖号，没有我的号（公开名单那张表对 ball 渲染的是
 *     `opt_no`，逐行显示 `-`）；
 *   - 我的参与：有我的号，没有开奖号（接口 `myEntryView` 不带 `ball_result`）；
 *   - 管理端参与明细：接口一直下发 `pick`，表格一列都没画；
 *   - 唯一并排的地方是「为什么是这个结果」弹窗，在另一张标签页的一个图标按钮
 *     后面，而且要整份拉证据链再用 WebCrypto 复算。
 *
 * # 这份测试守什么
 *
 *  1. **两组号同屏**。开奖号、我的号、命中情况在四处都要能从真实 DOM 上读出来。
 *  2. **命中要能看出来**，而且不能只靠颜色 —— 命中的球挂 `aria-label`，
 *     这里就按 `aria-label` 数命中，与屏幕阅读器读到的是同一份事实。
 *  3. **没中要写出来**。「未中奖」与「还没开奖」是两个结论，写成同一个占位
 *     就等于什么都没说。
 *  4. **已取消的场次绝不许写「未中奖」** —— 那是把退款说成输钱。
 *  5. **已结束状态下仍然可见**，而且**不许被折叠**：`QyLotFinePrint` 收起时
 *     面板不挂载，所以"被折进去了"可以直接从 textContent 上量出来。
 *
 * 期望值一律在本文件里独立写出，不从被测组件回读。
 *
 * 变异实验见文件末尾。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import {
  cleanupQyLotScreens,
  mountQyLotScreen,
  qyLotDetailFixture,
  zhKeys,
} from './screen-harness'

after(cleanupQyLotScreens)

/** React `act` 的异步等待让单个用例超过 bun 默认的 5 秒。 */
const SLOW = { timeout: 120_000 }

const NOW = Math.floor(Date.now() / 1000)
const ACT_NO = 'LA-BALL-1'

/** 与线上那一期同形：红球 12 选 3、蓝球 4 选 1，开出 08 10 11 | 03。 */
const POOL = {
  ball_blue_pick: 1,
  ball_blue_pool: 4,
  ball_red_pick: 3,
  ball_red_pool: 12,
  draw_mode: 'ball' as const,
  issue_no: 3,
  pool_carry_quota: 40_000,
  pool_open_quota: 202_800,
  series_no: 'LS-vis',
}

const DRAWN = '08,10,11|03'

const SPEC = [
  {
    amount_quota: 0,
    blue_match: 1,
    count: 1,
    name: '一等奖',
    pool_share_bps: 5000,
    red_match: 3,
    tier: 1,
    win_ppm: 0,
  },
  {
    amount_quota: 0,
    blue_match: 0,
    count: 5,
    name: '二等奖',
    pool_share_bps: 3000,
    red_match: 2,
    tier: 2,
    win_ppm: 0,
  },
]

/**
 * 两张票：一张命中 2 红 0 蓝（中二等奖），一张一个号都没中。
 *
 * 同一个人身上两种结果，才能证明"中没中"是**逐票**判定 —— 整场一个结论的话，
 * 把两行渲染成同一个样子也能让"中奖那一行显示了金额"这类断言变绿。
 */
const MY_TICKETS = [
  {
    amount: 1000,
    entry_no: 'LE-WIN',
    pick: '10,11,12|04',
    seq: 1,
    status: 'success' as const,
    won_amount: 60_840,
    won_kind: 'prize' as const,
    won_tier: 2,
  },
  {
    amount: 1000,
    entry_no: 'LE-MISS',
    pick: '01,02,03|01',
    seq: 2,
    status: 'success' as const,
    won_amount: 0,
    won_kind: '' as const,
    won_tier: 0,
  },
]

const PROOF = {
  act_no: ACT_NO,
  algo: 'lot-v2',
  ball_result: DRAWN,
  chain_head: 'e'.repeat(64),
  entries: [
    {
      amount: 1000,
      entry_no: 'LE-WIN',
      opt_no: 0,
      pick: '10,11,12|04',
      seq: 1,
      status: 'success',
      user_ref: 'u-a',
    },
    {
      amount: 1000,
      entry_no: 'LE-MISS',
      opt_no: 0,
      pick: '01,02,03|01',
      seq: 2,
      status: 'success',
      user_ref: 'u-a',
    },
  ],
  locked_at: NOW - 600,
  notice: '',
  revealed_at: NOW - 300,
  roster_count: 2,
  roster_hash: 'c'.repeat(64),
  seed: 'd'.repeat(64),
  settled_at: NOW - 100,
  spec: SPEC,
  total: 2,
  ...POOL,
}

function ballDetail(over: Record<string, unknown> = {}) {
  return qyLotDetailFixture({
    ...POOL,
    act_no: ACT_NO,
    ball_result: DRAWN,
    close_at: NOW - 900,
    draw_at: NOW - 300,
    my_entry_count: 2,
    my_tickets: MY_TICKETS,
    open_at: NOW - 3600,
    outcome: 'drawn',
    spec: SPEC,
    status: 'finished',
    title: '双色球第 3 期',
    ...over,
  })
}

async function mountDetail(
  activity: unknown,
  series: unknown = { current: {}, series_no: 'LS-vis' }
) {
  const { QyLotteryDetail } = await import('../detail')
  return mountQyLotScreen({
    path: `/qy/lottery/${ACT_NO}/`,
    element: <QyLotteryDetail />,
    respond: (url) => {
      if (url.includes('/eligibility')) return { eligible: true, missing: [] }
      if (url.includes('/proof')) return PROOF
      if (url.includes('/lottery/series/')) return series
      if (url.includes('/lottery/activities/')) return activity
      return undefined
    },
  })
}

/**
 * 「本期开奖 · 我中了没有」那张卡的根节点。
 *
 * 逐卡取而不是在整屏文本里找关键词：开奖号在这一页上出现在两处（这张卡与
 * 公开名单里每一行的高亮），在整屏文本里找"08"永远能找到，测的是"这个数字
 * 在不在页面上"，不是"它在不在该在的地方"。
 */
function drawCardOf(container: HTMLElement): HTMLElement {
  const card = Array.from(
    container.querySelectorAll('[data-slot="card"]')
  ).find((node) =>
    (node.textContent ?? '').includes(zhKeys['qy_lot_ball_draw_title'])
  )
  assert.ok(card != null, '找不到「本期开奖」那张卡')
  return card as HTMLElement
}

/**
 * 一个节点里被标成「命中」的号。
 *
 * 判据是 `aria-label` 而不是 class：命中态在视觉上是"填色 + 加粗 + 加粗边框"，
 * 按 class 断言等于把 Tailwind 的类名钉进测试；而 `aria-label` 是屏幕阅读器
 * **真的会读出来**的那一份，断言它就是断言用户拿到的信息。
 */
function hitNumbersIn(node: HTMLElement): string[] {
  return Array.from(node.querySelectorAll('[aria-label]'))
    .filter((item) => (item.getAttribute('aria-label') ?? '').includes('命中'))
    .map((item) => (item.textContent ?? '').trim())
}

describe('活动详情：开奖号与我的号在同一屏，命中的高亮', () => {
  test('开奖号、我的号、规范化串三者都在明面上', SLOW, async () => {
    const screen = await mountDetail(ballDetail())
    const card = drawCardOf(screen.container)
    const text = (card.textContent ?? '').replace(/\s+/g, ' ')

    // 进哈希链的那份字节必须留着 —— 用户拿它去比对证据链。
    assert.ok(text.includes(DRAWN), `开奖号的规范化串不在卡上：${text}`)
    for (const pick of ['10,11,12|04', '01,02,03|01']) {
      assert.ok(text.includes(pick), `我的号 ${pick} 不在卡上：${text}`)
    }
    // 球也要真的画出来：七颗号各自是一个节点，而不是一行文本。
    const balls = Array.from(card.querySelectorAll('span')).filter((node) =>
      /^\d{2}$/.test((node.textContent ?? '').trim())
    )
    assert.ok(
      balls.length >= 4 + 4 + 4,
      `号码没有画成球（只找到 ${balls.length} 颗）`
    )
  })

  test('命中的号被标出来，没命中的不标', SLOW, async () => {
    const screen = await mountDetail(ballDetail())
    const card = drawCardOf(screen.container)

    // 开奖号 08,10,11|03 ⋆ 我的号 10,11,12|04 → 命中 10 与 11；
    //                    ⋆ 我的号 01,02,03|01 → 一个都不命中。
    // 期望值在这里独立算出，不从组件回读。
    assert.deepEqual(hitNumbersIn(card).sort(), ['10', '11'])
  })

  test('中了哪一档、赔多少，以及没中的那一张写着「未中奖」', SLOW, async () => {
    const screen = await mountDetail(ballDetail())
    const text = (drawCardOf(screen.container).textContent ?? '').replace(
      /\s+/g,
      ' '
    )
    // 档位 + 档名 + 金额：三样缺一样，用户就得自己去奖级表上查。
    assert.ok(text.includes('第 2 档'), `没写中了哪一档：${text}`)
    assert.ok(text.includes('二等奖'), `没写档名：${text}`)
    assert.ok(text.includes('$0.1217'), `没写这一档赔了多少：${text}`)
    // 命中几红几蓝。
    assert.ok(text.includes('红球 2 个、蓝球 0 个'), `没写命中数：${text}`)
    assert.ok(
      text.includes('红球 0 个、蓝球 0 个'),
      `没写未命中的那张：${text}`
    )
    // 「没中」是结论，必须写出来 —— 不显示与没中在屏幕上长得一样。
    assert.ok(
      text.includes(zhKeys['qy_lot_ball_not_won']),
      `没中的那一张没有写「未中奖」：${text}`
    )
  })

  test('还没开奖时写「待开奖」，一个字都不许写「未中奖」', SLOW, async () => {
    const screen = await mountDetail(
      ballDetail({
        ball_result: '',
        close_at: NOW + 3600,
        draw_at: NOW + 7200,
        outcome: '',
        status: 'published',
      })
    )
    const card = drawCardOf(screen.container)
    const text = (card.textContent ?? '').replace(/\s+/g, ' ')
    assert.ok(
      text.includes(zhKeys['qy_lot_ball_await_draw']),
      `没写「待开奖」：${text}`
    )
    assert.ok(
      !text.includes(zhKeys['qy_lot_ball_not_won']),
      `还没开奖就写了「未中奖」：${text}`
    )
    assert.deepEqual(hitNumbersIn(card), [], '还没开奖却有号被标成命中')
  })

  test('已取消的场次不许写「未中奖」——那是把退款说成输钱', SLOW, async () => {
    const screen = await mountDetail(
      ballDetail({ ball_result: '', outcome: 'cancelled', status: 'finished' })
    )
    const text = (drawCardOf(screen.container).textContent ?? '').replace(
      /\s+/g,
      ' '
    )
    assert.ok(
      !text.includes(zhKeys['qy_lot_ball_not_won']),
      `一场已取消的活动上写了「未中奖」：${text}`
    )
    assert.ok(text.includes(zhKeys['qy_lot_ball_await_draw']))
  })

  test('期次的三件事：期号、上期结转、下一期什么时候', SLOW, async () => {
    const screen = await mountDetail(ballDetail(), {
      current: {
        act_no: 'LA-BALL-2',
        close_at: NOW + 3600,
        draw_at: NOW + 7200,
        issue_no: 4,
        status: 'published',
      },
      series_no: 'LS-vis',
    })
    // 期号在顶部徽章上。
    assert.ok(screen.text.includes('第 3 期'), '期号不在页面上')
    const text = (drawCardOf(screen.container).textContent ?? '').replace(
      /\s+/g,
      ' '
    )
    assert.ok(
      text.includes(zhKeys['qy_lot_ball_carry_in']),
      `没写上一期结转了多少：${text}`
    )
    assert.ok(text.includes('$0.08'), `结转金额没渲染：${text}`)
    assert.ok(text.includes('下一期'), `没写下一期什么时候：${text}`)
    assert.ok(text.includes('第 4 期'), `下一期的期号没渲染：${text}`)
  })

  test('系列上没有下一期时如实说没有，不编一个时间', SLOW, async () => {
    const screen = await mountDetail(ballDetail(), {
      current: {},
      series_no: 'LS-vis',
    })
    const text = drawCardOf(screen.container).textContent ?? ''
    assert.ok(
      text.includes(zhKeys['qy_lot_ball_next_none']),
      `没有下一期时应当如实说明：${text}`
    )
  })

  test('这一整块默认不折叠 —— 上一轮精简不许把它折回去', SLOW, async () => {
    const screen = await mountDetail(ballDetail())
    // `QyLotFinePrint` 收起时 Base UI 不挂载面板，所以"被折进去了"直接表现为
    // 这些字不在 DOM 里。一屏文本读得到，就说明它们没被折叠。
    for (const piece of [
      DRAWN,
      '10,11,12|04',
      zhKeys['qy_lot_ball_not_won'],
      '红球 2 个、蓝球 0 个',
    ]) {
      assert.ok(
        screen.text.includes(piece),
        `「${piece}」不在首屏 —— 要么没渲染，要么被折进了折叠位`
      )
    }
  })
})

describe('买了超过 50 张票：界面必须自己说清列表被截断', () => {
  test('标题写的是真实注数，并指出完整清单在哪里', SLOW, async () => {
    // 后端把 my_tickets 截到 50 条（api_user.go 的 myTicketsCap），而
    // my_entry_count 是同一组过滤条件下的全量 COUNT。标题原先直接写
    // tickets.length，于是同一屏上「我的号（50 注）」与「已参与 60 次」
    // 互相打架，全屏没有一句话说明列表被截断 —— 用户第一反应是有 10 张票丢了。
    const screen = await mountDetail(ballDetail({ my_entry_count: 60 }))
    const card = drawCardOf(screen.container)
    const text = (card.textContent ?? '').replace(/\s+/g, ' ')

    assert.ok(text.includes('我的号（60 注）'), `标题写的不是真实注数：${text}`)
    assert.ok(
      !text.includes('我的号（2 注）'),
      '标题还在数"这一页列出来的那几张"，而不是"我一共买了几张"'
    )
    assert.ok(
      text.includes('只列出前 2 注'),
      `没有一句话说明列表被截断：${text}`
    )
  })

  test('没有截断时不许凭空多出一句提示', SLOW, async () => {
    const screen = await mountDetail(ballDetail())
    const text = (drawCardOf(screen.container).textContent ?? '').replace(
      /\s+/g,
      ' '
    )
    assert.ok(text.includes('我的号（2 注）'), text)
    assert.ok(
      !text.includes('只列出前'),
      '两张票全列出来了，却还写着"只列出前 N 注"'
    )
  })
})

describe('公开名单：每一注买的号', () => {
  test('双色球把「选项」那一列换成选号，并高亮命中', SLOW, async () => {
    const screen = await mountDetail(ballDetail())
    // 名单表默认折起来（一页 20 行、每行约 80 字，摊在首屏会把详情页从
    // 900 多字推到 2500 字）。要看它就得先点开，这一步本身也是断言：
    // 折叠没有把名单折没，只是不再默认占住首屏。
    const opened = await screen.click('展开全场名单(共 2 条)')
    assert.ok(opened, '找不到「展开全场名单」那颗折叠触发器')
    const roster = Array.from(
      screen.container.querySelectorAll('[data-slot="card"]')
    ).find((node) =>
      (node.textContent ?? '').includes(zhKeys['qy_lot_roster_title'])
    )
    assert.ok(roster != null, '找不到公开名单那张卡')
    const text = (roster.textContent ?? '').replace(/\s+/g, ' ')

    assert.ok(
      text.includes(zhKeys['qy_lot_ball_pick_col']),
      `名单上没有选号这一列：${text}`
    )
    // 表头不能写「你的选号」：这张表列的是全场每一个人的号。
    assert.ok(
      !text.includes(zhKeys['qy_lot_ball_my_pick']),
      `公开名单的表头写成了「你的选号」：${text}`
    )
    // 两注的号都画出来了，而且只有真命中的两个号被标出来。
    assert.deepEqual(
      hitNumbersIn(roster as HTMLElement).sort(),
      ['10', '11'],
      '名单里命中的号没有被标出来'
    )
  })
})

describe('我的参与：我的号与开奖号在同一行', () => {
  const row = (over: Record<string, unknown>) => ({
    act_no: ACT_NO,
    amount: 1000,
    ball_result: DRAWN,
    chain_hash: 'b'.repeat(64),
    created_at: NOW - 86400,
    draw_mode: 'ball',
    entry_no: 'LE-WIN',
    kind: 'draw',
    status: 'confirmed',
    title: '双色球第 3 期',
    won: null,
    ...over,
  })

  async function mountRecords(rows: unknown[]) {
    const { QyLotteryRecordsBody } = await import('../../lottery-records')
    return mountQyLotScreen({
      element: <QyLotteryRecordsBody />,
      respond: (url) =>
        url.includes('/lottery/my-entries')
          ? { items: rows, p: 1, page_size: 20, total: rows.length }
          : undefined,
    })
  }

  test('开奖号自己一列，命中的号在我的那一列里高亮', SLOW, async () => {
    const screen = await mountRecords([
      row({
        pick: '10,11,12|04',
        won: { amount: 60_840, kind: 'prize', tier: 2 },
      }),
    ])
    assert.ok(
      screen.text.includes(zhKeys['qy_lot_ball_result']),
      `我的参与没有开奖号这一列：${screen.text}`
    )
    // 这一列画的是球（表格里再摆一行定长串会把行高撑成两倍），
    // 规范化串留在「我的号」那一列与详情页上。
    assert.ok(
      screen.text.includes('081011'),
      `开奖号没有画成球：${screen.text}`
    )
    assert.ok(screen.text.includes('10,11,12|04'), '我的号没渲染')
    assert.deepEqual(
      hitNumbersIn(screen.container).sort(),
      ['10', '11'],
      '我的号里命中的两个没有被标出来'
    )
  })

  test('没中的那一行写「未中奖」，而不是一个含糊的占位', SLOW, async () => {
    const screen = await mountRecords([
      row({ entry_no: 'LE-MISS', pick: '01,02,03|01' }),
    ])
    assert.ok(
      screen.text.includes(zhKeys['qy_lot_ball_not_won']),
      `没中的那一行没写「未中奖」：${screen.text}`
    )
    assert.deepEqual(hitNumbersIn(screen.container), [], '没中却有号被标成命中')
  })

  test('还没开奖 / 已取消的那一行不许写「未中奖」', SLOW, async () => {
    const screen = await mountRecords([
      row({ ball_result: '', entry_no: 'LE-VOID', pick: '01,02,03|01' }),
    ])
    assert.ok(
      screen.text.includes(zhKeys['qy_lot_ball_await_draw']),
      `没开奖的那一行应当写「待开奖」：${screen.text}`
    )
    // 通用占位「未中奖 / 未结算」在这里是**错的**：它先说了"未中奖"。
    assert.ok(
      !screen.text.includes(zhKeys['qy_lot_result_none']),
      `双色球没开奖的行落到了通用占位，而那句话先说了"未中奖"：${screen.text}`
    )
  })

  test('只玩过普通抽奖时这两列一个都不出现', SLOW, async () => {
    const screen = await mountRecords([
      row({
        ball_result: '',
        draw_mode: 'rank',
        entry_no: 'LE-RANK',
        pick: '',
        title: '春季回馈抽奖',
      }),
    ])
    for (const key of ['qy_lot_ball_my_pick', 'qy_lot_ball_result'] as const) {
      assert.ok(
        !screen.text.includes(zhKeys[key]),
        `普通抽奖的表上挂了一列永远没内容的「${zhKeys[key]}」`
      )
    }
  })
})

describe('管理端参与明细：运营也要看得到号', () => {
  test('双色球把「选项」那一列换成选号', SLOW, async () => {
    const { QyAdminLotteryDetail } = await import('../../admin-lottery/detail')
    const activity = {
      act_no: ACT_NO,
      ball_blue_pick: 1,
      ball_blue_pool: 4,
      ball_red_pick: 3,
      ball_red_pool: 12,
      ball_result: DRAWN,
      created_at: NOW - 7200,
      draw_at: NOW - 300,
      draw_mode: 'ball',
      issue_no: 3,
      kind: 'draw',
      outcome: 'drawn',
      pool_open_quota: 202_800,
      pool_quota: 2000,
      refund_quota: 0,
      rules_text: '{}',
      series_no: 'LS-vis',
      stake_quota: 1000,
      status: 'finished',
      title: '双色球第 3 期',
    }
    const entries = [
      {
        amount: 1000,
        chain_hash: 'b'.repeat(64),
        created_at: NOW - 3600,
        entry_no: 'LE-WIN',
        fail_code: '',
        opt_no: 0,
        order_no: 'O-1',
        pick: '10,11,12|04',
        seq: 1,
        settled_at: NOW - 3600,
        status: 'success',
        user_id: 7001,
        user_ref: 'u-a',
        username: 'qy-t2-u1',
      },
    ]
    const screen = await mountQyLotScreen({
      path: `/qy/admin/lottery/${ACT_NO}/`,
      element: <QyAdminLotteryDetail />,
      respond: (url) => {
        if (url.includes('/entries')) {
          return { items: entries, p: 1, page_size: 20, total: 1 }
        }
        if (url.includes('/payouts')) return { items: [], total: 0 }
        if (url.includes('/events')) return { items: [], total: 0 }
        if (url.includes('/admin/lottery/activities/')) {
          return { activity, economics: {}, options: [], prizes: [] }
        }
        return undefined
      },
    })

    // 概览上先要有开奖号本身。
    assert.ok(screen.text.includes(DRAWN), '管理端概览上没有开奖号')

    const opened = await screen.click(zhKeys['qy_lot_a_tab_entries'])
    assert.ok(opened, '点不开「参与名单」这张标签')
    const after = screen.read()
    assert.ok(
      after.text.includes(zhKeys['qy_lot_ball_pick_col']),
      `参与明细上没有选号这一列：${after.text}`
    )
    assert.ok(
      after.text.includes('10,11,12|04') || after.text.includes('101112'),
      `参与明细上没有渲染那一注的号：${after.text}`
    )
  })
})

/*
 * ── 变异验证（逐条改产品代码，实测这些用例会不会红。baseline 14 pass / 0 fail）──
 *
 *  M1  详情卡拿不到 `my_tickets`（恰好是改造前的状态）  → 10 pass / 4 fail
 *  M2  「未中奖」的判据只看 `won` 空不空
 *      （还没开奖 / 已取消的场次也被写成"未中奖"）     → 12 pass / 2 fail
 *  M3  `QyLotBallNumbers` 的 `hits` 一律忽略（谁都不高亮） → 11 pass / 3 fail
 *  M4  公开名单退回渲染 `opt_no`                           → 13 pass / 1 fail
 *  M5  管理端参与明细退回渲染 `opt_no`                     → 13 pass / 1 fail
 *  M6  「我的参与」删掉开奖号那一列                        → 13 pass / 1 fail
 *  M7  把整张「本期开奖」卡塞进 `QyLotFinePrint` 折叠位      → 6 pass / 8 fail
 *  M8  非双色球也渲染那两列（`hasPick` 恒为真）           → 13 pass / 1 fail
 *  M9  下一期缺席时改成显示「第 0 期」                     → 13 pass / 1 fail
 *
 * 九条全部 KILLED。
 *
 * M7 是这份测试存在的主要理由：上一轮刚做过一次界面精简（已结束·双色球
 * 那一屏从 1548 字压到 750），而折叠位收起时 Base UI 不挂载面板 ——
 * “把它折回去”与“把它删了”在 DOM 上是同一件事。开奖号、我的号、命中情况
 * 属于"用户不看就不知道自己中没中"的那一类，不允许再被折起来。
 */
