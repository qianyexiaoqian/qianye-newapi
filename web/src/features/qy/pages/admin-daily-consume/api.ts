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

import { api } from '@/lib/api'

import { QY_API_PREFIX, qyErrorFromBlobFailure, qyGet } from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'
import type { QyDailyConsumePage, QyDailyConsumeSort } from './types'

export type QyDailyConsumePaging = {
  p: number
  page_size: number
}

/** 导出与列表共用的筛选条件（不含分页）。 */
export type QyDailyConsumeExportFilters = {
  /** yyyymmdd。两个都留空 = 昨日（后端的缺省口径）。 */
  start_date?: string
  end_date?: string
  sort: QyDailyConsumeSort
  order: 'asc' | 'desc'
  /**
   * 一个框同时搜用户名 / id / 邮箱。
   *
   * 与「用户佣金」那一页同一个理由：运营手上只有“某个人的某一个标识”，
   * 逼他先分类就会得到一个与“这个人真的没消费”长得一样的空列表。
   * 纯数字优先按 id 精确匹配由后端做。
   */
  keyword?: string
}

/** 列表接口的完整参数：筛选 + 分页。 */
export type QyDailyConsumeFilters = QyDailyConsumeExportFilters &
  QyDailyConsumePaging

/**
 * 把筛选拼成 query，空值一律不发（省得 URL 里全是 `=`）。
 *
 * 分页是可选的：导出走的是**整个筛选结果**而不是某一页，那条路径根本不该
 * 带 `p` / `page_size`。
 */
function dailyConsumeQuery(
  filters: QyDailyConsumeExportFilters & Partial<QyDailyConsumePaging>
) {
  const query: Record<string, unknown> = {
    sort: filters.sort,
    order: filters.order,
  }
  if (filters.p != null) query.p = filters.p
  if (filters.page_size != null) query.page_size = filters.page_size
  if (filters.start_date) query.start_date = filters.start_date
  if (filters.end_date) query.end_date = filters.end_date
  if (filters.keyword) query.keyword = filters.keyword
  return query
}

/**
 * 日消费明细。对应 `GET /api/qy/admin/commission/daily-consume`。
 *
 * 数据源是主库 `logs`（type=2），**不是**计佣表 —— 0% 分组、没有邀请关系、
 * 违规扣费、渠道测试这四类用户在计佣表里一行都没有，只读计佣表的报表会让
 * 他们凭空消失，而这张表恰恰是给“谁在花钱”用的。
 */
export function qyAdminDailyConsumeQuery(filters: QyDailyConsumeFilters) {
  const query = dailyConsumeQuery(filters)
  return queryOptions({
    queryKey: qyKeys.adminDailyConsume(query),
    queryFn: () =>
      qyGet<QyDailyConsumePage>('/admin/commission/daily-consume', query),
  })
}

/**
 * 导出 CSV。
 *
 * 走鉴权接口 + Blob 而不是 `<a href>` 直链：这条路由要管理员身份，直链缺
 * Bearer 会直接 401，而浏览器下载失败不会有任何可见提示。与违规命中导出、
 * 工单附件下载是同一套写法。
 *
 * 不能用 `qyGet`：那条路会把响应体当 `{success,data}` 信封解，而这里的成功
 * 响应就是 CSV 文本本身；失败时 axios 给回的 `response.data` 也是 Blob，
 * 所以错误还原走 `qyErrorFromBlobFailure`。
 *
 * 刻意**不带分页参数**:导出的是整个筛选结果,而不是当前这一页。行数上界与
 * 列表接口共用后端那一个 —— 能看的才能导,一行都不多。
 */
export async function exportQyDailyConsume(
  filters: QyDailyConsumeExportFilters
): Promise<Blob> {
  const query = dailyConsumeQuery(filters)
  try {
    const res = await api.get(
      `${QY_API_PREFIX}/admin/commission/daily-consume/export`,
      {
        skipErrorHandler: true,
        skipBusinessError: true,
        responseType: 'blob',
        params: query,
        // 上游的在途 GET 去重只按 url + params 归并，认不出 responseType 的差异。
        disableDuplicate: true,
      }
    )
    return res.data as Blob
  } catch (error) {
    throw await qyErrorFromBlobFailure(error)
  }
}
