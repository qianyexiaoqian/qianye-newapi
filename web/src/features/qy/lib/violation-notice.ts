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
 * 「我的违规记录」页顶部的三条横幅公告。
 *
 * 超级管理员在系统设置「站点信息」里填写,经 `/api/status` 的
 * `qy_violation_notices` 下发(后端已去掉空条与校验不过的条目,按 1、2、3 排好)。
 * 判据与后端 `operation_setting.ValidateQyViolationNotice` 同源:单条最多 500 字。
 */
export const QY_VIOLATION_NOTICE_KEYS = [
  'QyViolationNotice1',
  'QyViolationNotice2',
  'QyViolationNotice3',
] as const

export const QY_VIOLATION_NOTICE_MAX_CHARS = 500

/**
 * 从 status 里取出要展示的横幅。
 *
 * 后端已经筛过,这里再防一层:`/api/status` 有 localStorage 快照,旧版本缓存里
 * 可能没有这个字段或形状不对;字段缺失、不是数组、元素不是字符串都按「没有」处理,
 * 最多取 3 条。内容完全相同的条目只留一条:同一句话挂两遍没有意义,去重之后
 * 正文本身就能当渲染 key。
 */
export function qyViolationNotices(
  status: Record<string, unknown> | null | undefined
): string[] {
  const raw = status?.qy_violation_notices
  if (!Array.isArray(raw)) return []
  const notices = raw
    .filter((item): item is string => typeof item === 'string')
    .map((item) => item.trim())
    .filter((item) => item !== '')
  return [...new Set(notices)].slice(0, QY_VIOLATION_NOTICE_KEYS.length)
}
