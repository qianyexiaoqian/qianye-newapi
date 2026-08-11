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

/**
 * 「用户佣金」一行的 DTO。逐字对应后端 `userCommissionView`
 * （`qianye/modules/commission/api_admin_users.go`）。
 *
 * ── 为什么是 `QyCommissionBalance` 的**超集** ──
 * 后端那个结构内嵌了 `balanceView`，也就是「佣金余额」标签用的同一个形状：
 * 同一个数在两张表上必须是同一个名字、同一套算法。派生可提现与账本漂移尤其
 * 如此 —— 那条恒等式在后端已经被结算/冲正/提现三条路径各实现了一遍。
 *
 * 交换到的直接好处：手工增减佣金的弹窗不需要任何适配层就能接收本表的行。
 *
 * ── 一行 = 一个人 ──
 * 站内此前三张管理端佣金表的"一行"都不是一个人（一笔计佣 / 一条邀请关系 /
 * 一行余额账），所以"关于这个人的全部佣金事务"要开三张表、搜三次。本表把它们
 * 收在同一行上。
 *
 * ── 覆盖范围 ──
 * 有上线的人 ∪ 有下线的人 ∪ 有过佣金账的人。不是全站用户：与返佣无关的账号
 * 列出来只会让运营翻不到要找的人。
 *
 * ── 本页只读；写动作一个都不在这里 ──
 * 手工增减走 `/admin/commission/balances/adjust`，绑定/换绑/解绑走
 * `/admin/commission/relations/*`，拉黑走 `/admin/commission/relations/block`。
 * 前端因此**没有第二份资金逻辑**，上限校验、幂等键、恒等式、审计全部只有
 * 后端那一份实现。
 */
export type QyCommissionUser = QyCommissionBalance & {
  /** 主库 `users.display_name`，可能为空；为空时列表回落显示 `username`。 */
  display_name: string
  email: string
  /**
   * 主库 `users.group`，用户分组名。
   *
   * 后端刻意不叫 `group`：那是 SQL 保留字，两边都用它会在原始 SQL 那一侧
   * 反复需要引号方言（`"group"` / `` `group` ``）。
   */
  user_group: string

  // ── 上线（他自己的邀请人）──
  /** `0` = 没有上线。它是"这一行有没有邀请关系"唯一的判据。 */
  inviter_id: number
  inviter_username: string
  /** 假值 + `inviter_id > 0` 表示上线账号已被删除（或这次主库读失败）。 */
  inviter_resolved: boolean
  /**
   * 「**他作为下线**的这条关系被拉黑了」——他的消费不再给上线计佣。
   * 它**不是**"这个账号被封号了"，所以界面上这个徽章挂在「上线」那一列。
   */
  inviter_blocked: boolean
  /**
   * **当前这个上线从这个人身上**已经挣到的佣金额度，也就是"把这条关系换掉或
   * 解掉之后，会留在原邀请人名下的那笔钱"。`inviter_id === 0` 时恒为 `0`。
   *
   * ── 它与 `total_earned_quota` 是反方向的两个数，不能互相顶替 ──
   * `total_earned_quota` 是**他**从自己所有下线身上挣的；这一个是**别人**从他
   * 身上挣的。管理关系的确认框要回答的是"改了之后那笔钱怎么办"，答案只能是
   * 后者。曾经渲染成前者，实测出的后果：397 号自己没有下线（`total_earned` 0），
   * 而他上线 391 从他身上已挣到 13517 —— 确认框写"保留 0"，点完的成功提示写
   * "保留 13517"，同一次操作两个数差 13517。
   *
   * 后端与换绑/解绑响应里的 `kept_commission_quota` 共用同一份实现
   * （`pairCommissionQuotas`），所以确认框与成功提示不可能算出不同的数。
   */
  inviter_commission_quota: number

  // ── 下线 ──
  /** 他名下已被拉黑、不再产生新佣金的下线条数。 */
  blocked_invitee_count: number

  /**
   * 假值表示扩展库里还没有这个人的余额行（他一分佣金都没产生过）。
   * 不是异常；此时那五个额度全是 0，而"0"与"没有这一行"含义不同 ——
   * 对账时把两者混成一个 0 会让人往错误的方向找。
   */
  has_balance_row: boolean
}

/** 列表页的合计，跟着当前筛选条件走（逐页心算是不可行的）。 */
export type QyCommissionUserTotals = {
  user_count: number
  available_quota: number
  withdrawn_quota: number
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
 * 前四个与「佣金余额」标签同名同义；`invitees` 是本表独有的 ——
 * 「谁拉的人最多」是这张表最常被问的问题，而余额表答不了它。
 *
 * 后端在**内存里已经 join 好的行**上排序，所以主库列（下线数）与扩展库列
 * （可提现）能出现在同一个下拉里。代价是候选行数有上界，超过就明确报错
 * 而不是截断出一张"看起来正常、实际少了人"的表。
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
 * 行内筛选。三个都是**布尔开关**而不是下拉：它们互相独立、可以同时成立
 * （「有下线且被拉黑」正是运营最想一眼看到的那一批）。
 *
 * 键名与后端 query 参数逐字一致，`api.ts` 直接把它们摊平进 query。
 * `has_balance` 的后端口径是「账上还挂着钱」（可提现/冻结/未结算余数任一非零），
 * **不含已提现** —— 那笔钱已经走了。
 */
export const QY_COMMISSION_USER_FILTERS = [
  'has_invitees',
  'has_balance',
  'blocked',
] as const

export type QyCommissionUserFilter = (typeof QY_COMMISSION_USER_FILTERS)[number]

/**
 * 关系管理的四种动作。
 *
 * 拆成四个而不是「编辑关系」一个，是因为可用性各不相同（有没有上线决定了
 * 其中三个能不能做），而且**后端是三个不同的端点**：bind / rebind / unbind。
 * 换绑走后端的原子 rebind，不是前端"先解绑再绑"两步 —— 那样第二步失败会把人
 * 留在"没有上线"的中间态。
 */
export const QY_RELATION_ACTIONS = [
  /** 给一个还没有上线的用户指定上线 → `relations/bind`。 */
  'set_inviter',
  /** 换上线 → `relations/rebind`（后端在一个事务里完成）。 */
  'replace_inviter',
  /** 解除这个用户与他上线的关系 → `relations/unbind`。 */
  'remove_inviter',
  /** 给这个用户添加一个下线 → `relations/bind`（方向反过来）。 */
  'add_invitee',
] as const

export type QyRelationAction = (typeof QY_RELATION_ACTIONS)[number]

/** 换绑的返回。`kept_commission_quota` 是**留在旧上线名下**的历史佣金。 */
export type QyRebindRelationResult = {
  invitee_id: number
  old_inviter_id: number
  inviter_id: number
  rebound: boolean
  kept_commission_quota: number
}
