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

// ───────────────────────── 推广佣金（D-16：以星屑结算）─────────────────────────
//
// 与上面的邀请返星屑**并行**，而且 D-16 之后两边发的是**同一种钱**：佣金账本以
// 星屑记账，金额在计佣那一刻按 `stardust.quota_per_unit` 折算，界面一律走
// `QySdAmount`。这一页上没有「元 / 现金 / 提现 / 打款」，也不再有「星辉」。
//
// ⚠ 两条线的三档（充值 / 消费 / 兑换码）打的是**同一笔基数**。两边同时为正就会给
//   同一个上线落两笔星屑 —— 这是运营的配置选择，管理端健康面板会把重叠标出来。
//
// D-15 那一版记的是「星辉」（站内余额 `users.quota` 的展示名），入账要跨库，所以
// 这里曾有 `frozen_quota`（在途）与 `fund_order_no`（两阶段资金单号）。D-16 入账
// 是扩展库里的本地事务，两者连同它们描述的中间态一起没了。

/**
 * `GET /commission/summary`。
 *
 * **凡是 decimal 的字段后端一律以 string 下发**（`unsettled_amount`、
 * `pending_mature`、`gross_amount`…）：`decimal(30,10)` 超出 JS `number`
 * 的精确表达范围，解析成数字再显示会丢位。前端只做展示，不参与任何运算。
 */
export type QyCommissionSummary = {
  invitee_count: number
  /** 已成熟、等待自动入账的佣金（星屑）。到 `min_credit_stardust` 就会被下一轮入账。 */
  available: number
  /** 累计已自动入账进星屑余额的佣金（星屑）。 */
  credited: number
  total_earned: number
  total_clawback: number
  /** 不足 1 星屑的精确余数，用来解释"我用了一天怎么没佣金"。 */
  unsettled_amount: string
  /** 已计佣但还没过成熟期的部分（星屑，decimal 字符串）。 */
  pending_mature: string
  /** 冲正欠账。为 true 时自动入账会跳过这个人，直到后续佣金把欠账抵完。 */
  debt_blocked: boolean
  last_settled_at: number
  /**
   * 下一轮自动入账最早开跑的时刻（unix 秒）。0 = 后端暂时给不出（任务没起来）。
   * 与 `min_credit_stardust` 一起回答"我这笔什么时候到账"：够门槛 + 到时刻。
   */
  next_credit_at: number
  /** 自动入账的门槛（星屑）：可用余额不到它不会被入账，攒着。 */
  min_credit_stardust: number
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
    /** 一次结算至少要凑够多少星屑才发。 */
    min_settle_stardust: number
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
  /** 计佣基数（**额度** —— 下线实际花掉的那个数，不是星屑）。 */
  base_quota: number
  rate_bps: number
  /** 计佣当刻冻结的刻度：1 星屑 = 多少额度。0 = D-16 之前的历史行。 */
  quota_per_unit: number
  /** 星屑，decimal 字符串。 */
  gross_amount: string
  settled_amount: string
  /** `accrued` | `settled` | `risk_hold` | `voided`。 */
  status: string
  mature_at: number
  bucket_date: string
  created_at: number
}

/**
 * 自动入账单的状态，与后端 `qy_commission_credit.status` 逐字一致。
 *
 * 只剩 `done`。D-15 的 `pending` / `failed` / `held` 描述的都是**跨库**转账的
 * 中间与失败形状（资金单在途、探针确认主库未动、进人工裁决）；D-16 入账是扩展库里
 * 的一个本地事务，佣金余额的搬运与星屑流水的写入在同一次提交里，不存在"这一半成了
 * 那一半没成"。保留成数组是为了让筛选下拉与状态徽章的形状不变，后端将来加态时
 * 这里加一行即可。
 */
export const QY_COMMISSION_CREDIT_STATUSES = ['done'] as const
export type QyCommissionCreditStatus =
  (typeof QY_COMMISSION_CREDIT_STATUSES)[number]

/** `GET /commission/credits` 的一行：一次自动入账（佣金可用余额 → 星屑余额）。 */
export type QyCommissionCredit = {
  credit_no: string
  /** 这一笔入账的星屑数。 */
  amount: number
  /**
   * 星屑账本那一侧的流水号（`qy_sd_ledger.ledger_no`，kind=`commission_credit`）。
   * 拿着它去「星屑 → 流水」按号一查就是同一笔 —— 它取代了 D-15 的 `fund_order_no`。
   */
  ledger_no: string
  status: QyCommissionCreditStatus | (string & {})
  created_at: number
  /** 0 = 还没落定。 */
  finished_at: number
  remark: string
}
