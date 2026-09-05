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
 * 佣金管理端 DTO。对应 `qianye/modules/commission/api_admin.go`（D-15 从 git HEAD
 * 恢复，法币折算那一整档删除，加自动入账两个参数）。
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
   * 这两件事必须分开显示：把回落值填进输入框的话，运营下一次保存就把"跟随"
   * 固化成了一个显式数字，从此改充值档不再带动兑换码。
   */
  redemption_rate_percent: string
  /** 兑换码档**实际按几个点算**：没单独配时等于充值档。只读，不可提交。 */
  redemption_rate_effective_percent: string
  /** `redemption_rate_percent === ''` 的服务端版本，前端不自己推。 */
  redemption_rate_follows_topup: boolean
  min_settle_quota: number
  max_per_order_quota: number
  holding_days: number
  /** 自动入账门槛（额度）。可用余额攒到它才会被下一轮记入星辉。 */
  min_credit_quota: number
  /** 自动入账任务的心跳周期（秒）。 */
  credit_interval_seconds: number
  max_daily_quota_per_inviter: number
  large_accrual_alert_quota: number
  min_invitee_age_hours: number
}

/**
 * YAML 只读段。
 *
 * 这些开关涉及安全与启动行为（是否计佣、排除哪些口径、扫描周期），
 * 只能改文件后重载。管理端展示它们是为了让人看到"当前到底跑在什么口径下"，
 * 而不是让人以为可以在这里改。渲染按 `Object.entries` 走，后端多下发一个键
 * 也照常显示 —— 所以这里只列已知的，剩下的兜在索引签名里。
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
  /**
   * 结算调度的**心跳周期**，不是结算周期：佣金一日一结算，每次心跳只判断"今天
   * 这一次跑过了没有"。展示时不要把它说成"多久结算一次"。
   */
  settle_interval_seconds: number
  /** 返佣「一天」相对 UTC 的偏移（分钟），读的是 `invite.day_offset_minutes`。 */
  day_offset_minutes: number
  topup_scan_interval_seconds: number
  topup_scan_lookback_hours: number
} & Record<string, boolean | number | string>

/**
 * 一条分组差异化费率规则。
 *
 * 口径是**推广人自己**的分组（既有拍板，与下线在哪个分组无关）。
 * 没有规则的分组按上面的全局默认费率返。
 */
export type QyCommissionGroupRate = {
  group_name: string
  topup_rate_percent: string
  consume_rate_percent: string
  /**
   * 本组的兑换码档。**`null` = 本组没单独配**，按后端 `redemptionRateUnits`
   * 的顺序回落（全局兑换码档 → 本组充值档）。后端刻意发 `null` 而不是空串：
   * JS 里 `''` 与 `'0'` 都是假值，只有 `null` 不会被真值判断把显式 0% 画成"跟随"。
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
   * 同样由后端给出：前端猜错的方向恰好是把空当成 `0` 提交上去。
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
  /** 冻结的分组。空串表示计佣时没有分组信息。 */
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
   * 这一行背后那条邀请关系**此刻**是不是被停止计佣了（后端每次列表现查）。
   * `invitee_id <= 0` 的手工调整行恒为 false。没有这一位，本页就只能画一个
   * 单向的「停止计佣」按钮。
   */
  relation_blocked: boolean
}

/** 一次结算运行的记录。与后端 `runView` 逐字对应。 */
export type QyDailySettleRun = {
  run_date: string
  status: string
  holder: string
  attempts: number
  started_at: number
  finished_at: number
  heartbeat_at: number
  duration_sec: number
  rounds: number
  processed: number
  failed: number
  granted_quota: number
  reclaimed_quota: number
  remark: string
}

/**
 * `GET /admin/commission/health` 里 `daily_settle` 那一段。
 *
 * 佣金改成一天结算一次之后，「今天这一跑成了没有」变成一个每天只有一次机会的
 * 问题：跑挂了，当天剩下所有人的佣金都要等到明天，而用户端与其它页面上没有
 * 任何症状。所以它必须出现在界面上，而不是只躺在接口的 JSON 里。
 */
export type QyDailySettleSnapshot = {
  /** 结算日界口径下的“今天”，不是 UTC 自然日也不是服务器本地日。 */
  today: string
  /** 日界相对 UTC 的偏移分钟数。面板必须说清用的是哪个日界。 */
  day_offset_minutes: number
  next_run_after: number
  max_attempts: number
  /**
   * 「消费之后第几天到账」里的那个 N（后端 `payoutDayOffset = holding_days + 1`）。
   * 由后端下发而不是前端拿 holding_days 现算：那个 +1 是"桶要等一整天结束才
   * 封板"的直接后果，`holding_days: 0` 也是 T+1。
   */
  payout_day_offset: number
  /** 只有 `status=done` 才为真 —— 有人失败绝不报“今天跑过了”。 */
  ran_today: boolean
  current?: QyDailySettleRun
  /** 昨天那一跑。昨天没跑成是今天才会被发现的事，必须同屏可见。 */
  previous?: QyDailySettleRun
}

/**
 * `GET /admin/commission/health` 里 `credit` 那一段（D-15 新增，形状由 Z1 定；
 * 缺省时整段可能不下发，界面按"取不到"处理）。
 */
export type QyCommissionCreditSnapshot = {
  /** 在途(pending)入账单数:资金单已开、主库还没落定。 */
  pending: number
  /** 卡在 held 的入账单数。非 0 就该去资金对账页裁决。 */
  held: number
  /** 挂起单的额度合计(星辉)。 */
  held_quota: number
  /** 上一轮处理了几行、成功几行。 */
  last_processed?: number
  last_done?: number
  last_run_at?: number
}
