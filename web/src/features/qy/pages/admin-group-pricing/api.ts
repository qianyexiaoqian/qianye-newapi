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

import { qyGet, qyPost, qyPut, qyDelete } from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'
import type {
  QyGpPreviewInput,
  QyGpPreviewResult,
  QyGpRule,
  QyGpRuleInput,
  QyGpRulesPage,
  QyGpShadowResponse,
} from './types'

/**
 * 分组定价的管理端取数。
 *
 * 路径全部集中在这一个文件，与 `qianye/modules/grouppricing/grouppricing.go` 的
 * `RegisterAdminRoutes` 一一对应。
 */

const QY_GP_BASE = '/admin/group-pricing'

export type QyGpRulesParams = {
  p: number
  page_size: number
  group_name?: string
  model_name?: string
  mode?: string
}

/**
 * 规则列表。每一项都带后端算好的 `effective`。
 *
 * `staleTime: 0`：改完价立刻要能核对折算结果，「刚保存完列表里还显示旧价」
 * 在一个直接决定扣多少钱的页面上不可接受。
 */
export function qyGpRulesQuery(params: QyGpRulesParams) {
  return queryOptions({
    queryKey: qyKeys.adminGroupPricingRules(params),
    queryFn: () => qyGet<QyGpRulesPage>(`${QY_GP_BASE}/rules`, params),
    staleTime: 0,
  })
}

export function qyCreateGpRule(body: QyGpRuleInput) {
  return qyPost<QyGpRule>(`${QY_GP_BASE}/rules`, body)
}

export function qyUpdateGpRule(id: number, body: QyGpRuleInput) {
  return qyPut<QyGpRule>(`${QY_GP_BASE}/rules/${id}`, body)
}

export function qyDeleteGpRule(id: number) {
  return qyDelete<{ deleted: number }>(`${QY_GP_BASE}/rules/${id}`)
}

/**
 * 只读试算：运营边打边看最终生效价。
 *
 * 折算**必须**走后端这个接口而不是前端自己乘：前端再实现一遍相乘，两处只要有
 * 一处漏乘分组倍率或多舍一位，管理端显示的数字就与实际扣费不一致。
 * 它同时会回传后端的 `warning`（例如「该模型当前按次计价，ratio 覆盖不会生效」），
 * 那是前端凭一份模型名单根本判断不出来的。
 *
 * `user_group` 必填。后端缺省时直接 400 并说明原因，因此调用侧必须先确保
 * 用户选了一个用户分组再发请求，而不是塞一个默认值蒙混过去 ——
 * 那样得到的是一个「看起来精确」的错数字。
 */
export function qyPreviewGpRule(body: QyGpPreviewInput) {
  return qyPost<QyGpPreviewResult>(`${QY_GP_BASE}/preview`, body)
}

/**
 * 影子差额对账。区间参数是 unix 秒的 `start` / `end`，后端上限 31 天。
 *
 * 区间由前端显式传而不是靠后端默认值：「这个月会多收多少」这句话的答案完全
 * 取决于区间，含糊的默认值会让两个人看着同一个页面得出不同结论。
 */
export function qyGpShadowQuery(params: { start: number; end: number }) {
  return queryOptions({
    queryKey: qyKeys.adminGroupPricingShadow(params),
    queryFn: () =>
      qyGet<QyGpShadowResponse>(`${QY_GP_BASE}/shadow/summary`, params),
    staleTime: 30_000,
  })
}

// ─────────────────────────── 下拉候选（上游端点）───────────────────────────

/** `/api/pricing` 里本页用得上的那部分。其余字段与定价页无关，不声明。 */
type QyGpPricingPayload = {
  data?: { model_name?: string }[]
  group_ratio?: Record<string, number>
}

export type QyGpOptions = {
  /** 分组名，按字典序。 */
  groups: string[]
  /** 模型名，按字典序。 */
  models: string[]
}

/**
 * 分组与模型的候选清单。
 *
 * 取自上游 `/api/pricing` —— 它同时给出分组倍率表的 key 与全部模型名，
 * 与后端 `computeEffective` 读的 `ratio_setting` 是同一份数据。
 *
 * **这两份清单只是输入辅助，不参与任何计算。** 页面上每一个价格数字都来自
 * 后端的 `effective`，所以即使清单过期或取不到，也不会有任何数字是错的 ——
 * 最坏情况只是下拉里少了一项，表单退化成自由输入。因此这里刻意压掉上游拦截器
 * 的报错弹窗：一个辅助清单挂了不该在改价页面上糊一片红。
 */
export function qyGpOptionsQuery() {
  return queryOptions({
    queryKey: qyKeys.adminGroupPricingOptions(),
    queryFn: async (): Promise<QyGpOptions> => {
      const res = await api.get('/api/pricing', {
        skipErrorHandler: true,
        skipBusinessError: true,
      })
      const payload = (res.data ?? {}) as QyGpPricingPayload
      const models = (payload.data ?? [])
        .map((item) => item.model_name ?? '')
        .filter((name) => name !== '')
      return {
        groups: Object.keys(payload.group_ratio ?? {}).sort(),
        models: [...new Set(models)].sort(),
      }
    },
    staleTime: 5 * 60_000,
    retry: false,
  })
}
