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
 * 星屑配置（管理端）的 DTO。与契约 `stardust-api-contract.md` §3 逐字对应。
 * 五段形状照抽奖配置（`admin-lottery/types.ts` 的 `QyLotAdminConfig`）。
 */

/**
 * `qy_settings`（scope=stardust）里可在线改的那几项。
 * 键名与后端 `editable_keys` 逐字一致 —— 它们同时是表里的行键与 PUT 请求体的字段名。
 */
export type QyStardustEffective = {
  /** 货币名（≤16 rune）。 */
  name: string
  /** 用户侧入口显隐。1 = 显示。 */
  show_entry: number
  /** 消费返（万分比）。 */
  consume_bps: number
  /**
   * 下线消费返（万分比，D-14）：下线当日消费 × 邀请人分组档的这一比例 → 邀请人
   * 得到的星屑，日结与消费返同一次 run。默认 0；`bps=0` 时后端不写日结行。
   */
  invite_consume_bps: number
  /** 下线充值返（万分比）。合规未确认时正值被 400 `qy_sd_compliance_required`。 */
  invite_topup_bps: number
  /** 下线用兑换码返（万分比）。同上受合规闸门约束。 */
  invite_redeem_bps: number
  /** 下线注册一次性返（整数星屑）。同上受合规闸门约束。 */
  invite_register_stardust: number
  /** 日桶被扣住超过 N 天就在对账页告警。 */
  held_alert_days: number
}

/** 一个可写键的取值区间（契约写的是 `lo` / `hi`，不是抽奖那边的 `min` / `max`）。 */
export type QyStardustBound = {
  lo: number
  hi: number
}

/** YAML 只读段。 */
export type QyStardustYamlReadonly = {
  /** 1 星屑折多少额度；YAML 填 0 时后端下发实际的 `common.QuotaPerUnit`。 */
  quota_per_unit: number
  settle_delay_minutes: number
  settle_interval_seconds: number
  exclude_subscription_consume: boolean
  exclude_manual_topup: boolean
  /** 单次手调的绝对值上限（整数星屑）。 */
  max_manual_adjust: number
  /** 合规确认。为 false 时三个邀请类键不能配正值。 */
  compliance_confirmed: boolean
}

export type QyStardustAdminConfig = {
  effective: QyStardustEffective
  overrides: Record<string, string>
  editable_keys: string[]
  bounds: Record<string, QyStardustBound>
  yaml_readonly: QyStardustYamlReadonly
}

/** PUT 请求体：稀疏，只传改动键。 */
export type QyStardustConfigPatch = Record<string, number | string>

/** PUT 的响应：保存之后的生效值（后端实施时下发的形状，契约没写这一层）。 */
export type QyStardustConfigSaveResult = {
  effective: QyStardustEffective
}

/** 按用户分组覆盖的费率。`null` = 该项不覆盖、沿用全站。 */
export type QyStardustGroupRate = {
  user_group: string
  consume_bps: number | null
  invite_consume_bps: number | null
  invite_topup_bps: number | null
  invite_redeem_bps: number | null
  enabled: boolean
  updated_at: number
  operator_id: number
}

export type QyStardustGroupRatesView = {
  items: QyStardustGroupRate[]
  /** 可选分组名清单：在册 ∪ 登记表。 */
  groups: string[]
}

export type QyStardustGroupRateInput = {
  consume_bps?: number | null
  invite_consume_bps?: number | null
  invite_topup_bps?: number | null
  invite_redeem_bps?: number | null
  enabled: boolean
}

/** DELETE 的响应。行本来就不存在时同样 200，只是 `deleted=false`（删除是幂等动作）。 */
export type QyStardustGroupRateDeleteResult = {
  user_group: string
  deleted: boolean
}

/**
 * 套餐清单的一行（上游 `/api/subscription/admin/plans` 里本页用得到的字段）。
 * 只给「套餐返还」编辑器的下拉用：运营在下拉里认的是标题，落库的是 id。
 */
export type QyStardustPlanOption = {
  id: number
  title: string
  enabled: boolean
}

/** 套餐返的来源：走哪几种购买渠道才返。 */
export type QyStardustPlanRewardSource =
  | 'admin'
  | 'balance'
  | 'order'
  | 'redemption'
  | (string & {})

export type QyStardustPlanReward = {
  plan_id: number
  buyer_bps: number
  inviter_bps: number
  sources: QyStardustPlanRewardSource[]
  /** false = 没配过，上面三项是默认值（buyer 10000 / inviter 0 / order+balance）。 */
  exists: boolean
}

export type QyStardustPlanRewardInput = {
  buyer_bps: number
  inviter_bps: number
  sources: QyStardustPlanRewardSource[]
}
