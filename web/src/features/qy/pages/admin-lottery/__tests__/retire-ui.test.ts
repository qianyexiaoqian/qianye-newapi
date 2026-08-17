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
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { QY_ERROR_CODE_I18N } from '../../../lib/api'

/**
 * 「关闭」「删除」「取消」三个按钮的界面契约。
 *
 * ## 为什么按源码守
 *
 * 这三个动作的代价天差地别 —— 取消**会退钱**，下架与删除**都不退钱**，
 * 而删除还会永久毁掉这一场的公正性证明。它们并排出现在同一屏上，
 * 分辨它们的唯一依据是弹窗里那几句话。
 *
 * 这类缺陷没有任何运行期症状：把「下架」的说明改成一句「确定吗」，
 * typecheck 全绿、渲染测试全绿、接口照常 200 —— 直到某个运营以为「关闭」
 * 会退钱（或者以为「取消」只是藏起来），而那两个方向的误解都真的会花钱。
 * 所以把「文案里必须出现这几件事」本身变成断言。
 *
 * ## 抓不到什么
 *
 * 它只看键有没有被引用、文案里有没有那几个字，不看排版好不好读。
 * 职责是防止这几层解释整段消失，不是证明它们写得好。
 */

const dir = dirname(fileURLToPath(import.meta.url))
const read = (...p: string[]) => readFileSync(join(dir, '..', ...p), 'utf8')

const deleteDialog = read('components', 'lottery-delete-dialog.tsx')
const hideDialog = read('components', 'lottery-hide-dialog.tsx')
const detail = read('detail.tsx')
const api = read('api.ts')

const zh = JSON.parse(
  readFileSync(
    join(dir, '..', '..', '..', '..', '..', 'i18n', 'qy', 'zh.json'),
    'utf8'
  )
) as Record<string, string>
const en = JSON.parse(
  readFileSync(
    join(dir, '..', '..', '..', '..', '..', 'i18n', 'qy', 'en.json'),
    'utf8'
  )
) as Record<string, string>

describe('删除的二次确认必须说清代价', () => {
  test('确认框逐条列出四种代价，而不是一句「确定删除吗」', () => {
    for (const key of [
      'qy_lot_delete_warn_proof',
      'qy_lot_delete_warn_money',
      'qy_lot_delete_warn_rows',
      'qy_lot_delete_warn_audit',
    ]) {
      assert.ok(
        deleteDialog.includes(`t('${key}')`),
        `删除确认框里少了 ${key} 这一条代价`
      )
      assert.ok(zh[key] != null && en[key] != null, `${key} 缺 zh/en 文案`)
    }
  })

  test('文案里写明「证据链永久消失」与「奖金不会追回」', () => {
    assert.match(zh.qy_lot_delete_warn_proof, /无法再验证|永久/)
    assert.match(zh.qy_lot_delete_warn_money, /不会追回/)
    assert.match(en.qy_lot_delete_warn_proof, /verify/i)
    assert.match(en.qy_lot_delete_warn_money, /clawed back|refund/i)
  })

  test('必须原样输入活动编号才能按下删除', () => {
    assert.ok(
      deleteDialog.includes(
        'const matched = confirmActNo.trim() === props.activity.act_no'
      ),
      '删除按钮不再要求回填活动编号 —— 那就退化成了一个「确定吗」'
    )
    assert.ok(
      deleteDialog.includes('disabled={!matched ||'),
      '编号没对上时删除按钮必须是禁用的'
    )
    // 前端这一层只是省一次往返，服务端还会再判一次；两边都在才算数。
    assert.ok(
      api.includes('confirm_act_no'),
      '请求体里必须带 confirm_act_no，服务端要拿它再校验一次'
    )
  })

  test('删完之后要离开这一页：那一行已经不存在了', () => {
    assert.ok(
      deleteDialog.includes("navigate({ to: '/qy/admin/lottery' })"),
      '删除成功后留在详情页只会得到一个 404'
    )
  })
})

describe('「关闭」与「取消」在界面上必须分得开', () => {
  test('下架弹窗的标题就写着「不是取消、一分钱都不会退」', () => {
    assert.ok(hideDialog.includes("t('qy_lot_hide_warn_title')"))
    assert.match(zh.qy_lot_hide_warn_title, /不是取消/)
    assert.match(zh.qy_lot_hide_warn_title, /不会退/)
    assert.match(en.qy_lot_hide_warn_title, /not a cancellation/i)
  })

  test('取消弹窗仍然写着它会全额退款', () => {
    assert.match(zh.qy_lot_cancel_warn_title, /退款/)
    assert.match(zh.qy_lot_cancel_confirm, /退款/)
  })

  test('下架说明写明它不遮详情、我的参与与公正查询', () => {
    assert.match(zh.qy_lot_hide_warn_desc, /我的参与/)
    assert.match(zh.qy_lot_hide_warn_desc, /公正查询|证据/)
    assert.match(en.qy_lot_hide_warn_desc, /My entries/i)
  })
})

describe('入口只对已结束的场次出现', () => {
  test('下架与删除都挂在 isFinished 上', () => {
    assert.ok(
      detail.includes("const isFinished = activity?.status === 'finished'"),
      '判据必须只有这一处 —— 抄第二份必然与后端闸门漂移'
    )
    for (const key of ['qy_lot_hide_title', 'qy_lot_delete_title']) {
      assert.ok(detail.includes(`t('${key}')`), `详情页缺少 ${key} 按钮`)
    }
  })

  test('已下架的场次在列表与详情上都有徽标', () => {
    const list = read('index.tsx')
    assert.ok(
      list.includes("t('qy_lot_hidden_badge')"),
      '列表上分不出哪几场已下架，运营只能逐个点进去看'
    )
    assert.ok(detail.includes("t('qy_lot_hidden_badge')"))
    assert.ok(detail.includes("t('qy_lot_unhide_title')"), '下架必须可撤回')
  })
})

describe('六道硬闸门的拒绝文案前端都有', () => {
  test('后端每个 code 都登记了映射，并且两种语言都有文案', () => {
    // 没登记的 code 会回落成按 HTTP 状态码归类的泛化文案 —— 而这八个全是 409/400，
    // 塌成一句「操作冲突」之后，运营看不出该等出款落定、该去发兑换码、还是该先
    // 关闭双色球系列。这正是这一整层存在的理由。
    for (const code of [
      'qy_lot_delete_not_finished',
      'qy_lot_delete_funds_open',
      'qy_lot_delete_text_pending',
      'qy_lot_delete_entry_open',
      'qy_lot_delete_flag_open',
      'qy_lot_delete_series_live',
      'qy_lot_delete_evidence_broken',
      'qy_lot_delete_confirm',
      'qy_lot_delete_audit_off',
      'qy_lot_hide_not_finished',
      'qy_lot_hide_already',
      'qy_lot_hide_not_hidden',
    ]) {
      const key = QY_ERROR_CODE_I18N[code]
      assert.ok(key != null, `${code} 没有登记进 QY_ERROR_CODE_I18N`)
      assert.ok(zh[key] != null, `缺少 ${key} 的中文文案`)
      assert.ok(en[key] != null, `缺少 ${key} 的英文文案`)
    }
  })
})
