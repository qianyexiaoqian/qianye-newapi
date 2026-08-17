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
import { QY_API_PREFIX } from '../../../lib/api'

/**
 * 活动封面的两种来源 → 一个可以直接放进 `<img src>` 的地址。
 *
 * ## 为什么封面能用 `<img src>`，而工单截图不能
 *
 * 工单截图那条接口要 Bearer 头，浏览器给 `img` 发的请求不带它，所以那边只能
 * 走 axios 取 Blob 再 `createObjectURL`。封面走的是**匿名**端点
 * （`GET /api/qy/lottery/covers/:ref`，只回已经用在某场活动上的那些），
 * 因此可以直接交给浏览器：大厅首屏是十几张卡片并排，每张都走一次 XHR + 一个
 * blob URL + 一次撤销的代价太大，而且 blob 拿不到任何 HTTP 缓存。
 *
 * 匿名成立的三条前提写在后端 `api_admin_cover.go` 的文件头上，其中最关键的一条是：
 * 封面本来就是面向所有访客的招贴，不含任何一个用户的私人信息。
 */
export type QyLotCoverSource = {
  cover_url?: string
  cover_ref?: string
}

/** 一张封面的来源分类。外链要挂 `no-referrer`，站内引用不需要。 */
export type QyLotCoverKind = 'link' | 'none' | 'upload'

export function qyLotCoverKind(activity: QyLotCoverSource): QyLotCoverKind {
  if ((activity.cover_ref ?? '') !== '') return 'upload'
  if ((activity.cover_url ?? '') !== '') return 'link'
  return 'none'
}

/**
 * 封面地址。没配封面时返回 `null` —— 调用方据此渲染兜底图案，**绝不**渲染一个
 * `src=""` 的 `<img>`（那在多数浏览器里会立刻发一次指向当前页面的请求，
 * 并画出一个破图图标）。
 *
 * 上传引用优先于外链：后端保证两者互斥，但库里那两列是可以被手工改坏的，
 * 而"站内的那一份"是两者中唯一可验证的来源。
 *
 * `encodeURIComponent` 不是装饰：`ref` 来自接口响应，而把一段未编码的字符串
 * 拼进 URL 路径是这一整类问题里最便宜的一个洞。
 */
export function qyLotCoverSrc(activity: QyLotCoverSource): string | null {
  const ref = activity.cover_ref ?? ''
  if (ref !== '') {
    return `${QY_API_PREFIX}/lottery/covers/${encodeURIComponent(ref)}`
  }
  const url = (activity.cover_url ?? '').trim()
  // 只放行 http/https。后端在写入时已经判过一次，这里再判一次是因为这个值
  // 会被直接交给浏览器：库里那一列可以被手工改坏，而 `<img src>` 之外
  // 这个字符串将来还可能被放进 `<a href>` 或 CSS `url()`，那两处 `javascript:`
  // 是真的会执行的。判定放在唯一的出口上，比指望每个新调用点都记得判要稳。
  if (/^https?:\/\//i.test(url)) return url
  return null
}
