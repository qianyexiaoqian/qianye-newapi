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
import type { ReactNode } from 'react'

/**
 * qy 扩展的共享类型。
 *
 * 与后端 `qianye/controller/config.go`、`qianye/model/fund_order.go` 以及各
 * `qianye/modules/<mod>/api_*.go` 的响应体一一对应。各功能页面自己的 DTO 放在
 * `features/qy/<page>/types.ts`，本文件只收敛"所有页面都要用"的部分。
 */

// ───────────────────────────── 响应信封 ─────────────────────────────

/**
 * 后端统一信封。失败时额外带 `code`（见 `qianye/guard/guard.go`）。
 *
 * `success` 是判定信封的唯一标志：扩展未注册时请求会落到上游 NoRoute，
 * 返回的 `{"error":{...}}` 甚至 HTML 都没有这个字段。
 */
export type QyEnvelope<T> = {
  success: boolean
  message?: string
  code?: string
  data?: T
}

/**
 * 列表分页信封。
 *
 * 后端各模块统一返回 `{items, total}`，多数还带 `p` / `page_size`
 * （违规模块的几个列表只返回前两个字段，所以后两者可选）。
 */
export type QyPage<T> = {
  items: T[]
  total: number
  p?: number
  page_size?: number
}

// ───────────────────────────── 引导端点 ─────────────────────────────

/** YAML 功能开关。字段名与 `config.go` 的 `features` 段完全一致。 */
export type QyFeatures = {
  transfer: boolean
  commission: boolean
  withdraw: boolean
  availability: boolean
  violation: boolean
  lottery: boolean
  ticket: boolean
}

/** 钱包页三个入口卡的显隐开关。 */
export type QyWalletEntries = {
  show_transfer_entry: boolean
  show_commission_entry: boolean
  show_withdraw_entry: boolean
}

/** 日志页扩展列的显隐开关。 */
export type QyLogMetricsOptions = {
  show_reasoning_effort: boolean
  show_cache_ratio: boolean
  enable_filter: boolean
}

/** 提现方式。`quota` = 站内额度兑换，`fiat` = 线下法币打款。 */
export type QyWithdrawMethod = 'fiat' | 'quota'

export type QyWithdrawOptions = {
  methods: QyWithdrawMethod[]
  fiat_currency: string
  remark_max_runes: number
}

/**
 * 划转表单的预校验参数。
 *
 * `recipient_lookup` 限定收款人查找方式（后端刻意不提供用户名模糊搜索，
 * 否则等于开放用户枚举）。
 */
export type QyTransferOptions = {
  min_quota: number
  max_per_tx_quota: number
  recipient_lookup: string
}

/**
 * 抽奖/竞猜的两个展示开关（需求原文：「系统设置前端是否显示」）。
 *
 * 与 `features.lottery` 是**并列**关系而不是二选一：`features.lottery` 是
 * YAML 里"这个功能装没装"，`show_entry` 是站点"这一期要不要在前台露出入口"。
 * 关掉 `show_entry` 只隐藏用户侧入口，管理端照常可进 —— 否则关掉之后就再也
 * 没有地方能把它打开了。
 *
 * `proof_public` 决定证据链端点是否允许匿名访问。前端据此决定要不要把
 * 「把这个链接发给任何人都能自己验」这句话显示出来：关掉时那句话是假的。
 */
export type QyLotteryOptions = {
  show_entry: boolean
  proof_public: boolean
}

/**
 * 归一化后的引导端点响应。
 *
 * 后端在 `enabled=false` 时只返回 `{enabled, available}` 两个字段，因此原始
 * 响应是"部分对象"。`normalizeQyConfig()` 会补齐成本类型，让所有调用方都能
 * 无条件读 `config.features.transfer`，不必到处写可选链。
 */
export type QyConfig = {
  enabled: boolean
  available: boolean
  features: QyFeatures
  wallet: QyWalletEntries
  log_metrics: QyLogMetricsOptions
  withdraw_options: QyWithdrawOptions
  transfer_options: QyTransferOptions
  lottery: QyLotteryOptions
}

/** 引导端点的原始响应形状（字段可能缺失）。 */
export type QyConfigPayload = {
  enabled?: boolean
  available?: boolean
  features?: Partial<QyFeatures>
  wallet?: Partial<QyWalletEntries>
  log_metrics?: Partial<QyLogMetricsOptions>
  withdraw_options?: Partial<QyWithdrawOptions>
  transfer_options?: Partial<QyTransferOptions>
  lottery?: Partial<QyLotteryOptions>
}

// ───────────────────────────── 状态机 ─────────────────────────────

/**
 * 全站统一的单据状态取值。
 *
 * `uncertain` 是资金系统的"我不知道，交给人"出口（裁定文档 C12），前端必须用
 * 告警色而不是失败色 —— 钱可能已经动了，不能让用户以为一定没成功。
 *
 * 联合里保留 `(string & {})` 是刻意的：后端日后新增状态时前端只应降级为中性
 * 徽章，绝不能因为拿到未知字符串而崩溃。
 */
export type QyStatus =
  | 'approved'
  | 'cancelled'
  | 'failed'
  | 'frozen'
  | 'paid'
  | 'paying'
  | 'pending'
  | 'processing'
  | 'rejected'
  | 'reversed'
  | 'success'
  | 'uncertain'
  | (string & {})

// ───────────────────────────── 时间线 ─────────────────────────────

/**
 * 单据时间线的一个节点。提现历史、佣金结算这类多段流程用它渲染。
 *
 * `state` 决定视觉：`done` 实心、`current` 高亮 + 呼吸、`pending` 灰显、
 * `failed` 用失败色。未到达的节点必须保留占位而不是不渲染 ——
 * 用户需要知道"后面还有几步"。
 */
export type QyTimelineItem = {
  key: string
  title: string
  description?: ReactNode
  /** unix 秒。`0` / 缺省表示尚未发生，显示为 `-`。 */
  timestamp?: number
  state?: 'current' | 'done' | 'failed' | 'pending'
}

/** 资金单业务类型，取自 `qianye/model/fund_order.go` 的 kind 常量。 */
export type QyFundOrderKind =
  | 'commission_reverse'
  | 'commission_settle'
  | 'lottery_entry'
  | 'lottery_payout'
  | 'transfer'
  | 'withdraw_fiat'
  | 'withdraw_quota'
  | (string & {})

/**
 * 跨库两阶段资金单（`qy_fund_orders`）。
 *
 * `status` 是 int8 而不是字符串：0 待定 / 2 成功 / 3 失败 / 4 不可判定 / 5 已冲正。
 * 用 `qyFundOrderStatusName()` 转成 {@link QyStatus} 再交给徽章渲染。
 */
export type QyFundOrder = {
  id: number
  order_no: string
  kind: QyFundOrderKind
  status: number
  idem_scope: string
  idem_key: string
  user_id: number
  peer_user_id: number
  amount_quota: number
  fee_quota: number
  ref_type: string
  ref_id: string
  attempts: number
  next_probe_at: number
  last_error: string
  node_name: string
  created_at: number
  updated_at: number
  settled_at: number
}
