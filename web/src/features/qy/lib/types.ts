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
import type { ReactNode } from 'react'

/**
 * qy 扩展的共享类型。
 *
 * 与后端 `qianye/controller/config.go`、`qianye/model/fund_order.go` 以及各
 * `qianye/modules/<mod>/api_*.go` 的响应体一一对应。各功能页面自己的 DTO 放在
 * `features/qy/<page>/types.ts`，本文件只收敛"所有页面都要用"的部分。
 */

// ───────────────────────────── 响应信封 ─────────────────────────────

/**
 * 后端统一信封。失败时额外带 `code`（见 `qianye/guard/guard.go`）。
 *
 * `success` 是判定信封的唯一标志：扩展未注册时请求会落到上游 NoRoute，
 * 返回的 `{"error":{...}}` 甚至 HTML 都没有这个字段。
 */
export type QyEnvelope<T> = {
  success: boolean
  message?: string
  code?: string
  data?: T
}

/**
 * 列表分页信封。
 *
 * 后端各模块统一返回 `{items, total}`，多数还带 `p` / `page_size`
 * （违规模块的几个列表只返回前两个字段，所以后两者可选）。
 */
export type QyPage<T> = {
  items: T[]
  total: number
  p?: number
  /** 星屑 / 商城这一批新接口的页码参数叫 `page`（从 1 起），响应也原样回显它。 */
  page?: number
  page_size?: number
}

/** 星屑 / 商城这一批接口的分页参数：`page` 从 1 起。 */
export type QyPageParams = {
  page: number
  page_size: number
}

// ───────────────────────────── 引导端点 ─────────────────────────────

/** YAML 功能开关。字段名与 `config.go` 的 `features` 段完全一致。 */
export type QyFeatures = {
  transfer: boolean
  /**
   * 邀请返星屑（D-14）。它取代了此前的 `commission` 与 `withdraw` 两格：
   * 佣金账本、结算、法币折算与提现模块整体删除，邀请人的全部收益只有星屑
   * （不可提现、不可划转）。关掉 = 不建关系、不发任何邀请返、推广页 404。
   */
  invite: boolean
  /**
   * 推广佣金。与 `invite` **并存**：D-14 曾把佣金账本整体删除、只留星屑侧
   * 的邀请返；D-15 把账本请回来记「星辉」，D-16 又把它改成**星屑** —— 与邀请返
   * 到期自动入账，没有申请、没有审核、没有法币。关掉 = 不计佣、佣金页与结算台 404，
   * 星屑侧的邀请返照旧。
   */
  commission: boolean
  availability: boolean
  violation: boolean
  lottery: boolean
  ticket: boolean
  /** 用户分组 × 模型分组 矩阵。关掉时隐藏管理端入口与上游分组倍率页的指路提示。 */
  group_matrix: boolean
  /**
   * 支付密码。它没有自己的 `enabled` —— 后端下发的是
   * `transfer || lottery`（`guard.FlagPayPassword`；提现模块删除后 OR 链少了一项）。
   *
   * 这一页此前标的是 `feature: 'transfer'`：关掉划转，「支付密码」整页从侧栏
   * 消失，而抽奖报名照旧要求验密 —— 没设过密码的用户想去设置却连入口都找不到。
   */
  pay_password: boolean
  /**
   * 星屑（站内积分，与额度不同的第二种货币）。娱乐活动的报名费与派奖全部走它，
   * 商城也只收它 —— 所以 `mall` 在后端是 `mall.enabled && stardust.enabled`，
   * 星屑关掉时商城一定跟着关。
   */
  stardust: boolean
  mall: boolean
}

/**
 * 星屑段（引导端点 `stardust`）。
 *
 * `name` 是运营可改的**货币名**（默认「星屑」）：所有星屑金额旁边印的单位都读它，
 * 前端**不**硬编码。缺键或空白时由 `useStardustName()` 回落到 i18n 的
 * `qy_sd_unit_default` —— 回落放在 hook 而不是 `normalizeQyConfig` 里，因为后者
 * 在模块加载期就要把 localStorage 快照归一化一次，那一刻 i18next 还没初始化，
 * 而这个回落词本身是要随语言切换的。
 */
export type QyStardustOptions = {
  show_entry: boolean
  name: string
}

/** 商城段（引导端点 `mall`）。 */
export type QyMallOptions = {
  show_entry: boolean
}

/**
 * 钱包页入口的显隐开关。
 *
 * 「余额划转」一格照旧；`show_commission_entry` 随 D-15 回到契约里（后端
 * `wallet.show_commission_entry` 回来）。前端目前仍不读它 —— 推广入口在侧栏而
 * 不在钱包页 —— 登记只是让引导端点的形状与契约逐字一致。`show_withdraw_entry`
 * 不回来：提现模块已永久删除。
 */
export type QyWalletEntries = {
  show_transfer_entry: boolean
  show_commission_entry: boolean
}

/** 日志页扩展列的显隐开关。 */
export type QyLogMetricsOptions = {
  show_reasoning_effort: boolean
  show_cache_ratio: boolean
  enable_filter: boolean
}

/**
 * 划转表单的预校验参数。
 *
 * `recipient_lookup` 限定收款人查找方式（后端刻意不提供用户名模糊搜索，
 * 否则等于开放用户枚举）。
 */
export type QyTransferOptions = {
  min_quota: number
  max_per_tx_quota: number
  recipient_lookup: string
}

/**
 * 抽奖/竞猜的两个展示开关（需求原文：「系统设置前端是否显示」）。
 *
 * 与 `features.lottery` 是**并列**关系而不是二选一：`features.lottery` 是
 * YAML 里"这个功能装没装"，`show_entry` 是站点"这一期要不要在前台露出入口"。
 * 关掉 `show_entry` 只隐藏用户侧入口，管理端照常可进 —— 否则关掉之后就再也
 * 没有地方能把它打开了。
 *
 * `proof_public` 决定证据链端点是否允许匿名访问。前端据此决定要不要把
 * 「把这个链接发给任何人都能自己验」这句话显示出来：关掉时那句话是假的。
 */
export type QyLotteryOptions = {
  show_entry: boolean
  proof_public: boolean
  plays: QyLotPlays
}

/**
 * 四种玩法各自的显示/隐藏（后端 `qianye/modules/lottery/play.go`）。
 *
 * ── 为什么是四个而不是两个 ──
 * 库里只有两个 `kind`（draw / guess），但用户看到的是四种游戏：抽奖有三种定档
 * 方式（按名次 / 按公示概率 / 双色球），竞猜一种。三者的规则、概率口径与卡面
 * 含义互不相同 —— 双色球卡片上连「奖池」这个数的语义都换了。运营说「双色球
 * 先不上」时，说的是这一层，落不到 `kind` 上。
 *
 * ── 零值 ──
 * 后端不下发某个键时按**显示**处理，与后端 `mergeOverrides` 的口径逐字一致
 * （没配过 = 全部显示）。这与 `show_entry` 缺省按「不显示」相反，是刻意的：
 * `show_entry` 缺省隐藏最多是少一个入口，而玩法缺省隐藏会让一个从没动过配置
 * 的站点在升级后整块娱乐功能静默消失 —— 本仓在「缺一段配置 = 整块不可见」上
 * 已经栽过。多显示一格也不会变成断链：大厅列表由后端过滤，隐藏的玩法返回空。
 */
export type QyLotPlays = {
  draw_rank: boolean
  draw_prob: boolean
  draw_ball: boolean
  guess: boolean
  /**
   * 星屑转盘（`kind='draw', draw_mode='wheel'`）。
   *
   * 第五种玩法。项目方 2026-09-05 把它并成「抽奖竞猜」选择夹的第四张标签
   * （`lib/pages.ts` 的 `QY_TAB_GROUPS`），那张标签的显隐由
   * `show_entry × plays.wheel` 决定（`nav.ts` 的 `qyEntrySwitches.wheel`）；
   * 关掉它只藏这一张标签，宿主那一行由其余玩法撑着。
   */
  wheel: boolean
}

/**
 * 归一化后的引导端点响应。
 *
 * 后端在 `enabled=false` 时只返回 `{enabled, available}` 两个字段，因此原始
 * 响应是"部分对象"。`normalizeQyConfig()` 会补齐成本类型，让所有调用方都能
 * 无条件读 `config.features.transfer`，不必到处写可选链。
 */
export type QyConfig = {
  enabled: boolean
  available: boolean
  features: QyFeatures
  wallet: QyWalletEntries
  log_metrics: QyLogMetricsOptions
  transfer_options: QyTransferOptions
  lottery: QyLotteryOptions
  stardust: QyStardustOptions
  mall: QyMallOptions
}

/** 引导端点的原始响应形状（字段可能缺失）。 */
export type QyConfigPayload = {
  enabled?: boolean
  available?: boolean
  features?: Partial<QyFeatures>
  wallet?: Partial<QyWalletEntries>
  log_metrics?: Partial<QyLogMetricsOptions>
  transfer_options?: Partial<QyTransferOptions>
  lottery?: Partial<Omit<QyLotteryOptions, 'plays'>> & {
    plays?: Partial<QyLotPlays>
  }
  stardust?: Partial<QyStardustOptions>
  mall?: Partial<QyMallOptions>
}

// ───────────────────────────── 状态机 ─────────────────────────────

/**
 * 全站统一的单据状态取值。
 *
 * `uncertain` 是资金系统的"我不知道，交给人"出口（裁定文档 C12），前端必须用
 * 告警色而不是失败色 —— 钱可能已经动了，不能让用户以为一定没成功。
 *
 * `in_doubt` 是它前面的一档：主库 COMMIT 已经发出但结局不明，系统还在自动复判。
 * 同样不能用失败色，但也不该用 uncertain 的告警色 —— 它不需要人介入。
 *
 * 联合里保留 `(string & {})` 是刻意的：后端日后新增状态时前端只应降级为中性
 * 徽章，绝不能因为拿到未知字符串而崩溃。
 */
export type QyStatus =
  | 'approved'
  | 'cancelled'
  | 'failed'
  | 'frozen'
  | 'in_doubt'
  | 'paid'
  | 'paying'
  | 'pending'
  | 'processing'
  | 'rejected'
  | 'reversed'
  | 'success'
  | 'uncertain'
  | (string & {})

// ───────────────────────────── 时间线 ─────────────────────────────

/**
 * 单据时间线的一个节点。商城订单这类多段流程用它渲染。
 *
 * `state` 决定视觉：`done` 实心、`current` 高亮 + 呼吸、`pending` 灰显、
 * `failed` 用失败色。未到达的节点必须保留占位而不是不渲染 ——
 * 用户需要知道"后面还有几步"。
 */
export type QyTimelineItem = {
  key: string
  title: string
  description?: ReactNode
  /** unix 秒。`0` / 缺省表示尚未发生，显示为 `-`。 */
  timestamp?: number
  state?: 'current' | 'done' | 'failed' | 'pending'
}

/**
 * 资金单业务类型，取自 `qianye/model/fund_order.go` 的 kind 常量。
 *
 * 两种提现的 kind 已随 D-14 变成"历史 kind"注释，不再有 Resolver，这里不列；
 * `commission_credit` 是 D-15 新增的**佣金自动入账**（单号前缀 CC，主库
 * `IncreaseUserQuota`）。佣金结算 / 冲正两个 kind 由后端决定是否恢复为活的 kind，
 * 前端不预设 —— 联合里的 `(string & {})` 兜住任何未登记的行，界面上渲染成原样字符串。
 */
export type QyFundOrderKind =
  | 'commission_credit'
  | 'lottery_entry'
  | 'lottery_payout'
  | 'transfer'
  | (string & {})

/**
 * 跨库两阶段资金单（`qy_fund_orders`）。
 *
 * `status` 是 int8 而不是字符串：0 待定 / 2 成功 / 3 失败 / 4 不可判定 /
 * 5 已冲正 / 6 结局不明（主库 COMMIT 已发出）。
 * 用 `qyFundOrderStatusName()` 转成 {@link QyStatus} 再交给徽章渲染。
 */
export type QyFundOrder = {
  id: number
  order_no: string
  kind: QyFundOrderKind
  status: number
  idem_scope: string
  idem_key: string
  user_id: number
  peer_user_id: number
  amount_quota: number
  fee_quota: number
  ref_type: string
  ref_id: string
  attempts: number
  next_probe_at: number
  last_error: string
  node_name: string
  created_at: number
  updated_at: number
  settled_at: number
}
