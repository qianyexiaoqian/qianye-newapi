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
 * AFF（邀请）关系列表 DTO。对应 `qianye/modules/commission/api_admin_relation.go`。
 *
 * ── 权威字段是主库的 `users.inviter_id` ──
 * 扩展库的 `qy_invite_relation` 只是**懒建**的展示快照：某个下线第一次产生佣金时
 * 才会有那一行。所以列表（绑定中）由后端从主库出，`snapshot_present` 为假只是
 * 说明"这个人还没产生过佣金"，不是异常。
 */
export type QyAffRelation = {
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

  /** 这一对（邀请人 × 被邀请人）累计产生的佣金，decimal 字符串，转 number 会丢位。 */
  total_commission: string
  /** 同一个数向下取整后的额度，给 formatQuota 用。 */
  total_commission_quota: number
  total_base_quota: number
  accrual_count: number

  snapshot_present: boolean
  blocked: boolean
  /**
   * **自动风控**写的标记（目前只有 `reciprocal_invite`：A 邀 B 且 B 又邀 A）。
   * 由后端 `ensureRelation` 在建快照时算出，人工停/恢复计佣不会覆盖它。
   */
  risk_flags: string
  /**
   * 管理员最近一次停止/恢复计佣填的事由。
   *
   * 空串 = 从没填过（或这条关系从没被人工动过），**不是**"事由是空的"。
   * 它与 `blocked` 完全正交：恢复计佣时事由照样留下，所以不能拿它推断开关状态。
   */
  block_reason: string
}

export type QyAffRelationPage = {
  items: QyAffRelation[]
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
 * 解绑的返回。`kept_commission_quota` 是**保留下来**的历史佣金 ——
 * 解绑不回收任何已产生的佣金，这个数字就是那句话的量化形式。
 */
export type QyUnbindRelationResult = {
  invitee_id: number
  inviter_id: number
  unbound: boolean
  kept_commission_quota: number
}
