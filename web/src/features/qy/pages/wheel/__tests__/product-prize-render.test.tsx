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
 * 转盘上的商品奖（`prize_type='product'`）：奖档表与盘面图例印出商品名与形态，
 * 中了实物奖之后结果屏当场给收货地址表单，提交的请求体与路径逐字对得上。
 *
 * 这条链路上没有一处能靠类型守住：product 档 `amount_quota` 恒为 0，渲染成
 * `0 星屑` 在类型上完全合法，但用户读到的是"中了个空气"；地址表单发到哪张单、
 * 带什么字段，只有"真的发出去的那个请求"能证明。
 *
 * 缩减动效由夹具固定为真：盘面不旋转、结果直接出现（design-14 §4 的降级路径）。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import type { QyLotActivityDetail, QyLotSpecItem } from '../../lottery/types'
import type { QyWheelSpinResult } from '../types'
import {
  cleanupQyWheelScreens,
  mountQyWheelScreen,
  zhKeys,
} from './wheel-harness'

const { QyWheelTierTable } = await import('../components/wheel-tier-table')
const { QyWheelSpinDialog } = await import('../components/wheel-spin-dialog')

after(async () => {
  await cleanupQyWheelScreens()
})

/** 星屑 30%、实物周边 20%、谢谢参与 50%。 */
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
    name: '周边奖',
    count: 3,
    prize_type: 'product',
    product_no: 'P-mug',
    product_title: '限量马克杯',
    product_kind: 'physical',
    win_ppm: 200_000,
    stock_left: 3,
  },
  { tier: 3, name: '谢谢参与', prize_type: 'none', win_ppm: 500_000 },
]

const ACTIVITY: QyLotActivityDetail = {
  act_no: 'LW-P',
  kind: 'draw',
  draw_mode: 'wheel',
  status: 'published',
  outcome: '',
  title: '星屑转盘·商品奖',
  intro: '',
  currency: 'stardust',
  stardust_balance: 5_000,
  stake_quota: 100,
  open_at: 0,
  close_at: 4_102_444_800,
  draw_at: 4_102_444_801,
  settle_deadline: 0,
  commit_hash: 'a'.repeat(64),
  rules_text: '{}',
  spec: SPEC,
  active_count: 3,
  pool_quota: 300,
  fee_bps: 0,
  min_entries_to_hold: 0,
  allow_multi_win: true,
  my_entry_count: 1,
  win_opt_no: 0,
  result_evidence: '',
  bet_min_quota: 0,
  bet_max_quota: 0,
  max_entries_per_user: 0,
  cooldown_seconds: 0,
  dedup_ip: false,
  pay_password_required: false,
  pay_password_threshold_stardust: 20,
  play_open: true,
}

const PAY_PASSWORD_STATUS = {
  is_set: true,
  locked: false,
  locked_until: 0,
  fail_count: 0,
  remaining_attempts: 5,
  max_attempts: 5,
  lock_minutes: 30,
  set_at: 1,
  changed_at: 1,
}

/** 摇号量 464_548 落在第 2 档（[300000, 500000)）。 */
const WON_PRODUCT: QyWheelSpinResult = {
  entry_no: 'LE-product-1',
  seq: 5,
  ppm: 464_548,
  result_tier: 2,
  exhausted_tier: 0,
  amount: 0,
  prize_type: 'product',
  product_no: 'P-mug',
  mall_order_no: 'ML-PRIZE-1',
  chain_head: 'd'.repeat(64),
  replayed: false,
}

describe('转盘商品奖的展示', () => {
  test('奖档表：product 档印商品名与形态徽章，不印 0 星屑', async () => {
    const screen = await mountQyWheelScreen({
      element: <QyWheelTierTable spec={SPEC} />,
      respond: () => undefined,
    })
    const text = screen.text()
    assert.ok(text.includes('限量马克杯'), `奖档表上没有商品名：${text}`)
    assert.ok(
      text.includes(zhKeys.qy_ml_kind_physical),
      '奖档表上没有形态（实物）'
    )
    const badge = document.querySelector('[data-qy-mall-kind="physical"]')
    assert.ok(badge != null, '形态徽章没带 data-qy-mall-kind')
    // 「500 星屑」（头奖那一档）里也含 "0 星屑" 这个子串，所以按"前面不是数字的 0"判。
    assert.doesNotMatch(
      text,
      new RegExp(`(?<!\\d)0 ${zhKeys.qy_sd_unit_default}`),
      'product 档的 amount_quota 恒为 0，不该渲染成 0 星屑'
    )
  })

  test('中了实物商品奖：结果屏印商品名、给「去查看」，地址表单当场提交到那张单上', async () => {
    const screen = await mountQyWheelScreen({
      element: (
        <QyWheelSpinDialog
          activity={ACTIVITY}
          open
          onOpenChange={() => {}}
          canSpinAgain
        />
      ),
      respond: (req) => {
        if (req.url.includes('/pay-password')) return PAY_PASSWORD_STATUS
        if (req.method === 'POST' && req.url.endsWith('/spins')) {
          return WON_PRODUCT
        }
        if (req.method === 'POST' && req.url.endsWith('/address')) {
          return { order_no: 'ML-PRIZE-1', address_missing: false }
        }
        return undefined
      },
    })
    assert.ok(await screen.click(zhKeys.qy_lot_wheel_spin_confirm))

    const text = screen.text()
    assert.ok(
      text.includes(
        zhKeys.qy_lot_wheel_result_won.replaceAll('{{name}}', '周边奖')
      ),
      `结果屏没说中了哪一档：${text}`
    )
    assert.ok(text.includes('限量马克杯'), '结果屏上没有商品名')
    assert.ok(
      text.includes(zhKeys.qy_lot_wheel_won_product_physical_note),
      '实物奖要说清"填地址后发货"'
    )
    const view = screen.button(zhKeys.qy_lot_wheel_won_product_view)
    assert.ok(view != null, '没有「去查看」')
    assert.ok(
      (view.getAttribute('href') ?? '').includes('/qy/mall'),
      `「去查看」应指向商城的我的订单：${view.getAttribute('href')}`
    )

    // 地址表单就在结果屏上，提交前按钮禁用。
    assert.ok(
      document.querySelector('[data-testid="qy-mall-prize-address-form"]') !=
        null,
      '实物奖的结果屏上没有地址表单'
    )
    assert.equal(
      screen.button(zhKeys.qy_ml_prize_address_submit)?.disabled,
      true,
      '地址与联系方式都空着时不该能提交'
    )
    const address = screen.field(zhKeys.qy_ml_address)
    const contact = screen.field(zhKeys.qy_ml_contact)
    assert.ok(address != null && contact != null, '表单缺地址 / 联系方式')
    await screen.type(address, '某省某市某路 1 号')
    await screen.type(contact, '13800000000')
    assert.ok(await screen.click(zhKeys.qy_ml_prize_address_submit))

    const posts = screen.sent.filter(
      (row) => row.method === 'POST' && row.url.endsWith('/address')
    )
    assert.equal(posts.length, 1, `应当恰好提交一次地址：${posts.length}`)
    assert.ok(
      posts[0].url.endsWith('/mall/orders/ML-PRIZE-1/address'),
      `地址发到了别的单上：${posts[0].url}`
    )
    assert.deepEqual(posts[0].body, {
      address: '某省某市某路 1 号',
      contact: '13800000000',
    })
    // 填过之后表单让位给一句"已填写"：这张单只能填一次。
    assert.equal(
      document.querySelector('[data-testid="qy-mall-prize-address-form"]'),
      null
    )
    assert.ok(screen.text().includes(zhKeys.qy_ml_prize_address_done))
  })
})

/* ────────────────────────────────────────────────────────────────────────
 * 奖档清单改成"一档一行"之后的形状（2026-09-05：「文字画太多，观感很差」）
 * ──────────────────────────────────────────────────────────────────────── */

describe('转盘奖档清单的形状', () => {
  test('一档一枚档位牌，牌面颜色与盘面扇区同色；行末不留孤立的分隔点', async () => {
    const screen = await mountQyWheelScreen({
      element: <QyWheelTierTable spec={SPEC} />,
      respond: () => undefined,
    })
    const medals = [
      ...screen.container.querySelectorAll<HTMLElement>(
        '[data-testid="qy-tier-medal"]'
      ),
    ]
    assert.equal(
      medals.length,
      SPEC.length,
      `档位牌应当与奖档一一对应（含「谢谢参与」），实际 ${medals.length}`
    )

    // 清单与盘面是同一份数据的两种画法，颜色是它们之间唯一的对应关系：
    // 真实档的牌面取扇区色，「谢谢参与」不取（它在牌上画成空心）。
    const painted = medals.filter((node) => node.style.background !== '')
    assert.equal(
      painted.length,
      SPEC.filter((item) => item.prize_type !== 'none').length,
      '真实档的档位牌必须带上盘面那一格的扇区色'
    )

    // 脚注项之间的「·」画在每一项**之前**。某一项渲染成空却仍占着位置时，
    // 行末会挂一个没有下文的分隔点 ——「谢谢参与」没有库存、也没有复算值，
    // 正是最容易踩到这条的那一行（实测「50.0000% · ·」）。
    for (const medal of medals) {
      const row = medal.closest('li')
      const text = (row?.textContent ?? '').replaceAll(/\s+/g, ' ').trim()
      assert.ok(!text.endsWith('·'), `奖档行末尾挂着孤立的分隔点：「${text}」`)
    }
  })

  test('复算库存与公布库存对不上时，那一格自己变成警示色', async () => {
    const screen = await mountQyWheelScreen({
      // 第 1 档公布剩 1，复算出来是 0 —— 揭示之后这两个数必须相等。
      element: <QyWheelTierTable spec={SPEC} replayStock={new Map([[1, 0]])} />,
      respond: () => undefined,
    })
    const mismatch = screen.container.querySelector('.text-destructive')
    assert.ok(
      mismatch != null,
      '复算值与公布值对不上，却没有任何一格喊出来 —— 一个只是灰着的数字对不上，没有人会发现'
    )
    assert.ok(
      (mismatch.textContent ?? '').includes('0'),
      '喊出来的应当是复算得到的那个数'
    )
  })
})
