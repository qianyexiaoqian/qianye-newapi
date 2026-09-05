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
 * 转盘标签的列表：发出去的请求参数与卡片上真的出现了什么。
 *
 * 转盘是「抽奖竞猜」选择夹的一张标签（项目方 2026-09-05），但不走大厅的
 * lane，自己带 `draw_mode=wheel` 拉列表；两个参数一起给是 400。
 * 这条契约在类型层守不住（`lane` 与 `draw_mode` 都是可选的查询串），只有
 * "真的发出去的那个请求"能证明。卡片那一半同理：每档"剩余 / 初始"是转盘卡片
 * 与大厅卡片唯一的差别，派生的「谢谢参与」行没有库存、不该出现在那一列里。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import type { QyLotActivityBrief } from '../../lottery/types'
import {
  cleanupQyWheelScreens,
  mountQyWheelScreen,
  zhKeys,
} from './wheel-harness'

const { QyWheelBody } = await import('../index')
const { useQyLotHallCursor } = await import('../../lottery/lib/use-hall-cursor')

/**
 * 正文 + 它在宿主里那份「翻到哪儿了」。生产里游标由 `lottery/hub.tsx` 持有，
 * 这里只需要同一形状的一份；标签栏本身由 hub-tabs.test.tsx 守。
 */
function WheelTab() {
  const cursor = useQyLotHallCursor()
  return <QyWheelBody {...cursor} />
}

after(async () => {
  await cleanupQyWheelScreens()
})

function wheel(over: Partial<QyLotActivityBrief>): QyLotActivityBrief {
  return {
    act_no: 'LW-0',
    kind: 'draw',
    draw_mode: 'wheel',
    status: 'published',
    outcome: '',
    title: '未命名',
    currency: 'stardust',
    stake_quota: 100,
    open_at: 0,
    close_at: 4_102_444_800,
    draw_at: 4_102_444_801,
    active_count: 7,
    pool_quota: 700,
    prize_total_quota: 1_000,
    my_entry_count: 0,
    tiers: [
      {
        tier: 1,
        name: '头奖',
        amount_quota: 500,
        count: 2,
        prize_type: 'quota',
        win_ppm: 300_000,
        stock_left: 1,
      },
      {
        tier: 2,
        name: '兑换码',
        count: 1,
        prize_type: 'text',
        win_ppm: 200_000,
        text_desc: '联系客服领取',
        stock_left: 0,
      },
      { tier: 3, name: '谢谢参与', prize_type: 'none', win_ppm: 500_000 },
    ],
    ...over,
  }
}

const LIVE = [wheel({ act_no: 'LW-live', title: '正在转的一场' })]
const ENDED = [
  wheel({
    act_no: 'LW-done',
    status: 'finished',
    outcome: 'drawn',
    title: '已经结束的一场',
  }),
]

describe('转盘标签列表', () => {
  test('默认问「进行中」，请求带 draw_mode=wheel 与 phase=live、不带 lane，卡片画出每档剩余', async () => {
    const screen = await mountQyWheelScreen({
      element: <WheelTab />,
      respond: (req) => {
        if (!req.url.includes('/lottery/activities')) return undefined
        const items = req.params.phase === 'ended' ? ENDED : LIVE
        return { items, total: items.length, p: 1, page_size: 12 }
      },
    })

    const asked = screen.sent.filter((row) =>
      row.url.includes('/lottery/activities')
    )
    assert.equal(asked.length, 1)
    assert.equal(asked[0].params.draw_mode, 'wheel')
    assert.equal(asked[0].params.phase, 'live')
    assert.ok(
      !('lane' in asked[0].params),
      '转盘那张标签不走大厅 lane：与 lane 同给是 400'
    )

    const text = screen.text()
    assert.ok(text.includes('正在转的一场'), `进行中的转盘没渲染出来：${text}`)
    assert.ok(!text.includes('已经结束的一场'))
    assert.ok(
      text.includes(`100 ${zhKeys.qy_sd_unit_default}`),
      '卡片必须写清每转多少'
    )
    // 每档"剩余 / 初始"：头奖 1/2、兑换码 0/1；谢谢参与没有库存、不在这一列。
    // 名字与数字是两个相邻的 span，textContent 之间没有空白。
    assert.match(text, /头奖\s*1 \/ 2/, `头奖的剩余没画出来：${text}`)
    assert.match(text, /兑换码\s*0 \/ 1/)
    assert.ok(!text.includes('谢谢参与'))
    assert.ok(
      !text.includes('qy_lot_wheel_'),
      'i18n 键没翻译，卡片上出现的是键名'
    )

    // 每档剩余同时画成条：`1 / 2` 与 `50 / 100` 读起来一样，条子把比例画出来。
    // 判据用 progressbar 的 aria 值 —— 那是读屏与条子共用的同一份事实。
    const bars = [...screen.container.querySelectorAll('[role="progressbar"]')]
    assert.equal(bars.length, 2, '两档真实奖各一根条，谢谢参与没有库存不画')
    const [top, code] = bars
    assert.equal(top.getAttribute('aria-valuenow'), '1')
    assert.equal(top.getAttribute('aria-valuemax'), '2')
    assert.equal(top.getAttribute('data-empty'), null)
    assert.equal(code.getAttribute('aria-valuenow'), '0')
    assert.equal(
      code.getAttribute('data-empty'),
      'true',
      '发完的档轨道要换成斜纹终态，不能是一根空白条'
    )
    // 倒计时环：进行中的一场画一个环，可访问名称就是「距封盘 + 时长」。
    const ring = screen.container.querySelector('svg[role="img"][aria-label]')
    assert.ok(
      ring
        ?.getAttribute('aria-label')
        ?.startsWith(zhKeys.qy_lot_countdown_close),
      '进行中的转盘卡上没有倒计时环'
    )
  })

  test('切到「已结束」后请求带 phase=ended，屏幕换成已结束的那一批', async () => {
    const screen = await mountQyWheelScreen({
      element: <WheelTab />,
      respond: (req) => {
        if (!req.url.includes('/lottery/activities')) return undefined
        const items = req.params.phase === 'ended' ? ENDED : LIVE
        return { items, total: items.length, p: 1, page_size: 12 }
      },
    })
    assert.ok(await screen.click(zhKeys.qy_lot_tab_done), '没有「已结束」标签')

    const phases = screen.sent
      .filter((row) => row.url.includes('/lottery/activities'))
      .map((row) => row.params.phase)
    assert.deepEqual(phases, ['live', 'ended'])

    const text = screen.text()
    assert.ok(text.includes('已经结束的一场'))
    assert.ok(!text.includes('正在转的一场'))
    assert.ok(
      text.includes(zhKeys.qy_lot_outcome_drawn),
      '已结束的卡要看得出结局'
    )
  })
})
