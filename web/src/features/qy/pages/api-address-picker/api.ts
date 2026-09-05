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
import { queryOptions } from '@tanstack/react-query'

import { isQyError, qyGet } from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'

/** 用户可选的一条 API 地址。字段是后端 `userView` 的白名单。 */
export type QyApiAddressOption = {
  id: number
  name: string
  remark: string
  url: string
  /**
   * 控制台「API信息」卡片的圆点颜色；空串 = 前端默认色。选择弹窗不消费它。
   * 可选而不是必填：升级瞬间 react-query 缓存里还躺着旧版响应，消费方必须
   * 容忍缺失（`?? ''`），而 getBgColorClass 对空值本来就有兜底。
   */
  color?: string
}

/**
 * 展示位置。调用方必须声明自己是谁 —— 过滤在服务端做（`?surface=`），
 * 后端 `allowedSurfaces` 是这份取值的唯一权威。
 */
export type QyAddressSurface = 'picker' | 'console'

/**
 * 用户侧地址清单的取数定义。
 *
 * ── 为什么「扩展未启用」不能抛出去 ──
 * 消费方是**上游**密钥列表上的「复制链接信息」与控制台「API信息」卡片。
 * 这些入口在扩展关掉时必须原样可用（回落到站点地址 / 整块卡片收起），
 * 所以 404 / feature_off 在这里被翻译成空数组，而不是让 react-query 进
 * error 态 —— 后者会让上游页面为一个它根本不知道存在的扩展弹一个红色报错。
 *
 * 503 / 网络错误**保持**抛出：那时"到底配没配地址"是未知的，调用方据此选择
 * 打开选择窗口(带错误态)而不是悄悄用站点地址替用户做决定。
 */
export function qyApiAddressesQuery(surface: QyAddressSurface) {
  return queryOptions({
    queryKey: qyKeys.apiAddresses(surface),
    queryFn: async (): Promise<QyApiAddressOption[]> => {
      try {
        const data = await qyGet<{ items: QyApiAddressOption[] }>(
          `/api-addresses?surface=${surface}`
        )
        return data.items
      } catch (error) {
        if (isQyError(error) && error.isHidden) return []
        throw error
      }
    },
    retry: false,
    staleTime: 5 * 60 * 1000,
  })
}
