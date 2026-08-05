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
import { useQyConfig } from '../hooks/use-qy-config'

/**
 * 渠道列表页该渲染哪一套批量按钮。
 *
 * # 为什么需要这个开关，而不是无条件替换
 *
 * 上游渠道页本来就有「批量启用 / 批量停用 / 批量删除」三个按钮，它们能用，
 * 只是回的是一个整数、说不清哪几条失败。qy 这一套是它们的替代品而不是补充：
 * 两套同时渲染会得到两个长得一样的删除按钮，而"两个删除按钮里哪个是对的"
 * 是没人应该被迫回答的问题。
 *
 * 所以按扩展开关二选一：
 *
 *   扩展启用   → 渲染 qy 的四个按钮（启用 / 停用 / 重置统计 / 删除）
 *   扩展关闭   → 原样回落到上游那三个按钮，功能一个不少
 *
 * 取 `enabled` 而不是 `status === 'enabled'` 是刻意的：`status` 的 `unknown`
 * 档（首屏取数中 / 后端 503）下 `enabled` 会沿用本地快照，页面因此不会在刷新时
 * 先闪一套上游按钮再换成 qy 的。真正说了算的永远是后端 —— 扩展关着时
 * `/api/qy/admin/channels/*` 一律 404，点了也只会得到一句"功能未启用"。
 */
export function useQyChannelBulkVisible(): boolean {
  return useQyConfig().enabled
}
