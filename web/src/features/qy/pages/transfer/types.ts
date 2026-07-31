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
 * 划转模块的 DTO。字段与 `qianye/modules/transfer/handler.go` 的响应体一一对应。
 *
 * 所有金额都是 quota 整数：后端刻意不接受浮点美元金额，浮点换算的舍入分歧会
 * 直接变成对不上的账。
 */

/** 收款人查找方式。后端 `config/validate.go` 只有这两档，**没有用户名模糊搜索**。 */
export type QyRecipientLookup = 'id' | 'id_or_email' | (string & {})

/**
 * `GET /api/qy/transfer/limits`。
 *
 * 前四个是静态门槛，`remaining_*` / `cooldown_until` / `transferable_quota`
 * 是当前用户的实时状态 —— 后端算好下发，前端不自行推导（客户端时钟与服务端
 * 自然日的边界不一致，本地算会在跨日时刻给出错误的剩余额度）。
 */
export type QyTransferLimits = {
  min_quota: number
  max_per_tx_quota: number
  daily_max_quota: number
  daily_max_count: number
  fee_bps: number
  fee_min_quota: number
  cooldown_seconds: number
  recipient_lookup: QyRecipientLookup
  remaining_daily_quota: number
  remaining_daily_count: number
  /** unix 秒。大于当前时间表示仍在冷却中。 */
  cooldown_until: number
  transferable_quota: number
  /** `pending_exists` | `account_too_new` | `''`。 */
  blocked_reason: string
}

/** `POST /api/qy/transfer/preview` 的请求体。 */
export type QyTransferPreviewRequest = {
  identifier: string
  /** 传 0 表示只解析收款人、不回填金额明细。 */
  amount: number
}

/**
 * 预校验结果。
 *
 * `masked_username` / `masked_email` **已由后端脱敏**，前端只做展示。
 * `blocked_reason` 取值：`not_found` | `self` | `disabled`。
 */
export type QyTransferPreview = {
  exists: boolean
  user_id: number
  masked_username: string
  masked_email: string
  receivable: boolean
  blocked_reason: string
  amount: number
  fee_quota: number
  total: number
}

/** `POST /api/qy/transfer` 的请求体。 */
export type QyTransferCreateRequest = {
  to_user_id: number
  amount: number
  remark: string
  /** 幂等键。打开确认弹窗时生成，重试沿用同一个（裁定 C10）。 */
  client_request_id: string
  /** 服务端二次确认标记，后端 `validateCreate` 缺它直接 400。 */
  confirm: true
}

export type QyTransferCreated = {
  order_no: string
  status: string
  amount: number
  fee_quota: number
  to_user_id: number
  to_masked_username: string
  my_quota_after: number
  created_at: number
}
