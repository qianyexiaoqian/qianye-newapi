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
 * 返佣看板 DTO。对应 `qianye/modules/commission/api_user.go`。
 *
 * **凡是 decimal 的字段后端一律以 string 下发**（`unsettled_amount`、
 * `pending_mature_quota`、`available_fiat`、`gross_amount`…）：`decimal(30,10)`
 * 超出 JS `number` 的精确表达范围，解析成数字再显示会丢位。前端只做展示，
 * 不参与任何运算。
 */
export type QyCommissionSummary = {
  invitee_count: number
  /** 已成熟、可直接提现的佣金额度。 */
  available_quota: number
  /** 已被提现单冻结、等待审核/打款的部分。 */
  frozen_quota: number
  withdrawn_quota: number
  total_earned_quota: number
  total_clawback_quota: number
  /** 不足 1 额度的精确余数，用来解释"我用了一天怎么没佣金"。 */
  unsettled_amount: string
  /** 已计佣但还没过成熟期的部分。 */
  pending_mature_quota: string
  /** 冲正欠账。为 true 时后端会拒绝一切提现。 */
  debt_blocked: boolean
  available_fiat: string
  fiat_currency: string
  last_settled_at: number
  rate: {
    topup_bps: number
    consume_bps: number
    /**
     * 兑换码那一档的**生效值**（百分比 × 100）。后端已经把"没单独配就跟随
     * 充值档"那一步算完了，前端拿到的永远是一个能直接显示的数。
     */
    redemption_bps: number
    /** 为真表示这一档没单独配、正跟随充值档，界面据此标一句话。 */
    redemption_follows_topup: boolean
  }
  policy: {
    holding_days: number
    min_settle_quota: number
    /**
     * 结算调度的**心跳周期**，不是"多久发一次钱"。佣金已改成一日一结算
     * （后端 `settle_daily.go`），所以这个数字不该再出现在到账时间的说明里。
     */
    settle_interval_seconds: number
    /** 恒为 true 的口径标记：自动结算是一天一次，不是按周期轮询。 */
    settle_daily: boolean
    /**
     * 「消费之后第几天到账」里的那个 N，后端算好下发（`payoutDayOffset`）。
     *
     * **它等于 `holding_days + 1`，前端不要自己加这个 1，更不要直接显示
     * `holding_days`**：那个 +1 来自"消费所在的那一天要整天结束才封板"，
     * 是账本的规则不是四舍五入 —— `holding_days: 0` 也是**次日**到账。
     * 两边各算一遍的结果就是界面上写着一个会被用户追问的错数字。
     */
    payout_day_offset: number
    /**
     * 返佣「一天」相对 UTC 的偏移（分钟）。0 = UTC，480 = UTC+8。
     *
     * 界面上说「次日到账」时必须能说清是谁的次日：站点配 0 时，
     * 国内用户的"次日"其实是北京时间早上 8 点。
     */
    day_offset_minutes: number
    exclude_redemption: boolean
    exclude_subscription: boolean
  }
}

/**
 * 已邀请用户。
 *
 * `masked_name` **已由后端脱敏**（`commission/mask.go`），前端不得再处理。
 * 后端刻意不下发下线的 `user_id` / 邮箱 —— 邀请返佣不是获取他人隐私的授权，
 * 对外标识只有不可逆的 `ref`。
 */
export type QyInvitee = {
  ref: string
  masked_name: string
  bound_at: number
  total_base_quota: number
  /** decimal 字符串。 */
  total_commission: string
  blocked: boolean
}

/** 我的佣金流水。 */
export type QyCommissionRecord = {
  accrual_no: string
  /** `topup` | `redemption` | `consume` | `clawback`。 */
  source_type: string
  /** 已脱敏的来源单号（只留后 4 位）。 */
  source_ref: string
  invitee_ref: string
  invitee_masked_name: string
  base_quota: number
  rate_bps: number
  gross_amount: string
  settled_amount: string
  /** `accrued` | `settled` | `risk_hold` | `voided`。 */
  status: string
  mature_at: number
  bucket_date: string
  created_at: number
}

/**
 * 某个下线在一个日期区间内的**计佣基数**与佣金。
 *
 * 这里刻意**没有**「消费额」这一列。上线看得到的是佣金基数,不是下线账户的
 * 真实消费:两者的差里装着违规扣费(下线被罚了多少款)、渠道测试、0% 商务价
 * 分组、以及被停止计佣的那段时间 —— 每一项都是下线或平台的事,不是拉他进来
 * 的人该知道的。基数本身不是新增泄漏,它早就在佣金流水里逐笔下发了。
 */
export type QyInviteeDaily = {
  ref: string
  masked_name: string
  base_quota: number
  /** decimal 字符串。 */
  commission: string
  blocked: boolean
}

/** 下线区间消费页的整包响应。 */
export type QyInviteeDailyPage = {
  items: QyInviteeDaily[]
  total: number
  p: number
  page_size: number
  range: {
    start_date: string
    end_date: string
    days: number
    max_days: number
  }
  summary: {
    invitee_count: number
    base_quota: number
    commission: string
  }
}
