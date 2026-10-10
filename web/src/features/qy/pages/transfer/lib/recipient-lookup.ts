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
import type { QyRecipientBy, QyTransferPreview } from '../types'

/**
 * 收款人输入框的提示随 `recipient_lookup` 变化。
 *
 * 划转表单与通讯录「添加」框走的是同一套后端解析，必须显示同一句话 ——
 * 两处各写一遍三元表达式，迟早一处多一档、一处少一档。
 */
export type QyRecipientInputHints = {
  inputMode: 'numeric' | 'text'
  placeholderKey: string
  helpKey: string
}

export function qyRecipientInputHints(mode: string): QyRecipientInputHints {
  switch (mode) {
    case 'id_or_email':
      return {
        inputMode: 'text',
        placeholderKey: 'qy_tr_recipient_ph_id_email',
        helpKey: 'qy_tr_recipient_help_id_email',
      }
    case 'id_or_username':
      return {
        inputMode: 'text',
        placeholderKey: 'qy_tr_recipient_ph_id_username',
        helpKey: 'qy_tr_recipient_help_id_username',
      }
    default:
      return {
        inputMode: 'numeric',
        placeholderKey: 'qy_tr_recipient_ph_id',
        helpKey: 'qy_tr_recipient_help_id',
      }
  }
}

/** 预校验命中了不止一个人（纯数字既是某人的 ID、又是另一个人的用户名）。 */
export function qyIsAmbiguousPreview(preview: QyTransferPreview | null) {
  return (
    preview != null &&
    preview.blocked_reason === 'ambiguous' &&
    (preview.candidates?.length ?? 0) > 0
  )
}

/**
 * 用户选定的查找方式，**与当时的输入绑在一起**。
 *
 * 在候选里点了「#3」之后，输入框变成 `3`、并以 `by=id` 预校验。之后用户只要
 * 改了输入框，这个选择就作废 —— 否则「上次选了按 ID」会悄悄套到另一串数字上。
 * 用一个 `{identifier, by}` 对比当前值，而不是靠"改动时清空"的事件时序：
 * 后者在 setValue 与手动输入之间分不清，而这里分错就是转错人。
 */
export type QyPinnedRecipient = { identifier: string; by: QyRecipientBy }

export function qyPinnedBy(
  pinned: QyPinnedRecipient | null,
  identifier: string
): QyRecipientBy | undefined {
  if (pinned == null) return undefined
  return pinned.identifier === identifier.trim() ? pinned.by : undefined
}
