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
import type { QyStatus } from '../../lib/types'

/**
 * 提现模块 DTO。对应 `qianye/modules/withdraw/{api_user,api_admin,view}.go`。
 *
 * **所有法币金额都是 string**：`decimal(18,6)` / `decimal(18,8)` 用 JSON number
 * 传会在前端丢位。前端只做展示与字符串比较，禁止 `parseFloat` 后再运算。
 */

/**
 * 提现方式。
 *
 * **它说的不是"系统怎么给钱",而是用户要的是哪种钱** —— 两种方式都由管理员
 * 手动发放,系统只负责扣佣金与记账。`quota` = 站内额度,`fiat` = 现金。
 */
export type QyWithdrawMethod = 'fiat' | 'quota'

/** 收款渠道。白名单写死在后端 `withdraw/validate.go`，配置改不了。 */
export type QyPayeeChannel =
  | 'alipay'
  | 'bank'
  | 'paypal'
  | 'usdt_trc20'
  | 'wechat'
  | (string & {})

export type QyWithdrawFiatConfig = {
  currency: string
  /** decimal 字符串，空串表示不设下限。 */
  min_amount: string
  fee_bps: number
  payee_channels: QyPayeeChannel[]
  /** 仅供预览。真正生效的是提交那一刻冻结进单据的汇率。 */
  preview_quota_per_unit?: string
  preview_fx_rate?: string

  /**
   * 凭证图片开关。后端 `Withdraw.ProofOn()` = `methods 含 fiat && proof_enabled`，
   * 所以这三项只在本段里出现，且一旦本段存在就一定同时下发（`handleGetConfig`）。
   */
  proof_enabled: boolean
  /** 单张字节上限。**用于在选文件那一刻拦住超大图**，而不是让用户等一次 413。 */
  proof_max_bytes: number
  /** 后端 `ProofAcceptMimes()`，直接拼进 `<input accept>`。真正的判定是服务端魔数。 */
  proof_accept: string[]
}

/**
 * `POST /withdraw/proofs` 的返回。
 *
 * `ref` 是提交申请时要放进 `proof_ref` 的凭证标识。它在被某张单认领之前一直有效，
 * 但**未认领的上传只保留 24 小时**（后端 `proofOrphanSeconds`），过期后会被清理任务
 * 连文件带元数据一起删掉，此时再拿它提交会得到 `qy_wd_proof_not_found`。
 */
export type QyWithdrawProof = {
  ref: string
  mime_type: string
  size: number
  created_at: number
}

export type QyWithdrawConfig = {
  methods: QyWithdrawMethod[]
  min_quota: number
  remark_max_runes: number
  daily_max_count: number
  used_today: number
  payee_account_max: number
  review_sla_hours: number
  /**
   * 发放时限：审核通过之后多久没标记已发放算积压。0 = 不计时限。
   *
   * 用户端展示它是为了给一个诚实的预期：提现**不会自动到账**，审核通过之后
   * 还要等管理员手动发放。只写审核时限会让用户以为通过即到账。
   */
  payout_sla_hours: number
  withdrawable_quota: number
  /** 只有开启了 fiat 方式才有这一段。 */
  fiat?: QyWithdrawFiatConfig
}

/** 已保存的收款方式。永远只有脱敏值。 */
export type QyPayeeAccount = {
  ref: string
  channel: QyPayeeChannel
  label: string
  masked: string
  created_at: number
}

/** 单据事件，用来渲染时间线。`action` 是稳定英文标识，文案由前端 i18n。 */
export type QyWithdrawEvent = {
  action: string
  from_status: string
  to_status: string
  actor_type: string
  actor_name: string
  reason: string
  /** 仅管理端视图有值。 */
  detail?: string
  created_at: number
}

/**
 * 提现单（用户视图）。
 *
 * 需求原文的三个问题在这里闭环：
 *   什么时候拒绝的 → `reviewed_at`；拒绝理由 → `reject_reason`；
 *   什么时候发的钱 → `paid_at`（配合 `payout_ref`）。
 */
export type QyWithdrawal = {
  id: number
  withdraw_no: string
  method: QyWithdrawMethod
  status: QyStatus
  quota: number

  currency: string
  frozen_quota_per_unit: string
  frozen_fx_rate: string
  gross_amount: string
  fee_amount: string
  net_amount: string
  fee_bps: number

  payee_channel: string
  payee_masked: string
  remark: string
  /**
   * 本单**附过**凭证图片。不代表现在还下载得到 —— 单据被拒绝/撤销/打款失败，
   * 或已过 `pii_retention_days`，图片都会被清掉，此时下载接口回 `qy_wd_proof_purged`。
   */
  has_proof: boolean

  reviewed_at: number
  reject_reason: string
  paid_at: number
  /** 发放凭证。fiat 是流水号/txid，quota 是管理员那次手工加额度的操作记录标识。 */
  payout_ref: string
  fail_reason: string

  created_at: number
  updated_at: number

  events?: QyWithdrawEvent[]
}

/** 管理端视图：在用户视图之上补齐排障、风控与 SLA 字段。 */
export type QyAdminWithdrawal = QyWithdrawal & {
  user_id: number
  username: string
  /** 非空表示命中风控，目前只有 `shared_payee`（收款账号被多个账号用过）。 */
  risk_flags: string
  reviewer_id: number
  reviewer_name: string
  payout_operator_id: number
  payout_operator_name: string
  payout_note: string
  client_ip: string
  /** 后端算好的截止时间与超时标记，前端不自行推导，避免客户端时钟偏差误标红。 */
  sla_deadline: number
  sla_breached: boolean
  /**
   * 上面那对字段说的是哪一道时限：`review` = 待审、`payout` = 待发放、
   * 空串 = 终态不计时。
   *
   * 一张单在任一时刻只处在一道时限里，所以两者共用同一对字段。文案必须按它
   * 分岔：管理员看到"超时"却不知道该去审核还是去发钱，等于没提示 ——
   * 而这两件事在组织上往往根本不是同一个人。
   */
  sla_kind: string
}

export type QyWithdrawCreateRequest = {
  client_request_id: string
  method: QyWithdrawMethod
  quota: number
  remark: string
  /** 复用已保存的收款方式，与 `payee` 二选一。 */
  payee_ref?: string
  payee_channel?: string
  payee?: Record<string, string>
  /**
   * 先经 `POST /withdraw/proofs` 拿到的凭证 ref，可选。
   *
   * **只能出现在 `method === 'fiat'` 的请求里**：后端 `acceptCreate` 对 quota 单带
   * `proof_ref` 是直接报错而不是静默忽略的（`qy_wd_proof_disabled`）。
   */
  proof_ref?: string

  /**
   * 支付密码。后端 `handleCreate` 在**进事务之前**用它跑一次 `paypass.Require`，
   * 不通过就直接 403，一张单都不会落库、一分佣金都不会冻结。
   *
   * 必填而不是可选：漏传与传空串在后端是同一种结果（`qy_pay_pwd_required`），
   * 但把它标成可选会让下一个新增调用点以为"不带也行"。
   * 用户没设过支付密码时后端返回 `qy_pay_pwd_not_set` 并要求先去设置 ——
   * 提现是钱离开站点的那条路，不存在"没设过就放行"这个档位。
   */
  pay_password: string
}

/** 审核队列角标。 */
export type QyWithdrawStats = {
  buckets: { status: string; count: number; quota: number }[]
  /** 待审超时数（`review_sla_hours`，从提交起算）。 */
  sla_breached: number
  /**
   * 待发放积压数（`payout_sla_hours`，从审核通过起算）。
   *
   * 这是人工发放模型**新引入**的观测点：佣金在申请那一刻就离开了用户的可用池，
   * 通过之后没人发钱，用户就是"钱扣了、东西没拿到"。
   */
  payout_sla_breached: number
}

/** 收款信息明文。只能通过带审计的专用接口获取。 */
export type QyPayeePlain = {
  channel: QyPayeeChannel
  masked: string
  payee: Record<string, string>
  withdraw_no: string
}
