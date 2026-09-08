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

/* ────────────────────────────────────────────────────────────────────────
 * 盘面画成一只转盘（2026-09-05：项目方「文字画太多，观感很差」那一轮）
 * ──────────────────────────────────────────────────────────────────────── */

describe('盘面的图形层', () => {
  test('每档一道刻度、一颗轮毂、一层球面光，且渐变 id 是本实例专属的', async () => {
    await mountQyWheelScreen({
      element: <QyWheelFace spec={SPEC} targetPpm={null} reducedMotion />,
      respond: () => undefined,
    })
    const node = face()

    // 刻度：每个扇区起点一道，把"这里是一档的边界"补在 1px 发丝线之外。
    // 限定成盘面 svg 的直接子节点 —— 售罄斜纹那个 `<pattern>` 里也有一条
    // `<line>`，不限定就会把它数进来。
    assert.equal(
      node.querySelectorAll('svg[role="img"] > line').length,
      SPEC.length,
      '刻度数应当与扇区数一致'
    )
    // 球面光：一层渐变盖在扇区之上，轮毂再盖在它之上。两处引用同一个 id。
    const dome = [...node.querySelectorAll('circle')].filter((circle) =>
      (circle.getAttribute('fill') ?? '').startsWith('url(#')
    )
    assert.equal(dome.length, 2, '球面光应当各盖一层在盘面与轮毂上')
    const domeId = dome[0]?.getAttribute('fill')
    assert.equal(dome[1]?.getAttribute('fill'), domeId, '两层要引用同一份渐变')

    const defined = [...node.querySelectorAll('radialGradient')].map((item) =>
      item.getAttribute('id')
    )
    assert.equal(defined.length, 1)
    assert.equal(`url(#${defined[0]})`, domeId, '球面光引用的不是本实例的定义')
  })

  test('同一页上的两只盘各用各的渐变 id', async () => {
    // 这正是生产里的形状：详情页面板上挂着一只静止的盘，转动弹窗里又挂一只在
    // 转的盘。SVG 的 id 是全文档作用域的，写死 id 会让两只盘共用先挂载的那份
    // 定义 —— 表现是其中一只的球面光整个消失或者错位，而不会报任何错。
    await mountQyWheelScreen({
      element: (
        <>
          <QyWheelFace spec={SPEC} targetPpm={null} reducedMotion />
          <QyWheelFace spec={SPEC} targetPpm={4_097} reducedMotion />
        </>
      ),
      respond: () => undefined,
    })
    const faces = [
      ...document.querySelectorAll('[data-testid="qy-wheel-face"]'),
    ]
    assert.equal(faces.length, 2, '夹具应当挂出两只盘')
    const ids = faces.map((item) =>
      item.querySelector('radialGradient')?.getAttribute('id')
    )
    assert.ok(ids.every((id) => id != null && id !== ''))
    assert.notEqual(ids[0], ids[1], '两只盘的渐变 id 撞了 —— id 被写死了')
  })

  test('转动中才有扫光；落定与缩减动效下都没有', async () => {
    // 缩减动效：`settled` 与 `targetPpm` 在同一拍内就位，从头到尾不该有扫光。
    await mountQyWheelScreen({
      element: <QyWheelFace spec={SPEC} targetPpm={4_097} reducedMotion />,
      respond: () => undefined,
    })
    assert.equal(
      face().querySelectorAll('.qy-fx-spin-sheen').length,
      0,
      '缩减动效下盘面不旋转，那道表示"还在转"的扫光就不该存在'
    )

    // 正常动效：结果已经回来、盘面还在转的那两秒里，扫光在。它画在盘面之外、
    // 匀速自转 —— 盘面自己那条 ease-out 曲线最后一秒几乎不动，没有它用户会
    // 以为卡住了。
    await mountQyWheelScreen({
      element: (
        <QyWheelFace spec={SPEC} targetPpm={4_097} reducedMotion={false} />
      ),
      respond: () => undefined,
    })
    const spinning = face()
    assert.equal(spinning.getAttribute('data-settled'), null, '这时还没落定')
    assert.equal(
      spinning.querySelectorAll('.qy-fx-spin-sheen').length,
      1,
      '转动中必须有扫光'
    )
  })

  test('落在一档占满整个盘的格子上时，不画那圈描边', async () => {
    // 演示站真实场次：0.5% 一档小奖 + 99.5%「谢谢参与」。落在后者时，落档描边
    // 的两条半径近乎重合，描出来的是盘子的外圈加一条从圆心拉到边缘的竖线 ——
    // 没有对照物、只剩噪音，而且强调的恰好是"你没中"那一格。
    const lopsided: QyLotSpecItem[] = [
      {
        tier: 1,
        name: '5',
        amount_quota: 500,
        count: 1,
        prize_type: 'quota',
        win_ppm: 5_000,
        stock_left: 1,
      },
      { tier: 2, name: '谢谢参与', prize_type: 'none', win_ppm: 995_000 },
    ]
    const outlines = (root: HTMLElement) =>
      [...root.querySelectorAll('path')].filter(
        (item) =>
          item.getAttribute('fill') === 'none' &&
          item.getAttribute('stroke') === 'var(--foreground)'
      ).length

    await mountQyWheelScreen({
      element: (
        <QyWheelFace spec={lopsided} targetPpm={500_000} reducedMotion />
      ),
      respond: () => undefined,
    })
    const wide = face()
    assert.equal(
      wide.getAttribute('data-landed-tier'),
      '2',
      '应当落在 99.5% 那一格'
    )
    assert.equal(outlines(wide), 0, '占满整盘的那一格不该再描一圈边')

    // 反过来：落在 0.5% 那一条细缝上，描边仍然要在 —— 那时它真的把一格
    // 从别的格里挑了出来。
    await mountQyWheelScreen({
      element: <QyWheelFace spec={lopsided} targetPpm={2_000} reducedMotion />,
      respond: () => undefined,
    })
    const thin = face()
    assert.equal(thin.getAttribute('data-landed-tier'), '1')
    assert.equal(outlines(thin), 1, '窄格子落档时描边必须还在')
  })
})
