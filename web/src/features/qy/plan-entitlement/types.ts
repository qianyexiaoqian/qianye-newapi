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
 * 套餐的「模型分组解锁 + 余额使用范围」DTO。
 *
 * 与后端 `qianye/modules/planentitlement` 的 `api_admin.go` 逐字对齐。
 *
 * ── 这一段配置回答的两个问题 ──
 *
 *   1. **这个套餐能解锁哪些模型分组**（`unlock_groups`）。
 *      解锁**不绑定用户分组**：买了套餐的人拿得到这些模型分组，与他所在的
 *      用户分组无关。它叠在「用户分组范围」之上，是并集不是替换。
 *   2. **套餐自带的那笔额度能用在什么上**（`balance_scope`）。
 *      `universal` = 也能用于其它模型分组（上游今天的行为）；
 *      `restricted` = 只能用于它绑定的模型分组，其余请求直接跳过这个套餐
 *      （**不是余额不足** —— 余额明明还在，只是用不了）。
 *
 * ── 套餐**不携带倍率** ──
 *
 * 倍率恒为 `(用户分组, 模型分组)` 的纯函数，真相源只有上游 `options`。
 * 允许套餐带倍率之后，倍率会变成 `(userId, 模型分组, 此刻持有的套餐集合)`
 * 的函数，而**套餐到期的那一秒倍率会跳变**，预扣与结算跨越那一秒时会用两个
 * 不同的倍率结算同一笔请求 —— 这一条没有便宜的修法。所以本 DTO 里没有任何
 * 倍率字段，只有一张**只读**的 `ratio_table` 用来告诉运营"实际会按多少扣"。
 */

/** 套餐余额的使用范围。 */
export type QyPlanBalanceScope = 'restricted' | 'universal'

/**
 * 该套餐解锁的每个模型分组，在**每个用户分组**下实际会按多少扣。
 *
 * ── 为什么必须常驻显示 ──
 *
 * 项目方原话是「按照模型分组 G 的模型分组倍率来使用」，字面指兜底
 * `GetGroupRatio(G)`。但计价链路会**无条件优先**用 `(用户分组, G)` 的交叉格：
 * 用户组 A 谈好了 G 的 0.5，A 组的用户买了解锁 G 的套餐，实际按 0.5 而不是
 * G 的兜底 1.0。
 *
 * 这个偏差是刻意保留的（用户组谈好的价不该因为买了套餐而失效），但它必须
 * **看得见** —— 否则运营只能靠猜。
 */
export type QyPlanRatioRow = {
  user_group: string
  model_group: string
  /** 走 `service.GetUserGroupRatio` 现算，不经过任何扩展缓存。 */
  ratio: number
  /** `cross_cell` = 该对配了交叉倍率；`group_ratio` = 继承该模型分组的兜底倍率。 */
  source: 'cross_cell' | 'group_ratio'
}

/** `GET /api/qy/admin/subscription/plans/:plan_id/entitlement` 的响应。 */
export type QyPlanEntitlement = {
  plan_id: number
  plan_title: string
  /** 解锁的模型分组。空数组 = 没解锁任何分组（合法）。 */
  unlock_groups: string[]
  balance_scope: QyPlanBalanceScope
  note: string
  /**
   * 已绑定、但**已经从 `options.GroupRatio` 里消失**的模型分组。
   *
   * 非空时套餐编辑页出红色横幅：这是"运营删了一个仍被引用的模型分组"唯一能被
   * 发现的地方（上游删除时不做任何引用检查）。这些绑定在快照里已被剔除，用户
   * 拿不到该分组；而 restricted 套餐撞上它时，余额会变成**死钱** —— 花不掉，
   * 到期作废。
   */
  missing_groups: string[]
  /** 当前生效中的订阅条数 —— 改这份配置会立刻影响多少人。列表接口下发 `-1`（未统计）。 */
  active_subscriptions: number
  ratio_table: QyPlanRatioRow[]
  /**
   * 「余额使用范围」此刻真的在扣费路径上生效吗。
   *
   * 为 `false` 时 `balance_scope` 只是一份存下来的配置：`restricted` 的套餐余额
   * 照样会被任意模型分组花掉。界面**必须**在这时说出来 —— 否则管理端显示
   * 「已经限制住了」，而钱照样从那张套餐里扣，骗的正是"我的钱去哪了"这个问题。
   */
  balance_scope_enforced: boolean
  /**
   * 套餐解锁模块的 kill switch（`plan_entitlement.enabled`）此刻的状态。
   *
   * 为 `false` 时**这个弹窗配的东西一样都不生效**：已购用户拿不到解锁的模型分组，
   * 余额范围也不过滤。而读写两条接口都不看这个开关，配了会保存成功、审计记成功。
   * 界面不说的话，运营会对着一个已经被拉闸的功能反复调参。
   */
  enabled: boolean
  /**
   * 多选框的候选集合：**模型分组轴**的事实清单（`options.GroupRatio` 的键）。
   *
   * **必须由后端下发，不能由前端从别处凑**：用户分组与模型分组共用一个字符串
   * 命名空间，凑错轴会让运营解锁出一个根本没有渠道的"分组"；而解锁一个不在
   * 兜底倍率表里的名字，会撞上上游 `GetGroupRatio` 的 fail-open —— 它找不到时
   * 静默 `return 1` 并只写一条日志，于是那批请求按原价扣费、零告警。
   */
  model_group_candidates: string[]
}

// ─────────────────────────── 用户端（买家看到的那一半）───────────────────────────

/**
 * 一条活跃订阅在买家眼里的样子。
 *
 * 与后端 `qianye/modules/planentitlement/api_user.go` 的 `subscriptionView`
 * 逐字对齐。`qyGet` 是纯类型断言、没有运行时校验：字段名对不上不会有任何编译或
 * 运行时报错，只会渲染成 `undefined` —— 而 `undefined` 落在「这笔余额能用在什么
 * 上」这个位置上会被读成「没有限制」，正好是与事实相反的那一半。
 */
export type QyUserSubscriptionEntitlement = {
  user_subscription_id: number
  plan_id: number
  plan_title: string
  /** 0 表示**不限额**（上游语义），此时 `remaining` 无意义，看 `unlimited`。 */
  amount_total: number
  amount_used: number
  unlimited: boolean
  /** 已把「下一次扣费事务里会先跑的那次周期重置」算进去了。 */
  remaining: number
  pending_reset: boolean
  next_reset_time: number
  start_time: number
  end_time: number
  allow_wallet_overflow: boolean
  balance_scope: QyPlanBalanceScope
  /**
   * 该套餐解锁 / 绑定的模型分组。
   *
   * `balance_scope === 'restricted'` 时它同时是「这笔余额能花在什么上」的权威
   * 清单；`universal` 时它只是解锁清单，余额仍可用于任意分组。
   */
  bound_groups: string[]
  usable_here: boolean
  /** 「按此刻的余额，下一笔会先动这一张」。是判据不是承诺，文案必须写「优先」。 */
  will_charge_first: boolean
}

/** `GET /api/qy/subscription/entitlements` 的响应。 */
/**
 * 一个套餐的权益披露，**与当前用户是否持有它无关**。
 *
 * 这是「买之前」那一半的数据源：`subscriptions` 按定义只覆盖已持有的活跃订阅，
 * 拿它去渲染套餐卡等于「买了才告诉你这笔余额只能花在哪」。
 * 只包含后台配置过权益的套餐，没配过的套餐不会出现在这个数组里。
 */
export type QyPlanGrantView = {
  plan_id: number
  balance_scope: QyPlanBalanceScope
  bound_groups: string[]
}

export type QyMyEntitlements = {
  model_group: string
  /** 全站配置过权益的套餐各一条，用于**付款前**披露。 */
  plans: QyPlanGrantView[]
  /** 已按**扣费顺序**（end_time asc, id asc）排好，不是上游那个展示顺序。 */
  subscriptions: QyUserSubscriptionEntitlement[]
  /** 这些套餐一共给用户解锁了哪些模型分组（并集）。 */
  unlocked_groups: string[]
  any_restricted: boolean
  /**
   * 「仅限」此刻真的在扣费路径上生效吗。
   *
   * 为 `false` 时 `balance_scope: 'restricted'` 只是一份存下来的配置，余额照样
   * 会被任意模型分组花掉。界面在这时说「仅限」就是在骗人。
   */
  balance_scope_enforced: boolean
  wallet_fallback_blocked: boolean
}

/** `PUT /api/qy/admin/subscription/plans/:plan_id/entitlement` 的请求体。 */
export type QyPlanEntitlementRequest = {
  /**
   * 模型分组名的清单，**整体替换、必填**。
   *
   * 空数组 = 清空解锁（合法）。**省略该字段后端直接 400**
   * （`qy_invalid_param`：缺少 unlock_groups）—— 所以它不是可选的。
   */
  unlock_groups: string[]
  /**
   * 余额使用范围。**省略不等于"不改"**：后端在缺字段时按 `universal` 处理，
   * 也就是**静默把「仅限」改回「通用」**。任何调用方都必须显式传当前值。
   */
  balance_scope: QyPlanBalanceScope
  note: string
}
