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
 * 抽奖用户端「少文字、多画面」之后画面上到底有什么(task-C-visual)。
 *
 * 字数上限与决策量由 text-budget.test.tsx 守;这里守的是**新长出来的图形**
 * 真的接上了数据,而不是一张装饰:
 *
 *   1. 大厅每张卡一个倒计时环,弧长等于这一段窗口的剩余比例 —— 环画反了
 *      (剩 10% 画成 90%)在浏览器里只是"看起来怪",没有任何东西会红;
 *   2. 详情页头的阶段轨:当前节点带 `aria-current='step'`,已结束时没有
 *      节点在呼吸;
 *   3. 参与条件里那句长解释折起来了,短句在明面上,点开之后原文逐字回来 ——
 *      折叠不是删除,那句话是协议承诺。
 *
 * 期望值一律在本文件里独立写出或从 zh.json 取,不从被测组件回读。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { QY_RING_CIRCUMFERENCE } from '../../../components/art/ring'
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
/** 报名窗口 2 小时,此刻正好走到一半 → 环应当剩一半。 */
const HALF_WAY = {
  open_at: NOW - 3600,
  close_at: NOW + 3600,
  draw_at: NOW + 7200,
}

async function mountHall() {
  const { QyLotteryDrawBody } = await import('../index')
  const items = [
    qyLotBriefFixture({ act_no: 'LA-a', title: '走到一半的一场', ...HALF_WAY }),
    qyLotBriefFixture({
      act_no: 'LA-b',
      title: '已封盘的一场',
      status: 'locked',
      ...HALF_WAY,
    }),
  ]
  return mountQyLotScreen({
    element: (
      <QyLotteryDrawBody
        page={1}
        scope='live'
        onPageChange={() => {}}
        onScopeChange={() => {}}
      />
    ),
    respond: (url) =>
      url.includes('/lottery/activities')
        ? { items, total: items.length, p: 1, page_size: 12 }
        : undefined,
  })
}

async function mountDetail(activity: unknown) {
  const { QyLotteryDetail } = await import('../detail')
  return mountQyLotScreen({
    path: '/qy/lottery/LA-1/',
    element: <QyLotteryDetail />,
    respond: (url) => {
      if (url.includes('/eligibility')) return { eligible: true, missing: [] }
      if (url.includes('/proof')) return { entries: [], winners: [] }
      if (url.includes('/lottery/activities/')) return activity
      return undefined
    },
  })
}

describe('大厅卡片的倒计时环', () => {
  test(
    '进行中的卡画一个环,弧长 = 剩余比例;已封盘等开奖的卡倒计到开奖',
    SLOW,
    async () => {
      const hall = await mountHall()
      const cards = [...hall.container.querySelectorAll('[data-slot="card"]')]
      assert.equal(cards.length, 2)

      const [live, locked] = cards
      const liveArc = live.querySelector('[data-testid="qy-ring-arc"]')
      assert.ok(liveArc != null, '进行中的卡上没有倒计时环')
      // 走到一半:dashoffset ≈ 周长的一半。允许 1 秒时钟漂移带来的误差。
      const offset = Number(liveArc.getAttribute('stroke-dashoffset'))
      assert.ok(
        Math.abs(offset - QY_RING_CIRCUMFERENCE / 2) < 0.5,
        `剩一半的窗口 offset 应≈${(QY_RING_CIRCUMFERENCE / 2).toFixed(2)},实际 ${offset}`
      )
      assert.ok(
        live
          .querySelector('svg[role="img"]')
          ?.getAttribute('aria-label')
          ?.startsWith(zhKeys.qy_lot_countdown_close),
        '环的可访问名称必须以「距封盘」开头'
      )
      assert.ok(
        locked
          .querySelector('svg[role="img"]')
          ?.getAttribute('aria-label')
          ?.startsWith(zhKeys.qy_lot_countdown_draw),
        '已封盘的卡应当倒计到开奖'
      )
    }
  )
})

describe('详情页的阶段轨与折叠的规则解释', () => {
  test(
    '进行中:「进行中」节点是当前步;长解释默认折起、点开逐字回来',
    SLOW,
    async () => {
      const detail = await mountDetail(qyLotDetailFixture({ ...HALF_WAY }))
      const track = detail.container.querySelector(
        `[aria-label="${zhKeys.qy_lot_phase_track_aria}"]`
      )
      assert.ok(track != null, '页头没有阶段轨')
      const current = track.querySelector('[aria-current="step"]')
      assert.equal(current?.getAttribute('data-state'), 'current')
      assert.ok(
        current?.textContent?.includes(zhKeys.qy_lot_st_published),
        '当前步应当是「进行中」'
      )

      // 短句在明面上,那句 40 字的"为什么"折起来(Collapsible 收起时不挂载)。
      assert.ok(detail.text.includes(zhKeys.qy_lot_rule_admin_allowed_short))
      assert.ok(!detail.text.includes(zhKeys.qy_lot_rule_admin_allowed))
      assert.ok(
        await detail.click(zhKeys.qy_lot_fine_print),
        '找不到折叠触发器'
      )
      assert.ok(
        detail.text.includes(zhKeys.qy_lot_rule_admin_allowed),
        '点开之后原文必须逐字回来 —— 那是协议承诺,折叠不是删除'
      )
    }
  )

  test('已结束:四步全部填实,没有节点在呼吸', SLOW, async () => {
    const detail = await mountDetail(
      qyLotDetailFixture({
        status: 'finished',
        outcome: 'drawn',
        open_at: NOW - 7200,
        close_at: NOW - 3600,
        draw_at: NOW - 1800,
      })
    )
    const track = detail.container.querySelector(
      `[aria-label="${zhKeys.qy_lot_phase_track_aria}"]`
    )
    assert.ok(track != null)
    assert.equal(track.querySelector('[aria-current="step"]'), null)
    const states = [...track.querySelectorAll('li')].map((node) =>
      node.getAttribute('data-state')
    )
    assert.deepEqual(states, ['done', 'done', 'done', 'done'])
  })
})

/* ────────────────────────────────────────────────────────────────────────
 * 号码球与奖档清单（2026-09-05：项目方「文字画太多，观感很差」那一轮）
 * ──────────────────────────────────────────────────────────────────────── */

/** 一场已开奖的双色球，我买了两注：一注中五红一蓝，一注全落空。 */
const BALL_DRAWN = qyLotDetailFixture({
  draw_mode: 'ball',
  issue_no: 12,
  status: 'finished',
  outcome: 'drawn',
  pool_open_quota: 12_000,
  ball_red_pool: 33,
  ball_red_pick: 6,
  ball_blue_pool: 16,
  ball_blue_pick: 1,
  ball_result: '03,09,12,17,22,30|05',
  my_entry_count: 2,
  my_tickets: [
    {
      entry_no: 'E-hit',
      pick: '03,09,12,17,22,31|05',
      status: 'success',
      won_kind: 'quota',
      won_tier: 2,
      won_amount: 2_000,
    },
    {
      entry_no: 'E-miss',
      pick: '01,02,04,05,06,07|01',
      status: 'success',
      won_kind: '',
      won_tier: 0,
      won_amount: 0,
    },
  ],
  spec: [
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
      amount_quota: 2_000,
      count: 3,
      win_ppm: 0,
      red_match: 5,
      blue_match: 1,
    },
  ],
} as never)

/** 本期开出的那一行球（详情页上放大到 `lg` 的那一组）。 */
function drawnBalls(root: HTMLElement): HTMLElement[] {
  return [...root.querySelectorAll<HTMLElement>('.qy-fx-ball-drop')]
}

describe('号码球画成球', () => {
  test(
    '开出的号每颗都是实心球、逐颗落位，且一个「命中」标签都不挂',
    SLOW,
    async () => {
      const detail = await mountDetail(BALL_DRAWN)
      const balls = drawnBalls(detail.container)
      // 6 红 + 1 蓝。少一颗就说明那一行不是开奖号那一组。
      assert.equal(balls.length, 7, '开奖号那一行应当恰好 7 颗球')
      for (const ball of balls) {
        assert.equal(
          ball.getAttribute('data-solid'),
          'true',
          '开奖号必须是实心球：空心是"这颗没中"的形态，而开奖号没有可对照的对象'
        )
        assert.equal(
          ball.getAttribute('aria-label'),
          null,
          '开奖号不能挂「命中」标签，否则读屏把七颗号逐个念成「命中 03、命中 09…」'
        )
      }
      // 逐颗落位：第 N 颗延迟 N × 90ms，`backwards` 让未开始的那几颗停在起始帧。
      const delays = balls.map((ball) => ball.style.animationDelay)
      assert.deepEqual(delays, [
        '0ms',
        '90ms',
        '180ms',
        '270ms',
        '360ms',
        '450ms',
        '540ms',
      ])
    }
  )

  test(
    '我的号：命中的实心 + 挂「命中」标签，落空的空心 —— 差别不只是颜色',
    SLOW,
    async () => {
      const detail = await mountDetail(BALL_DRAWN)
      // 落位动效只给开奖号那一行，所以「我的号」在剩下的球里。
      const mine = [
        ...detail.container.querySelectorAll<HTMLElement>('.qy-art-ball'),
      ].filter((ball) => !ball.classList.contains('qy-fx-ball-drop'))
      assert.ok(mine.length > 0, '我的号一颗球都没画出来')

      const solid = mine.filter(
        (ball) => ball.getAttribute('data-solid') === 'true'
      )
      const hollow = mine.filter(
        (ball) => ball.getAttribute('data-solid') === 'false'
      )
      // 第一注命中 5 红 + 1 蓝 = 6 颗实心；第二注 7 颗全落空。
      assert.equal(solid.length, 6, '命中的号应当恰好 6 颗画成实心球')
      assert.equal(hollow.length, 8, '未命中的号应当留在空心态')
      // 实心 / 空心是**形态**差，`aria-label` 是给读屏的第二条独立线索：
      // 只靠颜色区分命中在色弱与灰度截图下会整个塌掉。
      for (const ball of solid) {
        assert.ok(
          (ball.getAttribute('aria-label') ?? '').includes('命中'),
          '命中的球必须同时挂 aria-label'
        )
      }
      for (const ball of hollow) {
        assert.equal(ball.getAttribute('aria-label'), null)
      }
    }
  )
})

describe('奖档清单排成一档一行', () => {
  test(
    '每一档一枚档位牌，条件与概率都还在，且行末不留孤立的分隔点',
    SLOW,
    async () => {
      const detail = await mountDetail(BALL_DRAWN)
      // 一档一行：两档奖 → 两枚档位牌。牌是纯装饰（档位号在文字里已经说过），
      // 判据用 testid 而不是类名 —— 排版会调，"一档一枚牌"不会。
      const medals = [
        ...detail.container.querySelectorAll<HTMLElement>(
          '[data-testid="qy-tier-medal"]'
        ),
      ]
      assert.equal(medals.length, 2, `档位牌应当两枚，实际 ${medals.length}`)

      // 精简掉的是排版，不是内容：命中门槛与"整档预算"这两句一个都不许少。
      for (const piece of ['需红 6 蓝 1', '需红 5 蓝 1', '整档预算']) {
        assert.ok(detail.text.includes(piece), `奖档清单少了「${piece}」`)
      }

      // 脚注项之间用「·」分隔，分隔点画在每一项**之前**。某一项渲染成空却
      // 仍占着一个位置时，行末会挂一个没有下文的孤立分隔点（实测「50.0000% ·」）。
      for (const medal of medals) {
        const row = medal.closest('li')
        const text = (row?.textContent ?? '').replaceAll(/\s+/g, ' ').trim()
        assert.ok(
          !text.endsWith('·'),
          `奖档行末尾挂着孤立的分隔点：「${text}」`
        )
      }
    }
  )
})
