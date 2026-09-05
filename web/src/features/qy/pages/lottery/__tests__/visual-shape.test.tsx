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
