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
 * 「划转门槛按用户分组分档」的管理端 DTO。
 * 对应 `qianye/modules/transfer/api_admin_limits.go` 与 `grouplimit.go`。
 *
 * # 三态在这里的形状是 `number | null`
 *
 * 后端用可空列承载三态，JSON 上就是：
 *
 * | 取值    | 含义                                   |
 * |---------|----------------------------------------|
 * | `null`  | 这一档**没有覆盖**这一项 → 回落全站门槛 |
 * | `0`     | 这一档**显式配成 0** → 这道闸门不设      |
 * | `n`     | 这一档配成 n                            |
 *
 * **绝不能在前端把 `null` 折成 0**（`?? 0`、`Number(x)`、非空断言都会）：
 * 折一次，运营就再也配不出「vip 不限日额度」——他填的 0 会被当成没配而回落到
 * 全站的 2 亿。本仓已经三次栽在这个形状上。
 */
import type { QyUserGroupOption } from '../../lib/group-options'

/** 可分档的门槛键。与后端 `tierableKeys` 逐字一致，顺序即渲染顺序。 */
export const QY_TIERABLE_KEYS = [
  'min_quota',
  'max_per_tx_quota',
  'daily_max_quota',
  'daily_max_count',
  'cooldown_seconds',
  'receiver_daily_max_in_count',
  'new_account_freeze_hours',
] as const

export type QyTierableKey = (typeof QY_TIERABLE_KEYS)[number]

/** 一项生效值的来源。字符串而不是布尔，理由见后端 `tierSourceGlobal`。 */
export type QyTierSource = 'global' | 'group'

/** 一档的原始覆盖。缺席的键与 `null` 同义：没有覆盖。 */
export type QyTierOverrides = Partial<Record<QyTierableKey, number | null>>

export type QyTransferGroupLimit = {
  /** 用户分组（`users.group`），后端已归一（去空白 + 折叠大小写）。 */
  user_group: string
  /**
   * 整档开关。它与「某一项没覆盖」是两件事：这是一个开关，那是七个独立的三态。
   * `false` 时整档视同不存在，这一组人回落全站门槛。
   */
  enabled: boolean
  remark: string
  created_at: number
  updated_at: number
  updated_by: number
  /**
   * 合并之后**真正生效**的七个数，与资金路径读到的逐位相同。
   *
   * 注意这一份是后端按「这一行叠到全站门槛上」现算的，**不看 `enabled`** ——
   * 它回答的是「把这一档启用之后会变成什么样」，正是运营按下那个开关之前
   * 需要看到的东西。
   */
  effective: Record<QyTierableKey, number>
  /** 每一项的生效值是兜底来的还是这一档覆盖的。 */
  sources: Record<QyTierableKey, QyTierSource>
  /**
   * 这一档与当前全站门槛的组合是否自洽。
   *
   * `false` 意味着后端已经把**这一组人**的划转失败关闭了（503），而列表里
   * 这一行看起来与别的行没有任何区别 —— 界面必须把它标出来。
   *
   * 它会在**运营没动这一档**的情况下变成 false：把全站单笔上限调到某一档的
   * 单笔下限之下就够了。
   */
  valid: boolean
} & QyTierOverrides

export type QyTransferGroupLimitsPage = {
  items: QyTransferGroupLimit[]
  /** 全站兜底那一份。勾掉一项覆盖之后会落到这里。 */
  global: Record<string, number>
  /** 全站那组值是否自洽。false 时整个划转已经停了，先去上面那张卡片修。 */
  global_valid: boolean
  tierable_keys: QyTierableKey[]
  bounds: Record<string, { min: number; max: number }>
  max_tier_count: number
  /**
   * 用户分组下拉候选。
   *
   * 键名与 `/admin/transfer/group-rules` 逐字对齐，**绝不叫 `group_options`**：
   * 那个名字在同模块的另一个端点上承载的是**模型分组**。两个端点用同一个键名
   * 承载两个命名空间时，任何按键名共享的 helper 都会把模型分组喂进划转的下拉，
   * 而两处各自 import 了不同的类型别名，TypeScript 编译期看不出来。
   */
  user_group_options: QyUserGroupOption[]
  /** 登记表是否读到了。false 时收起「未登记分组」软告警并保留自由输入。 */
  user_groups_probe_ok: boolean
  /** 分档引用了、但站点没登记过的用户分组。**软告警，不是错误。** */
  unknown_groups: string[]
}

/** 新建 / 整行覆盖的入参。 */
export type QyTransferGroupLimitInput = {
  user_group: string
  enabled: boolean
  remark: string
  /**
   * **整行替换语义**：这里没带（或带成 `null`）的键会被清成「跟随全局」。
   *
   * 刻意不是补丁：补丁语义下「把某一项改回跟随全局」这个动作根本无法表达 ——
   * 运营在界面上清掉一个输入框、保存、刷新，那一项还在。
   */
  overrides: QyTierOverrides
}
