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
/**
 * `blocked_reason` → i18n key。
 *
 * 后端在两处下发这个字段且取值不重叠：
 *   - `preview` 回 `not_found` / `self` / `disabled` / `group_denied`（收款人侧）
 *   - `limits`  回 `pending_exists` / `account_too_new` / `group_blocked`（发起人侧）
 *
 * 合成一张表是因为两者最终都渲染成同一个位置的一行红字，拆两张只会让调用方
 * 记不清该查哪一张。未知取值返回 `null`，调用方回落到通用文案而不是显示裸 key。
 *
 * `group_denied` 与 `group_blocked` 必须是两条不同的文案：前者是「换个收款人
 * 也许就行」，后者是「换谁都不行」。塌缩成一句，用户只会不停重试。
 */
const BLOCKED_REASON_I18N: Record<string, string> = {
  not_found: 'qy_tr_blk_not_found',
  self: 'qy_tr_blk_self',
  disabled: 'qy_tr_blk_disabled',
  pending_exists: 'qy_tr_blk_pending_exists',
  account_too_new: 'qy_tr_blk_account_too_new',
  group_denied: 'qy_tr_blk_group_denied',
  group_blocked: 'qy_tr_blk_group_blocked',
}

export function qyTransferBlockedKey(
  reason: string | null | undefined
): string | null {
  if (reason == null || reason === '') return null
  return BLOCKED_REASON_I18N[reason] ?? null
}
