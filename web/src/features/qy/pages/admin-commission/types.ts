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
 * 佣金管理端 DTO。对应 `qianye/modules/commission/api_admin.go`。
 *
 * 生效配置的 key 与后端 `settings.go` 的常量逐字一致 —— 它们同时是
 * `qy_settings` 表里的行键与 PUT 请求体的字段名，改一个字就写不进去。
 *
 * **返佣比例一律是百分比字符串**（"10"、"10.25"）。用字符串而不是 number
 * 是刻意的：10.25 在 JS 的 Number 里同样是二进制浮点，回填输入框时可能变成
 * 10.249999999999998，运营再点一次保存就把这个数字存进了资金配置。
 */
export type QyCommissionEffective = {
  topup_rate_percent: string
  consume_rate_percent: string
  /**
   * 兑换码这一档**配的是什么**。空串 = 没单独配 = 跟随充值档；`"0"` = 显式 0%。
   *
   * 这两件事必须分开显示：0% 是一个合法的运营配置（兑换码多用于活动赠送，
   * 不想为它付佣金），而"没配"是每一个升级上来的站点的样子。把回落值填进
   * 输入框的话，运营下一次保存就把"跟随"固化成了一个显式数字，从此改充值档
   * 不再带动兑换码 —— 一次什么都没改的保存，静默改变了系统行为。
   */
  redemption_rate_percent: string
  /** 兑换码档**实际按几个点算**：没单独配时等于充值档。只读，不可提交。 */
  redemption_rate_effective_percent: string
  /** `redemption_rate_percent === ''` 的服务端版本，前端不自己推。 */
  redemption_rate_follows_topup: boolean
  min_settle_quota: number
  max_per_order_quota: number
  holding_days: number
  max_daily_quota_per_inviter: number
  large_accrual_alert_quota: number
  min_invitee_age_hours: number
}

/**
 * YAML 只读段。
 *
 * 这些开关涉及安全与启动行为（是否计佣、排除哪些口径、扫描周期），
 * 只能改文件后重载。管理端展示它们是为了让人看到"当前到底跑在什么口径下"，
 * 而不是让人以为可以在这里改。
 */
export type QyCommissionYamlReadonly = {
  enabled: boolean
  topup_rate_percent: string
  consume_rate_percent: string
  /** YAML 里的兑换码档。空串就是"没写这一项"，也就是跟随充值档。 */
  redemption_rate_percent: string
  exclude_redemption_and_manual: boolean
  exclude_subscription_consume: boolean
  refund_clawback: boolean
  settle_interval_seconds: number
  topup_scan_interval_seconds: number
  topup_scan_lookback_hours: number
  inviter_cache_seconds: number
}

/**
 * 一条分组差异化费率规则。
 *
 * 口径是**被邀请人（下线）的分组**，不是邀请人的分组 —— 理由见后端
 * `qianye/modules/commission/grouprate.go` 的文件头。
 * 没有规则的分组按上面的全局默认费率返。
 */
export type QyCommissionGroupRate = {
  group_name: string
  topup_rate_percent: string
  consume_rate_percent: string
  /**
   * 本组的兑换码档。**`null` = 本组没单独配**，按后端
   * `redemptionRateUnits` 的顺序回落（全局兑换码档 → 本组充值档）。
   *
   * 后端刻意发 `null` 而不是空串：JS 里 `''` 与 `'0'` 都是假值，
   * 只有 `null` 不会被 `value ? … : 跟随` 这类写法把显式 0% 也画成"跟随"。
   */
  redemption_rate_percent: string | null
  enabled: boolean
  remark: string
  operator_id: number
  updated_at: number
}

export type QyCommissionAdminConfig = {
  effective: QyCommissionEffective
  /** `qy_settings` 里的运营覆盖，值一律是字符串。 */
  overrides: Record<string, string>
  editable_keys: string[]
  /** 这些键的取值是百分比字符串，其余键是整数。由后端给出，前端不猜。 */
  percent_keys: string[]
  /**
   * `percent_keys` 里**允许留空**的那些。空表示"没单独配，跟随充值档"。
   *
   * 同样由后端给出：前端猜错的方向恰好是把空当成 `0` 提交上去，
   * 而那是一次没有人批准的费率归零。
   */
  nullable_percent_keys: string[]
  group_rates: QyCommissionGroupRate[]
  yaml_readonly: QyCommissionYamlReadonly
}

/** 管理端计佣流水。后端直接回 `qy_commission_accrual` 原始行。 */
export type QyAdminAccrual = {
  id: number
  accrual_no: string
  idem_scope: string
  idem_key: string
  inviter_id: number
  invitee_id: number
  source_type: string
  source_ref: string
  base_quota: number
  base_money: string
  /** 冻结的费率，单位是"百分比 × 100"（1025 = 10.25%）。列名沿用历史。 */
  rate_bps: number
  /** 冻结的下线分组。空串表示计佣时没有分组信息。 */
  rate_group: string
  gross_amount: string
  settled_amount: string
  usd_rate: string
  status: string
  risk_flags: string
  mature_at: number
  bucket_date: string
  remark: string
  created_at: number
  /**
   * 这一行背后那条邀请关系**此刻**是不是被停止计佣了（后端每次列表现查，
   * 不走 60 秒缓存）。`invitee_id <= 0` 的手工调整行恒为 false。
   *
   * 没有这一位，本页就只能画一个单向的「停止计佣」按钮 —— 而"停了就没法恢复"
   * 正是项目方对这套功能的全部意见。
   */
  relation_blocked: boolean
}
