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
 * 转盘面落定之后自己会说什么:落档扇区高亮、售罄扇区打斜纹、指针点头。
 *
 * 这三样全是"不看字也要看得出"的视觉事实,而它们没有一样能靠类型守住:
 * 落档算错(高亮了隔壁那一格)在渲染上不报错,只是指针停的那一格与文字说的
 * 那一档对不上 —— 那正是转盘最伤信任的一种错。判据用 DOM 属性与 aria-label,
 * 不用 Tailwind 类名:属性是读屏与测试共用的那一份事实。
 *
 * 缩减动效由夹具固定为真:盘面不旋转、落定在同一拍内发生(design-14 §4)。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import type { QyLotSpecItem } from '../../lottery/types'
import {
  cleanupQyWheelScreens,
  mountQyWheelScreen,
  zhKeys,
} from './wheel-harness'

const { QyWheelFace } = await import('../components/wheel-face')

after(async () => {
  await cleanupQyWheelScreens()
})

/** 头奖 30%(还有货)、兑换码 20%(已发完)、谢谢参与 50%。 */
const SPEC: QyLotSpecItem[] = [
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
]

function face(): HTMLElement {
  const node = document.querySelector<HTMLElement>(
    '[data-testid="qy-wheel-face"]'
  )
  assert.ok(node != null, '盘面没渲染出来')
  return node
}

describe('转盘面', () => {
  test('静止时:盘面说明是通用那句,没有落档,售罄档打斜纹并挂「售罄」', async () => {
    const screen = await mountQyWheelScreen({
      element: <QyWheelFace spec={SPEC} targetPpm={null} reducedMotion />,
      respond: () => undefined,
    })
    const node = face()
    assert.equal(node.getAttribute('data-settled'), null)
    assert.equal(node.getAttribute('data-landed-tier'), null)
    assert.equal(
      node.querySelector('svg[role="img"]')?.getAttribute('aria-label'),
      zhKeys.qy_lot_wheel_face_aria
    )
    // 只有兑换码那一档发完了:盘面上恰好一格打斜纹,图例上恰好一枚「售罄」。
    assert.equal(node.querySelectorAll('[data-sold-out="true"]').length, 1)
    const badge = zhKeys.qy_lot_wheel_sold_out_badge
    assert.equal(
      screen.text().split(badge).length - 1,
      1,
      `「${badge}」应恰好出现一次`
    )
  })

  test('落在头奖:落定后高亮第 1 档、盘面说明改口说停在了哪、指针点头', async () => {
    await mountQyWheelScreen({
      element: <QyWheelFace spec={SPEC} targetPpm={4_097} reducedMotion />,
      respond: () => undefined,
    })
    const node = face()
    assert.equal(node.getAttribute('data-settled'), 'true')
    assert.equal(node.getAttribute('data-landed-tier'), '1')
    assert.equal(
      node.querySelector('svg[role="img"]')?.getAttribute('aria-label'),
      zhKeys.qy_lot_wheel_face_landed_aria.replaceAll('{{name}}', '头奖')
    )
    const pointer = node.querySelector('svg[aria-hidden="true"]')
    assert.ok(
      pointer?.getAttribute('class')?.includes('qy-fx-pointer-settle'),
      '落定那一拍指针要点一下头(缩减动效下由 CSS 关掉,类名仍在)'
    )
  })

  test('摇中已发完的那一档:盘面照样高亮它 —— 「摇中但已发完」要能从盘面上看出来', async () => {
    await mountQyWheelScreen({
      element: <QyWheelFace spec={SPEC} targetPpm={464_548} reducedMotion />,
      respond: () => undefined,
    })
    const node = face()
    assert.equal(node.getAttribute('data-landed-tier'), '2')
    const landed = node.querySelector('[data-sold-out="true"]')
    assert.ok(landed != null, '落档的那一格必须仍然标着售罄')
  })
})
