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
 * 已发布活动的「改名称」：弹窗提交的是 `PUT …/basics {title, intro}`，长度按 rune
 * 与后端同一口径；详情页只对 published / locked 挂这颗按钮（草稿走编辑向导）。
 *
 * 与「草稿改不了」是同一种缺陷形状：后端接口一直在，界面上点不到就等于没有。
 * 这里从两头钉：弹窗真的发出那条 PUT，详情页真的按状态挂了按钮。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { after, describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { QY_ERROR_CODE_I18N } from '../../../lib/api'
import {
  cleanupQyWheelScreens,
  mountQyWheelScreen,
  zhKeys,
} from '../../wheel/__tests__/wheel-harness'
import type { QyLotAdminActivity } from '../types'

const { QyLotBasicsDialog } =
  await import('../components/lottery-basics-dialog')

after(async () => {
  await cleanupQyWheelScreens()
})

const dir = dirname(fileURLToPath(import.meta.url))
const detailSource = readFileSync(join(dir, '..', 'detail.tsx'), 'utf8')

const PUBLISHED: QyLotAdminActivity = {
  act_no: 'LT-1',
  kind: 'draw',
  draw_mode: 'wheel',
  status: 'published',
  outcome: '',
  title: '原标题',
  intro: '原说明',
  stake_quota: 100,
  open_at: 1_800_000_000,
  close_at: 1_800_003_600,
  draw_at: 1_800_007_200,
  settle_deadline: 1_800_086_400,
  commit_hash: 'a'.repeat(64),
  rules_hash: 'rh',
  spec_hash: 'sh',
  algo: 'lot-v2',
  rules_text: '{}',
  allow_multi_win: true,
  fee_bps: 0,
  min_entries_to_hold: 0,
  max_entries_per_user: 0,
  max_attempts_per_user: 0,
  max_total_entries: 0,
  max_total_users: 0,
  max_per_inviter: 0,
  cooldown_seconds: 0,
  dedup_ip: false,
  bet_min_quota: 0,
  bet_max_quota: 0,
  entry_seq: 0,
  active_count: 0,
  pool_quota: 0,
  platform_fee_quota: 0,
  payout_quota: 0,
  refund_quota: 0,
  roster_hash: '',
  roster_count: 0,
  chain_head: '',
  win_option_id: 0,
  result_evidence: '',
  result_by: 0,
  cancel_reason: '',
  created_by: 1,
  created_at: 1_799_000_000,
  published_at: 1_799_500_000,
  locked_at: 0,
  revealed_at: 0,
  settled_at: 0,
  hidden_at: 0,
  hidden_by: 0,
  hidden_reason: '',
}

describe('改名称', () => {
  test('改了标题之后保存：PUT 到这一场的 /basics，请求体是裁过空白的标题与原样的说明', async () => {
    const screen = await mountQyWheelScreen({
      element: (
        <QyLotBasicsDialog activity={PUBLISHED} open onOpenChange={() => {}} />
      ),
      respond: (req) =>
        req.method === 'PUT'
          ? { act_no: 'LT-1', title: '新标题', intro: '原说明' }
          : undefined,
      path: '/qy/admin/lottery',
    })
    // 一个字都没改时不该能保存：一次空 PUT 也会写一条事件行。
    assert.equal(
      screen.button(zhKeys.qy_lot_basics_confirm)?.disabled,
      true,
      '没改动时「保存」应当禁用'
    )
    const title = screen.field(zhKeys.qy_lot_title_field)
    assert.ok(title != null, '弹窗里没有标题输入框')
    await screen.type(title, '  新标题  ')
    assert.ok(await screen.click(zhKeys.qy_lot_basics_confirm))

    const puts = screen.sent.filter((row) => row.method === 'PUT')
    assert.equal(puts.length, 1, `应当恰好提交一次：${puts.length}`)
    assert.ok(
      puts[0].url.endsWith('/admin/lottery/activities/LT-1/basics'),
      `发到了别的路径：${puts[0].url}`
    )
    assert.deepEqual(puts[0].body, { title: '新标题', intro: '原说明' })
  })

  test('标题超过 60 字时「保存」禁用', async () => {
    const screen = await mountQyWheelScreen({
      element: (
        <QyLotBasicsDialog activity={PUBLISHED} open onOpenChange={() => {}} />
      ),
      respond: () => undefined,
      path: '/qy/admin/lottery',
    })
    const title = screen.field(zhKeys.qy_lot_title_field)
    assert.ok(title != null)
    await screen.type(title, '字'.repeat(61))
    assert.equal(screen.button(zhKeys.qy_lot_basics_confirm)?.disabled, true)
    assert.equal(screen.sent.filter((row) => row.method === 'PUT').length, 0)
  })

  test('详情页只对 published / locked 挂「改名称」，草稿仍走编辑向导；错误码已登记', () => {
    assert.ok(
      detailSource.includes("t('qy_lot_basics_change')"),
      '详情页上没有「改名称」按钮'
    )
    assert.match(
      detailSource,
      /const canEditBasics =\s*activity\?\.status === 'published' \|\| activity\?\.status === 'locked'/,
      '「改名称」的判据必须是 published / locked（草稿另有编辑向导，结束后后端 409）'
    )
    assert.equal(
      QY_ERROR_CODE_I18N.qy_lot_basics_locked,
      'qy_lot_err_basics_locked'
    )
    assert.ok(
      'qy_lot_err_basics_locked' in zhKeys &&
        'qy_lot_event_basics_changed' in zhKeys,
      '错误码与事件行的文案要有'
    )
  })
})
