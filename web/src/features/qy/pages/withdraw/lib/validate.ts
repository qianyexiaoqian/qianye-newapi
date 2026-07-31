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
import { qyRuneLength } from '../../lib/constants'
import type { QyPayeeChannel } from '../types'
import { qyPayeeSpec } from './payee-spec'

/**
 * 与后端 `acceptPayee` 的 `strings.ContainsAny(v, "\x00\x1e\x1f\n\r")` 逐字对齐。
 *
 * 用字面量数组而不是正则：控制字符在正则里既触发 lint 也难以一眼看出到底拦了哪几个，
 * 而这里的取值必须与后端完全一致 —— 前端多拦一个字符只会让用户莫名其妙填不进去。
 */
const REJECTED_CONTROL_CHARS = ['\u0000', '\u001E', '\u001F', '\n', '\r']

/**
 * 收款信息的本地校验。
 *
 * 与后端 `acceptPayee` 同口径（按 rune 计长度、拒绝控制字符），存在的意义只是
 * 让用户不必提交一次才知道卡号少填了一位 —— **后端的那一遍校验才是防线**，
 * 这里放宽或收紧都不会影响安全性。
 *
 * 返回 `字段 key → i18n key` 的错误表，空表示通过。
 */
export function validateQyPayee(
  channel: QyPayeeChannel,
  values: Record<string, string>
): Record<string, string> {
  const errors: Record<string, string> = {}
  const spec = qyPayeeSpec(channel)
  if (spec.length === 0) {
    return { _channel: 'qy_wd_err_channel_required' }
  }

  for (const field of spec) {
    const raw = (values[field.key] ?? '').trim()
    if (raw === '') {
      if (field.required) errors[field.key] = 'qy_wd_err_field_required'
      continue
    }
    const length = qyRuneLength(raw)
    if (length < field.minRunes || length > field.maxRunes) {
      errors[field.key] = 'qy_wd_err_field_length'
      continue
    }
    // 与后端一致地拒绝控制字符：它们会破坏收款信息指纹的分隔符语义。
    if (REJECTED_CONTROL_CHARS.some((char) => raw.includes(char))) {
      errors[field.key] = 'qy_wd_err_field_invalid'
    }
  }
  return errors
}

/** 提交前把值收敛到规格内的键，并去掉首尾空白。多余的键 = 多一份 PII 落库。 */
export function normalizeQyPayee(
  channel: QyPayeeChannel,
  values: Record<string, string>
): Record<string, string> {
  const out: Record<string, string> = {}
  for (const field of qyPayeeSpec(channel)) {
    const raw = (values[field.key] ?? '').trim()
    if (raw !== '') out[field.key] = raw
  }
  return out
}
