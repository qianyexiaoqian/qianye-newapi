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
  /** `pending_exists` | `account_too_new` | `group_blocked` | `''`。 */
  blocked_reason: string
  group_policy: QyTransferGroupPolicy
}

/**
 * 当前用户的分组划转策略（`qianye/modules/transfer/grouprule.go`）。
 *
 * 只包含**发起方自己**的策略，绝不含其他人的分组归属。后端刻意把
 * `allow_all` 与「没有规则」都收敛成 `unrestricted`：对用户而言两者完全等价，
 * 区分它们只会多一种解释不清的文案。
 *
 * `my_group` 为空串表示后端没能读到用户主库行，此时不应渲染任何分组提示 ——
 * 拿一个不属于他的分组的规则去提示，比不提示更糟。
 */
export type QyTransferGroupPolicy = {
  policy: 'allow_list' | 'blocked' | 'deny_list' | 'unrestricted'
  my_group: string
  /** 仅 `allow_list` 有意义；后端已把 `@self` 解析成真实分组名。 */
  allowed_groups: string[]
  /** 仅 `deny_list` 有意义。 */
  denied_groups: string[]
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
