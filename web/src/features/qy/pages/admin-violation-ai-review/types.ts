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
 * AI 审核的前端类型。
 *
 * ── 密钥在这里只有两种形态 ──
 * 读:`has_key`(有没有)与 `key_hint`(掩码,尾 4 位)。**没有任何一个字段
 * 承载明文密钥** —— 后端的列表视图是白名单结构体,连密文都不下发。
 * 写:`api_key` 是可选字段,`undefined` 表示"这次不动密钥",空串表示"清除"。
 * 两者必须能分开,否则"改一下模型名"就会把密钥抹掉,而抹掉之后不可恢复。
 */

/** 一次审核调用的结局。除 clean / violation 外全部是失败,而失败一律放行。 */
export type QyAiOutcome =
  | 'clean'
  | 'violation'
  | 'timeout'
  | 'bad_json'
  | 'upstream_error'
  | 'no_channel'

export type QyAiChannel = {
  id: number
  name: string
  base_url: string
  model: string
  has_key: boolean
  key_hint: string
  timeout_ms: number
  weight: number
  enabled: boolean
  price_in_per_m: string
  price_out_per_m: string
  remark: string
  updated_at: number
}

export type QyAiChannelInput = {
  name: string
  base_url: string
  model: string
  /** 省略 = 保持原密钥;空串 = 清除。绝不能把它做成必填。 */
  api_key?: string
  timeout_ms: number
  weight: number
  enabled: boolean
  price_in_per_m: string
  price_out_per_m: string
  remark: string
}

export type QyAiChannelList = {
  items: QyAiChannel[]
  /** 后端有没有配 violation.ai_review_key。没配就存不下密钥。 */
  key_configured: boolean
}

export type QyAiSetting = {
  id: number
  enabled: boolean
  /** 万分比:3000 = 30%。 */
  sample_rate_bps: number
  pre_timeout_ms: number
  async_timeout_ms: number
  prompt: string
  max_input_chars: number
  third_party_notice_ack: boolean
}

export type QyAiSettingResponse = {
  setting: QyAiSetting
  default_prompt: string
  categories: string[]
  key_configured: boolean
  effective: {
    /** 快照里**真正生效**的那一份,不是表单回显。两者不同时界面必须说出来。 */
    active: boolean
    channels: number
    pre_rules: boolean
    post_async_rules: boolean
    pre_timeout_hint: string
    max_pre_timeout: number
    max_async_timeout: number
  }
}

export type QyAiStatsRow = {
  outcome: QyAiOutcome
  count: number
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  cost_usd: string
}

export type QyAiStats = {
  days: number
  by_outcome: QyAiStatsRow[]
  total_calls: number
  total_tokens: number
  total_cost_usd: string
  violated_calls: number
  /** 发出去了但算不出钱的调用数(渠道没填单价)。> 0 时总额是被低估的。 */
  unpriced_calls: number
}

export type QyAiReviewLog = {
  id: number
  review_no: string
  user_id: number
  username: string
  phase: string
  channel_name: string
  review_model: string
  outcome: QyAiOutcome
  violated: boolean
  category: string
  confidence: string
  reason: string
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  cost_usd: string
  latency_ms: number
  rule_id: number
  record_id: number
  request_id: string
  model_name: string
  using_group: string
  created_at: number
}

export type QyAiChannelTestResult = {
  outcome: QyAiOutcome
  violated: boolean
  category: string
  confidence: string
  latency_ms: number
  tokens: { prompt: number; completion: number; total: number }
  cost_usd: string
  priced: boolean
  message?: string
}
