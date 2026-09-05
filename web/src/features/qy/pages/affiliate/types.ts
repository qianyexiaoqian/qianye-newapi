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
 * 邀请返星屑（用户端）的 DTO。与契约 `invite-contract.md` 「用户端接口」逐字对应
 * （D-14：佣金账本 / 提现整体删除，邀请人的全部收益只有星屑）。
 *
 * 金额分两种单位，绝不混用：
 *   · `*_quota` —— **额度**（下线的消费基数），走 `QyAmountText`；
 *   · 其余整数（`totals.*`、`granted`、`held`、`amount`、`total_stardust`）——
 *     **星屑**，走 `QySdAmount`。
 * `gross` 是 decimal，后端以字符串下发，前端只展示、不运算。时间戳 unix 秒。
 */

/** 邀请类的五种流水 kind，顺序 = 概览里图形化分布的顺序 = 明细筛选下拉的顺序。 */
export const QY_INVITE_KINDS = [
  'invite_consume',
  'invite_topup',
  'invite_redeem',
  'invite_register',
  'plan_inviter',
] as const
export type QyInviteKind = (typeof QY_INVITE_KINDS)[number]

/** `GET /invite/summary`。 */
export type QyInviteSummary = {
  invitee_count: number
  /** 被停止计返的下线数。 */
  blocked_count: number
  /** 累计返给我的星屑，按 kind 拆开；`all` 是五项之和（后端算好，前端不复刻）。 */
  totals: Record<QyInviteKind, number> & { all: number }
  /** 昨日下线消费返：基数是额度；已返 / 暂缓是星屑。 */
  yesterday: {
    base_quota: number
    granted: number
    held: number
  }
  /** 今天到目前为止下线的消费基数（额度），下一次日结按它算。 */
  pending_today_base_quota: number
  /** 我自己所在分组档的四个比例 / 奖励（费率按邀请人自己的分组，既有拍板）。 */
  rate: {
    group: string
    invite_consume_bps: number
    invite_topup_bps: number
    invite_redeem_bps: number
    /** 每个注册下线一次性返的星屑（整数）。 */
    invite_register_stardust: number
  }
  /** 支付合规声明。未确认时邀请返一律不发，界面要把这件事说出来。 */
  compliance_confirmed: boolean
  /** 日界相对 UTC 的偏移（分钟）。0 = UTC，480 = UTC+8。 */
  day_offset_minutes: number
}

/** `GET /invite/invitees` 的一行。用户名**已由后端脱敏**，前端不得再处理。 */
export type QyInvitee = {
  user_id: number
  username_masked: string
  bound_at: number
  blocked: boolean
  /** `YYYYMMDD`，空串 = 没有过消费。 */
  last_active_day: string
  /** 累计消费基数（额度）。 */
  total_base_quota: number
  /** 我因 TA 得到的全部星屑（各 kind 合计）。 */
  total_stardust: number
}

/** `GET /invite/records` 的一行：我的星屑流水里邀请类那五种 kind 的行。 */
export type QyInviteRecord = {
  ledger_no: string
  kind: QyInviteKind | (string & {})
  /** 星屑，入账为正。 */
  amount: number
  /** 由 ref / accrual 反查；拿不到时为 0。 */
  invitee_id: number
  invitee_masked: string
  ref_no: string
  remark: string
  created_at: number
}

/** `GET /invite/invitee-daily?day=` 的整包响应。 */
export type QyInviteeDailyPage = {
  day: string
  items: QyInviteeDailyRow[]
  total_base_quota: number
  /** decimal 字符串。 */
  total_gross: string
}

export type QyInviteeDailyRow = {
  invitee_id: number
  invitee_masked: string
  /** 消费基数（额度）。 */
  base_quota: number
  bps: number
  /** 计提的星屑，decimal 字符串。 */
  gross: string
  /** `computed` / `settled` / `held`，与星屑日桶同一套状态。 */
  status: string
}

// ───────────────────────── 星辉佣金（D-15）─────────────────────────
//
// 与上面的邀请返星屑**并行**：项目方原话「以前是直接返还余额，现在是折算成星辉。
// 佣金账本、结算、佣金余额，这些你得改成星辉才行。不再直接结算成现金」。
// 「星辉」是站内余额 `users.quota` 的展示名，账本仍以额度整数记账，界面一律走
// `QyAmountText` 按展示单位印 —— 这一页上从此没有「元 / 现金 / 提现 / 打款」。
// 契约：`commission-xinghui-contract.md` 「用户」三条接口。

/**
 * `GET /commission/summary`。
 *
 * **凡是 decimal 的字段后端一律以 string 下发**（`unsettled_amount`、
 * `pending_mature_quota`、`gross_amount`…）：`decimal(30,10)` 超出 JS `number`
 * 的精确表达范围，解析成数字再显示会丢位。前端只做展示，不参与任何运算。
 */
export type QyCommissionSummary = {
  invitee_count: number
  /** 已成熟、等待自动入账的佣金（额度）。到 `min_credit_quota` 就会被下一轮入账。 */
  available_quota: number
  /**
   * 已被在途入账单占用的部分（额度）。自动入账先扣 available、写 pending 的入账行，
   * 资金单落定之后才算进 `credited_quota`；这一段时间里钱既不在可用里、也不在
   * 已入账里。后端若不再下发这一格就按 0 处理，不编一个数出来。
   */
  frozen_quota?: number
  /** 累计已自动入账到星辉的佣金（额度）。D-14 之前叫 `withdrawn_quota`。 */
  credited_quota: number
  total_earned_quota: number
  total_clawback_quota: number
  /** 不足 1 额度的精确余数，用来解释"我用了一天怎么没佣金"。 */
  unsettled_amount: string
  /** 已计佣但还没过成熟期的部分。 */
  pending_mature_quota: string
  /** 冲正欠账。为 true 时自动入账会跳过这个人，直到后续佣金把欠账抵完。 */
  debt_blocked: boolean
  last_settled_at: number
  /**
   * 下一轮自动入账最早开跑的时刻（unix 秒）。0 = 后端暂时给不出（任务没起来）。
   * 与 `min_credit_quota` 一起回答"我这笔什么时候到星辉"：够门槛 + 到时刻。
   */
  next_credit_at: number
  /** 自动入账的门槛（额度）：可用余额不到它不会被入账，攒着。 */
  min_credit_quota: number
  /**
   * 三档返佣比例。全部是**这个账号自己**的生效值 —— 费率按推广人自己所在的
   * 用户分组解析（既有拍板），所以"我能拿几个点"对每个人是一个确定的数字。
   */
  rate: {
    topup_bps: number
    consume_bps: number
    /** 兑换码档的**生效值**（百分比 × 100），后端已把"没单独配就跟随充值档"算完。 */
    redemption_bps: number
    redemption_follows_topup: boolean
    /** 解析用的用户分组名。空串 = 后端这次没能解析出分组，界面不编一个名字。 */
    group: string
    /** 为真表示命中了该分组单独配的档；为假表示三个数字来自全局默认档。 */
    group_matched: boolean
    global_topup_percent: string
    global_consume_percent: string
    global_redemption_percent: string
  }
  /**
   * 名下还没被结算吸收的计佣行里**最早的那个成熟时刻**（unix 秒）。0 表示没有
   * 在途佣金，或者在途的都已经成熟。它是账本上写着的事实，改配置不追溯；
   * `policy.payout_day_offset` 是按当前配置算的 T+N，只对此后新产生的消费成立。
   */
  pending_earliest_mature_at: number
  policy: {
    holding_days: number
    min_settle_quota: number
    settle_interval_seconds: number
    settle_daily: boolean
    /** 「消费之后第几天到账」里的那个 N，后端算好（`holding_days + 1`），前端不复刻。 */
    payout_day_offset: number
    /** 返佣「一天」相对 UTC 的偏移（分钟）。0 = UTC，480 = UTC+8。 */
    day_offset_minutes: number
    exclude_redemption: boolean
    exclude_subscription: boolean
  }
}

/** `GET /commission/records` 的一行：我的佣金账本逐笔。 */
export type QyCommissionRecord = {
  accrual_no: string
  /** `topup` | `redemption` | `consume` | `clawback` | `manual`。 */
  source_type: string
  /** 已脱敏的来源单号（只留后 4 位）。 */
  source_ref: string
  invitee_ref: string
  invitee_masked_name: string
  /** 计佣基数（额度）。 */
  base_quota: number
  rate_bps: number
  /** 额度，decimal 字符串。 */
  gross_amount: string
  settled_amount: string
  /** `accrued` | `settled` | `risk_hold` | `voided`。 */
  status: string
  mature_at: number
  bucket_date: string
  created_at: number
}

/** 自动入账单的四种终态 / 中间态，与后端 `qy_commission_credit.status` 逐字一致。 */
export const QY_COMMISSION_CREDIT_STATUSES = [
  'pending',
  'done',
  'failed',
  'held',
] as const
export type QyCommissionCreditStatus =
  (typeof QY_COMMISSION_CREDIT_STATUSES)[number]

/**
 * `GET /commission/credits` 的一行：一次自动入账（佣金余额 → 星辉）。
 *
 * `held` 是「资金单结局不明、等人工裁决」：钱可能已经进了星辉也可能没有，
 * 界面要把它当告警色画，绝不能与 `failed`（已确认没动、余额已退回可用）混成一格。
 */
export type QyCommissionCredit = {
  credit_no: string
  /** 这一笔入账的额度整数。 */
  quota: number
  /** 两阶段资金单单号（前缀 CC），在「资金对账」页按它能查到这一笔的落定过程。 */
  fund_order_no: string
  status: QyCommissionCreditStatus | (string & {})
  created_at: number
  /** 0 = 还没落定。 */
  finished_at: number
  remark: string
}
