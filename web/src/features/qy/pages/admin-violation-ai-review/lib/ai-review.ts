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
import type { QyAiChannel, QyAiChannelInput } from '../types'

/**
 * AI 审核页的纯逻辑。抽出来是因为这里有两处**不能靠肉眼保证**的转换:
 * 抽样率的百分比 ↔ 万分比,以及密钥的"不动 / 清除 / 换新"三态。
 * 两者都有单测(`__tests__/ai-review.test.ts`)。
 */

/** 抽样率:万分比 → 显示用的百分比文本。3000 → "30",3050 → "30.5"。 */
export function qyAiBpsToPercentText(bps: number): string {
  if (!Number.isFinite(bps) || bps <= 0) return '0'
  const pct = bps / 100
  return Number.isInteger(pct) ? String(pct) : String(Number(pct.toFixed(2)))
}

/**
 * 抽样率:百分比文本 → 万分比整数,并夹进 0..10000。
 *
 * 夹紧而不是原样透传:后端会 400 拒绝越界值,但界面上先夹住能让"我填了 200%"
 * 立刻显示成 100%,而不是点了保存才收到一句报错。空串与非数字一律回 0
 * (= 不抽样),那是**不花钱**的那一侧 —— 解析失败绝不能落到"全量送审"。
 */
export function qyAiPercentTextToBps(text: string): number {
  const n = Number.parseFloat(String(text).trim())
  if (!Number.isFinite(n) || n <= 0) return 0
  return Math.min(10000, Math.round(n * 100))
}

/** 每天大概花多少钱的粗估,给抽样率旁边那句提示用。 */
export function qyAiDailyCostEstimate(input: {
  dailyRequests: number
  sampleRateBps: number
  avgCostUsdPerCall: number
}): number {
  const { dailyRequests, sampleRateBps, avgCostUsdPerCall } = input
  if (dailyRequests <= 0 || sampleRateBps <= 0 || avgCostUsdPerCall <= 0) {
    return 0
  }
  return (dailyRequests * sampleRateBps * avgCostUsdPerCall) / 10000
}

/** 渠道表单的草稿形态。`apiKey` 为 null 表示"这次不动密钥"。 */
export type QyAiChannelDraft = {
  name: string
  base_url: string
  model: string
  /** null = 不动;'' = 清除;其它 = 换成这一把。 */
  apiKey: string | null
  timeout_ms: number
  weight: number
  enabled: boolean
  price_in_per_m: string
  price_out_per_m: string
  remark: string
}

export function qyAiChannelToDraft(ch?: QyAiChannel): QyAiChannelDraft {
  return {
    name: ch?.name ?? '',
    base_url: ch?.base_url ?? 'https://api.deepseek.com/v1',
    // 默认值只填地址与模型名,**密钥永远留空** —— 本仓不预置任何密钥。
    model: ch?.model ?? 'deepseek-v4-flash',
    apiKey: null,
    timeout_ms: ch?.timeout_ms ?? 0,
    weight: ch?.weight ?? 1,
    enabled: ch?.enabled ?? false,
    price_in_per_m: ch?.price_in_per_m ?? '0',
    price_out_per_m: ch?.price_out_per_m ?? '0',
    remark: ch?.remark ?? '',
  }
}

/**
 * 草稿 → 请求体。**这个函数唯一的难点是密钥的三态**。
 *
 * `apiKey === null` 时请求体里**根本不带 `api_key` 这个键**(不是带一个
 * `undefined`,也不是带空串):后端按"字段缺失 = 保持原密钥"处理,而空串
 * 是"显式清除"。把三态压成两态的代价是每次编辑都静默清掉密钥,
 * 而清掉之后不可恢复 —— 运营得回去找第三方重新签发。
 */
export function qyAiDraftToInput(draft: QyAiChannelDraft): QyAiChannelInput {
  const body: QyAiChannelInput = {
    name: draft.name.trim(),
    base_url: draft.base_url.trim(),
    model: draft.model.trim(),
    timeout_ms: draft.timeout_ms,
    weight: draft.weight,
    enabled: draft.enabled,
    price_in_per_m: draft.price_in_per_m.trim() || '0',
    price_out_per_m: draft.price_out_per_m.trim() || '0',
    remark: draft.remark,
  }
  if (draft.apiKey !== null) {
    body.api_key = draft.apiKey
  }
  return body
}
