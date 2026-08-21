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
  /**
   * 「站内佣金余额 → 法币」折算比例的**兜底档**。空串 = 从未配过，
   * 此时回落全站充值汇率（`fiat_rate_global`）。
   *
   * 它是一个**乘数**（`7.3` = 一美元折 7.3 元），不是百分比 —— 界面上绝不能
   * 画成带 `%` 的输入框，那会让运营填 7.3 之后以为自己配的是 7.3%。
   *
   * 与兑换码档不同，这一档**不可清空**：清空之后没配分组档的用户会悄悄退回
   * 充值页汇率，而界面上还写着兜底档。后端收到空串直接 400。
   */
  fiat_rate_default: string
  /** 没配分组档的人**实际**按几折算。只读，不可提交。 */
  fiat_rate_effective: string
  /** 上面那个数来自哪一层：`default`（兜底档）/ `global`（全站充值汇率）/ `none`。 */
  fiat_rate_effective_layer: QyFiatRateLayer
  /** 全站充值汇率，也就是层级里的最后一层。只读。 */
  fiat_rate_global: string
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
  /**
   * 结算调度的**心跳周期**，不再是结算周期。
   *
   * 佣金已改成一日一结算（后端 `settle_daily.go`）：每次心跳只判断"今天这一次
   * 跑过了没有"，没跑过才抢占并排空整个队列。所以调小它既不会让用户更早拿到
   * 钱，也不再让 `qy_commission_settlement` 变长 —— 展示时不要再把它说成
   * "多久结算一次"，那句话会把运营引向一个不存在的旋钮。
   */
  settle_interval_seconds: number
  /**
   * 返佣「一天」相对 UTC 的偏移（分钟）。0 = UTC，480 = UTC+8。
   *
   * 它同时决定日聚合分桶、成熟时刻、日封顶窗口与一日一结算的日界 ——
   * 界面上凡是出现"昨日/今日佣金"的地方，说的都是这个口径下的那一天。
   */
  day_offset_minutes: number
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

/**
 * 法币折算比例命中的层级。层级顺序：分组档 → 兜底档 → 全站充值汇率。
 *
 * `none` = 三层都拿不出一个大于 0 的比例（管理员把全站充值汇率改成了 0
 * 且没配兜底档）。此时佣金的法币折算是 0 —— 额度照加、法币不加，
 * 界面必须把它标成异常而不是当成一个正常配置画出来。
 */
export type QyFiatRateLayer = 'group' | 'default' | 'global' | 'none'

/**
 * 一条分组法币折算比例。
 *
 * 口径是**邀请人（上线）的分组**，与分组费率**相反**（那一档按下线）。
 * 理由见后端 `qianye/modules/commission/fiatrate.go` 的文件头：
 * 费率跟毛利走，折算比例跟收款人走 —— `available_fiat` 是上线的钱，
 * 提现单也是上线在提。
 */
export type QyCommissionFiatRate = {
  group_name: string
  /** 本行配的比例（十进制字符串）。 */
  rate: string
  /**
   * 这个分组**实际**按几折算。规则被禁用（或比例被人手工改坏）时它指向
   * 兜底档 / 全站汇率 —— 界面必须能一眼看出"这一行现在其实没在生效"，
   * 否则关掉一条规则和删掉它长得一模一样。
   */
  effective_rate: string
  effective_layer: QyFiatRateLayer
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
  /**
   * 取值为**法币折算比例**（一个乘数，不是百分比）的键。
   *
   * 同样由后端给出：前端猜错的方向恰好是把它当成百分比渲染，
   * 运营填 7.3 就以为自己配的是 7.3%，而实际生效的是"一美元折 7.3 元" ——
   * 一个差了两个数量级的资金参数。
   */
  fiat_rate_keys: string[]
  group_rates: QyCommissionGroupRate[]
  fiat_rates: QyCommissionFiatRate[]
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
   *
   * 由后端下发而不是前端拿 holding_days 现算：那个 +1 是"桶要等一整天结束才
   * 封板"的直接后果，不是四舍五入，`holding_days: 0` 也是 T+1。前端复刻它就是
   * 第三份口径（后端结算、用户端 policy、管理端），而这一处正是佣金审核那一屏
   * 撤掉「立即结算」之后唯一回答"什么时候到账"的地方。
   */
  payout_day_offset: number
  /** 只有 `status=done` 才为真 —— 有人失败绝不报“今天跑过了”。 */
  ran_today: boolean
  current?: QyDailySettleRun
  /** 昨天那一跑。昨天没跑成是今天才会被发现的事，必须同屏可见。 */
  previous?: QyDailySettleRun
}
