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
import type { QyPayeeChannel } from '../types'

/**
 * 各收款渠道的字段规格。
 *
 * **必须与后端 `qianye/modules/withdraw/validate.go` 的 `payeeSpecs` 逐字对齐**：
 * 后端会丢弃规格外的键并按 rune 数校验长度，前端多问一个字段只会让用户白填，
 * 少问一个必填字段则会在提交时才报 `qy_wd_payee_required`。
 *
 * 长度按**字符**计（后端用 `utf8.RuneCountInString`），所以校验时必须用
 * `qyRuneLength` 而不是 `.length`。
 */
export type QyPayeeFieldSpec = {
  key: string
  required: boolean
  minRunes: number
  maxRunes: number
  /** i18n key，展示用的字段名。 */
  labelKey: string
}

const ALIPAY_LIKE: QyPayeeFieldSpec[] = [
  {
    key: 'real_name',
    required: true,
    minRunes: 1,
    maxRunes: 64,
    labelKey: 'qy_wd_f_real_name',
  },
  {
    key: 'account',
    required: true,
    minRunes: 4,
    maxRunes: 64,
    labelKey: 'qy_wd_f_account',
  },
]

export const QY_PAYEE_SPECS: Record<string, QyPayeeFieldSpec[]> = {
  alipay: ALIPAY_LIKE,
  wechat: ALIPAY_LIKE,
  bank: [
    {
      key: 'real_name',
      required: true,
      minRunes: 1,
      maxRunes: 64,
      labelKey: 'qy_wd_f_real_name',
    },
    {
      key: 'bank_name',
      required: true,
      minRunes: 2,
      maxRunes: 64,
      labelKey: 'qy_wd_f_bank_name',
    },
    {
      key: 'account_no',
      required: true,
      minRunes: 6,
      maxRunes: 32,
      labelKey: 'qy_wd_f_account_no',
    },
    {
      key: 'branch',
      required: false,
      minRunes: 0,
      maxRunes: 64,
      labelKey: 'qy_wd_f_branch',
    },
  ],
  usdt_trc20: [
    {
      key: 'address',
      required: true,
      minRunes: 26,
      maxRunes: 64,
      labelKey: 'qy_wd_f_address',
    },
  ],
  paypal: [
    {
      key: 'email',
      required: true,
      minRunes: 5,
      maxRunes: 128,
      labelKey: 'qy_wd_f_email',
    },
  ],
}

/**
 * 「本次现填」在收款方式单选组里的哨兵值。
 *
 * 用一个不可能与 `ref`（32 位十六进制 UUID）冲突的字面量，而不是空串：
 * 空串会与"还没选"混淆，而这两种状态触发的是完全不同的提交分支。
 */
export const QY_PAYEE_NEW = '__new__'

/** 未知渠道返回空数组：后端新增渠道时前端最多"填不了"，不能白屏。 */
export function qyPayeeSpec(channel: QyPayeeChannel): QyPayeeFieldSpec[] {
  return QY_PAYEE_SPECS[channel] ?? []
}

/** 渠道名的 i18n key。渠道标识由后端下发，中文名一律走前端翻译。 */
export function qyPayeeChannelKey(channel: QyPayeeChannel): string {
  return `qy_wd_ch_${channel}`
}
