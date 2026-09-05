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
 * 邀请管理（管理端）的 DTO。对应 `qianye/modules/invite/api_admin_relation.go`
 * 与 `api_admin_accrual.go`（D-14：从 commission 模块搬家，关系接口形状照旧、
 * 路径前缀换成 `/admin/invite`）。
 *
 * ── 权威字段是主库的 `users.inviter_id` ──
 * 扩展库的 `qy_invite_relation` 只是**懒建**的展示快照：某个下线第一次触发
 * 邀请返时才会有那一行。所以列表（绑定中）由后端从主库出，`snapshot_present`
 * 为假只是说明"这个人还没触发过邀请返"，不是异常。
 */
export type QyInviteRelation = {
  invitee_id: number
  invitee_username: string
  /** 假值表示主库里查不到这个 id（账号已删，或这一次主库读失败）。 */
  invitee_resolved: boolean

  inviter_id: number
  inviter_username: string
  inviter_resolved: boolean

  /** 扩展库快照记下的绑定时刻。0 = 还没有快照行，此时回落显示注册时间。 */
  bound_at: number
  /** 下线的注册时间。自动绑定发生在注册那一刻，所以它同时是大多数关系的绑定时间。 */
  invitee_created_at: number
  /** 大于 0 表示这条关系已被管理员解绑，只保留历史。 */
  unbound_at: number

  /** 这一对（邀请人 × 被邀请人）累计返给邀请人的星屑（各 kind 合计，整数）。 */
  total_stardust: number

  snapshot_present: boolean
  blocked: boolean
  /**
   * **自动风控**写的标记（目前只有 `reciprocal_invite`：A 邀 B 且 B 又邀 A）。
   * 由后端 `ensureRelation` 在建快照时算出，人工停/恢复不会覆盖它。
   */
  risk_flags: string
  /**
   * 管理员最近一次停止/恢复计返填的事由。
   *
   * 空串 = 从没填过（或这条关系从没被人工动过），**不是**"事由是空的"。
   * 它与 `blocked` 完全正交：恢复时事由照样留下，所以不能拿它推断开关状态。
   */
  block_reason: string
}

export type QyInviteRelationPage = {
  items: QyInviteRelation[]
  total: number
  p: number
  page_size: number
}

/** 绑定中 / 已解绑。后端 `adminListRelations` 用 `scope` 参数区分两个数据源。 */
export const QY_RELATION_SCOPES = ['bound', 'unbound'] as const
export type QyRelationScope = (typeof QY_RELATION_SCOPES)[number]

/**
 * 列表排序口径，与后端 `relationSortOrders` / `unboundSortOrders` 的键逐字一致。
 *
 * 两张表的排序列不同（主库按 users.created_at，快照按 unbound_at），但键名是同一套，
 * 所以前端只有一个下拉。
 */
export const QY_RELATION_SORTS = [
  'newest',
  'oldest',
  'invitee',
  'inviter',
] as const
export type QyRelationSort = (typeof QY_RELATION_SORTS)[number]

export type QyBindRelationResult = {
  invitee_id: number
  inviter_id: number
  bound: boolean
}

/**
 * 解绑 / 换绑的返回。`kept_stardust` 是**留在旧上线名下**的已发放星屑 ——
 * 两个动作都不收回任何已发放的星屑，这个数字就是那句话的量化形式。
 */
export type QyUnbindRelationResult = {
  invitee_id: number
  inviter_id: number
  unbound: boolean
  kept_stardust: number
}

export type QyRebindRelationResult = {
  invitee_id: number
  old_inviter_id: number
  inviter_id: number
  rebound: boolean
  kept_stardust: number
}

export type QyBlockRelationResult = {
  invitee_id: number
  inviter_id: number
  blocked: boolean
}

/**
 * 日结明细的一行 = 一个（邀请人 × 下线 × 日）的那一桶（`qy_sd_invite_accrual`）。
 * 对应 `GET /admin/invite/invite-accruals?day=&inviter_id=`。
 *
 * `base_quota` 是额度（下线当日 type=2 消费，排除口径与消费返相同），`gross` 是
 * 按邀请人分组档算出来的星屑（decimal 字符串，只展示）；`status` 与星屑日桶
 * 同一套三态，`ledger_no` 是发放那一笔在星屑账本上的流水号（未发放为空）。
 */
export type QyInviteAccrualRow = {
  inviter_id: number
  inviter_username: string
  invitee_id: number
  invitee_username: string
  bucket_date: string
  base_quota: number
  rate_group: string
  bps: number
  quota_per_unit: number
  gross: string
  status: string
  hold_reason: string
  ledger_no: string
  created_at: number
  updated_at: number
}

export type QyInviteAccrualPage = {
  items: QyInviteAccrualRow[]
  total: number
  p: number
  page_size: number
}
