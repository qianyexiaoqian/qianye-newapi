/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
/*
 * 一整族判据共用一个 code 时，后端那句话就是答案。
 *
 * # 抓到的形状
 *
 * `qy_lot_bad_request` 一个 code 背后是抽奖模块 **96 处** `errBadRequest`，
 * 每一处都写着是哪一格错了、上限是多少、换算成站内余额是多少钱。而
 * `qyErrorMessage` 命中白名单就 `return t(known)`，把那句话整段丢掉，运营在
 * 创建向导上只看到一句「请求参数不合法」，然后去改别的字段。
 *
 * 也别指望"从白名单里删掉它"能救：`kindFromStatus` 把 400 归到 `invalid`，
 * 兜底文案是「请求参数有误，请检查后重试」——信息量完全相同。所以必须显式
 * 让这一类 code 走 rawMessage。
 *
 * 实测漏网的三条（客户端 `qyLotValidateDraft` 拦不住、输入控件却允许输入）：
 *   rules.max_entries_per_user = 1000 → 「每人参与/尝试上限不得超过 500」
 *   标题 80 个汉字（输入框 maxLength=120）→ 「活动标题必填且不超过 60 个字」
 *   奖档名 60 个汉字（输入框 maxLength=80）→ 「奖档名称必填且不超过 40 个字」
 */
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import zhKeys from '@/i18n/qy/zh.json'

import {
  QY_ERROR_CODE_I18N,
  QY_SERVER_MESSAGE_CODES,
  QyError,
  qyErrorMessage,
} from '../api'

// qyErrorMessage 只用到 TFunction 的 (key) => string 这一支。
const translate = (key: string) =>
  (zhKeys as Record<string, string>)[key] ?? key
const t = translate as never

describe('后端那句话就是答案的那一类 code', () => {
  test('qy_lot_bad_request 原样显示后端 message', () => {
    for (const raw of [
      '每人参与/尝试上限不得超过 500',
      '活动标题必填且不超过 60 个字',
      '奖品数量不得超过 50000 份(与全场参与上限同一个硬顶)',
    ]) {
      const error = new QyError('invalid', 'qy_lot_bad_request', raw, 400)
      assert.equal(
        qyErrorMessage(error, t),
        raw,
        '后端精心写的字段名与上限被一句通用文案盖掉了'
      )
    }
  })

  test('后端没给 message 时才回落到静态文案', () => {
    const error = new QyError('invalid', 'qy_lot_bad_request', null, 400)
    assert.equal(qyErrorMessage(error, t), zhKeys['qy_lot_err_bad_request'])
    const blank = new QyError('invalid', 'qy_lot_bad_request', '', 400)
    assert.equal(qyErrorMessage(blank, t), zhKeys['qy_lot_err_bad_request'])
  })

  test('不在这一类里的 code 仍然走白名单译文', () => {
    // 反面约束：这次改动不能变成"所有 code 都直接显示中文原文"。
    const error = new QyError(
      'conflict',
      'qy_lot_status_conflict',
      '活动状态已变化',
      409
    )
    assert.equal(qyErrorMessage(error, t), zhKeys['qy_lot_err_status_conflict'])
  })

  test('这一类 code 的静态回落文案必须仍在语言包里', () => {
    for (const code of QY_SERVER_MESSAGE_CODES) {
      const key = QY_ERROR_CODE_I18N[code]
      assert.ok(key != null, `${code} 掉出了白名单，没有回落文案`)
      assert.ok(key in zhKeys, `${key} 不在 zh 语言包里`)
    }
  })
})
