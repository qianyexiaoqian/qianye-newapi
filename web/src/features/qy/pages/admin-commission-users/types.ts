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
import type { QyCommissionBalance } from '../admin-commission-balances/types'
import type { QyCommissionCreditStatus } from '../affiliate/types'

/**
 * 「佣金用户」一行的 DTO。逐字对应后端 `userCommissionView`
 * （`qianye/modules/commission/api_admin_users.go`，D-15 从 git HEAD 恢复）。
 *
 * ── 为什么是 `QyCommissionBalance` 的**超集** ──
 * 后端那个结构内嵌了 `balanceView`，也就是「余额对账」次级标签用的同一个形状：
 * 同一个数在两张表上必须是同一个名字、同一套算法。派生可用与账本漂移尤其如此。
 *
 * ── 一行 = 一个人 ──
 * 计佣流水的"一行"是一笔计佣、余额表的"一行"是一行余额账，所以"关于这个人的
 * 全部佣金事务"要开两张表、搜两次。本表把它们收在同一行上。
 *
 * ── 覆盖范围 ──
 * 有上线的人 ∪ 有下线的人 ∪ 有过佣金账的人。不是全站用户。
 *
 * ── 本页只读；写动作只有手工增减 ──
 * 手工增减走 `/admin/commission/balances/adjust`；绑定 / 换绑 / 解绑 / 停止计返
 * 是邀请关系的事，在「邀请管理」里做（invite 模块），本页不再各写一份。
 */
export type QyCommissionUser = QyCommissionBalance & {
  /** 主库 `users.display_name`，可能为空；为空时列表回落显示 `username`。 */
  display_name: string
  email: string
  /** 主库 `users.group`，用户分组名。后端刻意不叫 `group`（SQL 保留字）。 */
  user_group: string

  // ── 上线（他自己的邀请人）──
  /** `0` = 没有上线。它是"这一行有没有邀请关系"唯一的判据。 */
  inviter_id: number
  inviter_username: string
  /** 假值 + `inviter_id > 0` 表示上线账号已被删除（或这次主库读失败）。 */
  inviter_resolved: boolean
  /** 「**他作为下线**的这条关系被停止计返了」—— 不是"这个账号被封了"。 */
  inviter_blocked: boolean
  /**
   * **当前这个上线从这个人身上**已经挣到的佣金(星屑)。`inviter_id === 0` 时恒为 0。
   * 与 `total_earned`（**他**从自己所有下线身上挣的）是反方向的两个数。
   */
  inviter_commission: number

  // ── 下线 ──
  /** 他名下已被停止计返、不再产生新佣金的下线条数。 */
  blocked_invitee_count: number

  /**
   * 假值表示扩展库里还没有这个人的余额行（他一分佣金都没产生过）。
   * "0"与"没有这一行"含义不同 —— 对账时把两者混成一个 0 会让人往错误的方向找。
   */
  has_balance_row: boolean
}

/** 列表页的合计，跟着当前筛选条件走（逐页心算是不可行的）。 */
export type QyCommissionUserTotals = {
  user_count: number
  available: number
  credited: number
  invitee_count: number
}

export type QyCommissionUserPage = {
  items: QyCommissionUser[]
  total: number
  p: number
  page_size: number
  totals: QyCommissionUserTotals
}

/**
 * 排序口径，与后端 `userCommissionSorters` 的键逐字一致。
 *
 * 前四个与「余额对账」同名同义；`invitees` 是本表独有的 ——
 * 「谁拉的人最多」是这张表最常被问的问题，而余额表答不了它。
 */
export const QY_COMMISSION_USER_SORTS = [
  'available',
  'earned',
  'updated',
  'user',
  'invitees',
] as const

export type QyCommissionUserSort = (typeof QY_COMMISSION_USER_SORTS)[number]

/**
 * 排序项 → 文案键。走查表而不是模板串：`available` 那一项的旧文案写着「按可提现」，
 * 现在的口径是「按可用」，而旧键留在主包里不能改，只能换键。
 */
export const QY_COMMISSION_USER_SORT_LABEL_KEY: Readonly<
  Record<QyCommissionUserSort, string>
> = {
  available: 'qy_cu_sort_available_xh',
  earned: 'qy_cu_sort_earned',
  updated: 'qy_cu_sort_updated',
  user: 'qy_cu_sort_user',
  invitees: 'qy_cu_sort_invitees',
}

/**
 * 行内筛选。三个都是**布尔开关**而不是下拉：它们互相独立、可以同时成立。
 *
 * 键名与后端 query 参数逐字一致，`api.ts` 直接把它们摊平进 query。
 * `has_balance` 的后端口径是「账上还挂着钱」（可用 / 未结算余数任一非零），
 * **不含已入账** —— 那笔钱已经发成星屑、进了对方的星屑余额。
 */
export const QY_COMMISSION_USER_FILTERS = [
  'has_invitees',
  'has_balance',
  'blocked',
] as const

export type QyCommissionUserFilter = (typeof QY_COMMISSION_USER_FILTERS)[number]

/**
 * `GET /admin/commission/credits` 的一行：全站的自动入账记录。
 *
 * 与用户端 `QyCommissionCredit` 同形，多两个字段（`user_id` / `username`；
 * 用户端不需要：那是"我"）。
 *
 * D-16 之后这里没有要裁决的单子了：入账是扩展库里的一个本地事务，要么成要么
 * 整笔回滚，不会留下"结局不明"的行。`ledger_no` 取代了 D-15 的 `fund_order_no`，
 * 指向星屑账本里的同一笔。
 */
export type QyAdminCommissionCredit = {
  credit_no: string
  user_id: number
  username: string
  /** 这一笔入账的星屑数。 */
  amount: number
  /** `qy_sd_ledger.ledger_no`，kind=`commission_credit`。 */
  ledger_no: string
  status: QyCommissionCreditStatus | (string & {})
  created_at: number
  finished_at: number
  remark: string
}

export type QyAdminCommissionCreditPage = {
  items: QyAdminCommissionCredit[]
  total: number
  p: number
  page_size: number
}
