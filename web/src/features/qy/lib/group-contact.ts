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
import { isSafeExternalLinkUrl } from '@/features/system-settings/integrations/utils'

/**
 * 首屏群聊卡片的两个配置项。
 *
 * 放在 `features/qy/lib` 而不是落地页自己的 lib 下：**管理端设置页**也要用同一套
 * 判据做提交前校验，让上游的设置页 import 进一个落地页的私有目录是错的形状。
 *
 * ── 为什么前端还要再净化一次 ──
 *
 * 后端已经在落库前和 `/api/status` 出口各挡过一次。这一道守的是另外两件事：
 * 库里可能躺着经直接改库写进来的值，而 `/api/status` 还有一份 localStorage 快照
 * 会跨版本活下来。判据与后端同源，样本集也与后端用例逐条对齐。
 */
export const QY_HOME_GROUP_JOIN_URL_KEY = 'QyHomeGroupJoinUrl'
export const QY_HOME_GROUP_NUMBER_KEY = 'QyHomeGroupNumber'

/**
 * 链接长度上界，与后端同值。**这是安全项**：`qrcode.react` 在超出容量时抛的是
 * 渲染期 `RangeError`，而路由根挂着 `errorComponent`，于是一个配置值能把整个
 * 前端外壳换成通用错误页，对每一个匿名访客都如此。
 */
const JOIN_URL_MAX_LENGTH = 512

/**
 * 控制符与双向排版符。
 *
 * 不是冗余：`new URL()` 按 WHATWG 规范会**静默剥掉**换行与制表符并判为合法，
 * 而 Go 的 `url.Parse` 会拒。这条正则把前端补齐到后端的严格度，避免两侧判据分家。
 */
const UNSAFE_CHARS =
  // eslint-disable-next-line no-control-regex
  /[\u0000-\u001f\u007f\u200e\u200f\u202a-\u202e\u2066-\u2069]/

const GROUP_NUMBER = /^[0-9A-Za-z._-]{3,32}$/

export function sanitizeQyGroupJoinUrl(value: unknown): string {
  if (typeof value !== 'string') return ''
  const trimmed = value.trim()
  if (!trimmed || trimmed.length > JOIN_URL_MAX_LENGTH) return ''
  if (UNSAFE_CHARS.test(trimmed)) return ''
  return isSafeExternalLinkUrl(trimmed) ? trimmed : ''
}

export function sanitizeQyGroupNumber(value: unknown): string {
  if (typeof value !== 'string') return ''
  const trimmed = value.trim()
  return GROUP_NUMBER.test(trimmed) ? trimmed : ''
}

/**
 * 从 `/api/status` 读出这张卡片要用的两个值。
 *
 * 两项净化后都为空就返回 `null` —— 首屏据此回落到模型信号板，所以全新装的站
 * 或者配置被清空时那一格不会开天窗。**判据是「净化之后还剩什么」，不是「后台填了
 * 什么」**，于是非法值与没配走同一条兜底路径，只有一条路要测。
 */
export function readQyGroupContact(
  status: Record<string, unknown> | null | undefined
): { joinUrl: string; number: string } | null {
  const joinUrl = sanitizeQyGroupJoinUrl(status?.qy_home_group_join_url)
  const number = sanitizeQyGroupNumber(status?.qy_home_group_number)
  if (!joinUrl && !number) return null
  return { joinUrl, number }
}
