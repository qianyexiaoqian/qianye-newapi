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
 * 转动弹窗：幂等键与客户端种子在打开那一刻定死、验密只在后端说要时才出现、
 * 重试沿用同一个请求号，以及三种结局各说各的。
 *
 * 这条链路上没有一处能靠类型守住：
 *   · 第一次提交**不带** `pay_password`（阈值只有后端知道）；
 *   · 吃到 `qy_pay_pwd_required` 之后密码格才出现；
 *   · 第二次提交带上密码，`client_request_id` 与 `client_seed` 与第一次**逐字相同**
 *     —— 换了请求号是一次新的转动（真的再扣一笔），换了种子是 409；
 *   · 结果屏要把「中了哪档、拿到多少」「摇中但已发完」「谢谢参与」说成三句话，
 *     并把序号 / 种子 / 链哈希留给用户。
 *
 * 缩减动效由夹具固定为真：盘面不旋转、结果直接出现（design-14 §4 的降级路径）。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import type { QyLotActivityDetail } from '../../lottery/types'
import type { QyWheelSpinResult } from '../types'
import {
  cleanupQyWheelScreens,
  mountQyWheelScreen,
  qyWheelHttpError,
  zhKeys,
} from './wheel-harness'

const { QyWheelSpinDialog } = await import('../components/wheel-spin-dialog')

after(async () => {
  await cleanupQyWheelScreens()
})

const ACTIVITY: QyLotActivityDetail = {
  act_no: 'LW-1',
  kind: 'draw',
  draw_mode: 'wheel',
  status: 'published',
  outcome: '',
  title: '星屑转盘·测试',
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
  spec: [
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

const WON: QyWheelSpinResult = {
  entry_no: 'LE-won-1',
  seq: 4,
  ppm: 4_097,
  result_tier: 1,
  exhausted_tier: 0,
  amount: 500,
  prize_type: 'quota',
  chain_head: 'c'.repeat(64),
  replayed: false,
}

function withName(key: string, name: string): string {
  return zhKeys[key].replaceAll('{{name}}', name)
}

describe('转动弹窗', () => {
  test('种子默认随机 16 位；先不带密码提交，后端说要才显示密码格；重试沿用同一个请求号与种子', async () => {
    let attempts = 0
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
          attempts += 1
          if (attempts === 1) {
            return qyWheelHttpError(
              400,
              'qy_pay_pwd_required',
              '请输入支付密码'
            )
          }
          return WON
        }
        return undefined
      },
    })

    const seed = screen.field(zhKeys.qy_lot_wheel_seed_label)
    assert.ok(seed != null, '弹窗上没有客户端种子那一格')
    assert.match(
      seed.value,
      /^[0-9A-Za-z_-]{16}$/,
      '默认种子应当是 16 位合规字符'
    )
    const defaultSeed = seed.value
    assert.ok(
      screen.field(zhKeys.qy_pp_field_label) == null,
      '还没提交就显示了密码格 —— 前端在猜后端的验密规则'
    )
    assert.ok(
      screen.text().includes(`100 ${zhKeys.qy_sd_unit_default}`),
      '弹窗上必须复述每转多少'
    )

    assert.ok(
      await screen.click(zhKeys.qy_lot_wheel_spin_confirm),
      '没有「确认转动并扣费」'
    )
    const password = screen.field(zhKeys.qy_pp_field_label)
    assert.ok(password != null, '吃到 qy_pay_pwd_required 之后密码格没有出现')
    assert.ok(
      screen.button(zhKeys.qy_lot_wheel_spin_confirm)?.disabled === true,
      '密码还没填时提交键应当是禁用的'
    )
    await screen.type(password, 'secret-1')
    assert.ok(await screen.click(zhKeys.qy_lot_wheel_spin_confirm))

    const posts = screen.sent.filter(
      (row) =>
        row.method === 'POST' &&
        row.url.endsWith('/lottery/activities/LW-1/spins')
    )
    assert.equal(posts.length, 2, `应当恰好提交两次：${posts.length}`)
    const first = posts[0].body as Record<string, unknown>
    const second = posts[1].body as Record<string, unknown>
    assert.ok(
      !('pay_password' in first),
      '第一次提交不该带 pay_password（前端不猜验密规则）'
    )
    assert.equal(
      first.client_seed,
      defaultSeed,
      '发出去的种子必须就是格子里那一份'
    )
    assert.equal(second.pay_password, 'secret-1')
    assert.ok(
      typeof first.client_request_id === 'string' &&
        first.client_request_id !== '',
      '幂等键必须在打开弹窗时就生成'
    )
    assert.equal(
      second.client_request_id,
      first.client_request_id,
      '重试换了幂等键：后端会把它当成第二次转动、再扣一笔'
    )
    assert.equal(
      second.client_seed,
      first.client_seed,
      '重试换了种子：后端会 409'
    )

    // 结果屏：缩减动效下直接出现。中了「头奖」+ 500 星屑 + 凭据三件。
    const text = screen.text()
    assert.ok(
      text.includes(withName('qy_lot_wheel_result_won', '头奖')),
      `结果屏没说中了哪一档：${text}`
    )
    assert.ok(text.includes(`500 ${zhKeys.qy_sd_unit_default}`))
    assert.ok(text.includes('LE-won-1'), '回执上要有转动单号')
    assert.ok(text.includes('c'.repeat(64)), '回执上要有链哈希')
    assert.ok(text.includes(defaultSeed), '回执上要原样带着种子')
    assert.ok(
      text.includes(zhKeys.qy_lot_wheel_spin_again),
      '活动还开着时要能再转一次'
    )
    assert.ok(!text.includes('qy_lot_wheel_'), 'i18n 键没翻译')
    // 中了才放粒子。它是纯装饰(aria-hidden),这里只断言"中奖那一屏有它"。
    assert.ok(
      document.querySelector('[data-testid="qy-burst"]') != null,
      '中奖的结果屏上没有粒子'
    )
    assert.equal(
      document
        .querySelector('[data-slot="alert"]')
        ?.getAttribute('data-outcome'),
      'won'
    )
  })

  test('摇中但已发完 → 说清是哪一档、为什么落空；不能塌成「谢谢参与」', async () => {
    const screen = await mountQyWheelScreen({
      element: (
        <QyWheelSpinDialog
          activity={ACTIVITY}
          open
          onOpenChange={() => {}}
          canSpinAgain={false}
        />
      ),
      respond: (req) => {
        if (req.url.includes('/pay-password')) return PAY_PASSWORD_STATUS
        if (req.method === 'POST' && req.url.endsWith('/spins')) {
          return {
            ...WON,
            entry_no: 'LE-exhausted-1',
            ppm: 464_548,
            result_tier: 0,
            exhausted_tier: 2,
            amount: 0,
            prize_type: 'none',
          } satisfies QyWheelSpinResult
        }
        return undefined
      },
    })
    assert.ok(await screen.click(zhKeys.qy_lot_wheel_spin_confirm))

    const text = screen.text()
    assert.ok(
      text.includes(withName('qy_lot_wheel_result_exhausted', '兑换码')),
      `没说清摇中了哪一档但已发完：${text}`
    )
    assert.ok(text.includes(zhKeys.qy_lot_wheel_exhausted_note))
    // 「谢谢参与」四个字在盘面图例里合法地出现（它是一个扇区），所以这里断的是
    // 结果标题：那一行必须是"摇中但已发完"，不能是"谢谢参与"。
    const title = document.querySelector('[data-slot="alert-title"]')
    assert.ok(title != null, '结果屏上没有结论那一行')
    assert.equal(
      (title.textContent ?? '').trim(),
      withName('qy_lot_wheel_result_exhausted', '兑换码')
    )
    assert.ok(
      !text.includes(zhKeys.qy_lot_wheel_spin_again),
      '活动已不受理新转动时不该给「再转一次」'
    )
    // 落空不庆祝:粒子只属于中奖那一屏。
    assert.equal(document.querySelector('[data-testid="qy-burst"]'), null)
    assert.equal(
      document
        .querySelector('[data-slot="alert"]')
        ?.getAttribute('data-outcome'),
      'exhausted'
    )
  })

  test('种子不合规时提交键禁用，并把后端那条判据的文案摆出来', async () => {
    const screen = await mountQyWheelScreen({
      element: (
        <QyWheelSpinDialog
          activity={ACTIVITY}
          open
          onOpenChange={() => {}}
          canSpinAgain
        />
      ),
      respond: (req) =>
        req.url.includes('/pay-password') ? PAY_PASSWORD_STATUS : undefined,
    })
    const seed = screen.field(zhKeys.qy_lot_wheel_seed_label)
    assert.ok(seed != null)
    await screen.type(seed, 'has|pipe')
    assert.equal(
      screen.button(zhKeys.qy_lot_wheel_spin_confirm)?.disabled,
      true,
      '带 | 的种子会破坏链原像的编码，不能让它发出去'
    )
    assert.ok(screen.text().includes(zhKeys.qy_lot_err_bad_client_seed))

    // 空串合法（按空分量进原像）：清空之后必须能提交。
    await screen.type(seed, '')
    assert.equal(
      screen.button(zhKeys.qy_lot_wheel_spin_confirm)?.disabled,
      false
    )
    assert.equal(
      screen.sent.filter((row) => row.method === 'POST').length,
      0,
      '这一条只看按钮状态，不该有任何一次提交'
    )
  })
})
