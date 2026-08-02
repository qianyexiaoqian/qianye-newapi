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
 * 三个审计视图共用的非组件部分。
 *
 * 与 `filters.tsx` 分文件是 fast-refresh 的硬要求(oxlint
 * `react(only-export-components)`):一个文件里既导出组件又导出常量/函数时,
 * 改常量会让整棵组件树丢状态,而这一页的状态就是管理员刚填好的筛选条件。
 */

/** 三个审计视图共用的每页条数。 */
export const QY_AUDIT_PAGE_SIZE = 20

/** 下拉筛选里的「全部」哨兵值。空串在 Select 里会被当成未选中。 */
export const QY_AUDIT_ALL = 'all'

/** `trim()` 后为空则返回 undefined,避免把空串当成筛选条件发给后端。 */
export function qyAuditTrimmed(value: string): string | undefined {
  const text = value.trim()
  return text === '' ? undefined : text
}
