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
import type {
  QyLotDrawMode,
  QyLotEntryStatus,
  QyLotKind,
  QyLotOption,
  QyLotOutcome,
  QyLotPayoutKind,
  QyLotPayoutStatus,
  QyLotRules,
  QyLotStatus,
  QyLotTier,
} from '../lottery/types'

/**
 * 抽奖 / 竞猜管理端 DTO。
 *
 * 用户端能看的那部分**直接复用 `../lottery/types`**，不在这里再抄一遍：
 * 同一个活动在两边显示的是同一件事，两份声明必然漂移成"管理员看到的字段
 * 用户端读不到"。这里只加管理端独有的那些列。
 *
 * **不含 `seed`**。种子在后端是独立表 + 两个函数才能读，前端从头到尾没有
 * 任何拿到它的通道 —— 揭示之后它出现在证据链里，那时它已经是公开信息了。
 */

export type QyLotAdminActivity = {
  act_no: string
  kind: QyLotKind
  /**
   * 定档方式（`rank` / `prob` / `ball`）。老活动不下发，按 `rank` 处理。
   *
   * 它是活动行上的一列而不是新的 `kind`：生命周期任务按 kind 扫表，新增一个
   * kind 要在四处各补一个分支，漏一处就是一条静默死路。
   */
  draw_mode?: QyLotDrawMode

  // ── 双色球期次（`draw_mode='ball'` 专用；其余模式恒为零值）──
  //
  // 池子那三个数要到 publish 那一刻才从系列行上**原子取走**，草稿期恒为 0。
  // 号池四元组在创建时就从系列抄一份，因为参与时要用它校验选号。
  series_no?: string
  issue_no?: number
  pool_seed_quota?: number
  pool_carry_quota?: number
  pool_open_quota?: number
  /** 本期投注额进奖池的万分比，其余是平台手续费。 */
  pool_share_bps?: number
  ball_red_pool?: number
  ball_red_pick?: number
  ball_blue_pool?: number
  ball_blue_pick?: number
  /** 本期开奖号 `03,09,12|05`。开奖前为空串。 */
  ball_result?: string

  status: QyLotStatus
  outcome: QyLotOutcome
  title: string
  intro: string
  stake_quota: number
  open_at: number
  close_at: number
  draw_at: number
  settle_deadline: number

  /** publish 时冻结的三个哈希。`draft` 阶段全是空串。 */
  commit_hash: string
  rules_hash: string
  spec_hash: string
  algo: string

  rules_text: string

  allow_multi_win: boolean
  fee_bps: number
  min_entries_to_hold: number
  max_entries_per_user: number
  max_attempts_per_user: number
  max_total_entries: number
  max_total_users: number
  max_per_inviter: number
  cooldown_seconds: number
  dedup_ip: boolean
  bet_min_quota: number
  bet_max_quota: number

  /** 物化计数。与逐条 COUNT 的差值由对账任务盯着，对不上会落一条 flag。 */
  entry_seq: number
  active_count: number
  pending_count: number
  pool_quota: number

  /** 结算三口径。管理端「本场收支」直读这三个数。 */
  platform_fee_quota: number
  payout_quota: number
  refund_quota: number

  /** 封盘时冻结的名单。揭示之前就已对外公开 —— 这是整套协议的关键。 */
  roster_hash: string
  roster_count: number
  chain_head: string

  /**
   * 获胜选项的**自增 id**，0 = 还没录。
   *
   * 对外稳定的编号是 `opt_no`，它在选项行上（`is_winner`）—— 详情页要显示
   * "哪个选项赢了"请从 {@link QyLotAdminActivityView.options} 里找，
   * 不要指望这里有一个 `win_opt_no`：自增 id 跨环境不稳定，而证据链必须能在
   * 任何一份数据库副本上算出同一个结果，所以进哈希的从来只有 `opt_no`。
   */
  win_option_id: number
  result_evidence: string
  result_by: number
  cancel_reason: string

  created_by: number
  created_at: number
  published_at: number
  locked_at: number
  revealed_at: number
  settled_at: number
}

/** 本场收支。`held_quota` 是"已经确定要发、只是还没发出去"的那一部分。 */
export type QyLotAdminEconomics = {
  prize_total_quota: number
  break_even_entries: number
  income_quota: number
  payout_quota: number
  refund_quota: number
  platform_fee_quota: number
  /**
   * 转人工的出款合计。
   *
   * 它必须单独摆出来：收尾时 `payout_quota` 只聚合已到账的那些，而转人工的
   * `held` 同时被当作终态放行 —— 不显示它，一笔平台还欠着的钱就会从
   * 「本场收支」里彻底消失，运营据此判断的亏损额比真实值小。
   */
  held_quota: number
  net_quota: number
}

/**
 * 活动详情的**完整信封**。
 *
 * 奖档与选项是独立的表，所以它们独立下发而不是嵌在活动行里 —— 活动行是
 * 状态机,奖档是被承诺的内容,两者的生命周期不同(草稿期奖档整体替换,
 * 发布之后连同 spec_hash 一起冻结)。
 */
export type QyLotAdminActivityView = {
  activity: QyLotAdminActivity
  prizes: QyLotTier[]
  options: QyLotOption[]
  economics: QyLotAdminEconomics
}

/** 列表行。刻意不带 `rules_text` / 奖档：列表用不上，几十行加起来就是几百 KB。 */
export type QyLotAdminActivityBrief = Pick<
  QyLotAdminActivity,
  | 'act_no'
  | 'active_count'
  | 'close_at'
  | 'created_at'
  | 'draw_at'
  | 'draw_mode'
  | 'issue_no'
  | 'kind'
  | 'open_at'
  | 'outcome'
  | 'payout_quota'
  | 'platform_fee_quota'
  | 'pool_quota'
  | 'refund_quota'
  | 'stake_quota'
  | 'status'
  | 'title'
> & {
  /** 未解决的对账异常条数。列表页的红点直读它。 */
  open_flag_count: number
}

/**
 * 创建活动的请求体。
 *
 * **没有种子入参**，这不是遗漏：管理员对随机源没有任何输入通道，种子在
 * `draft` 落库那一刻由服务端 CSPRNG 生成并写进独立表。给它开一个入参，
 * 整套承诺-揭示就没有意义了。
 *
 * ## 频次闸门为什么在 `rules` 里面而不是顶层
 *
 * "每人几次、多久一次、同 IP 算不算一个人"同样是**参与条件**，必须进
 * `rules_text` → `rules_hash` → `commit_hash`。放在顶层就等于事后改上限
 * 不会被任何验证检出 —— 那是改规则却不留痕。后端的 `activityInput` 顶层
 * 没有这些字段，`ShouldBindJSON` 对未知字段是**静默丢弃**：发错位置不会
 * 报错，只会让整组风控在界面上显示"已设置"而实际上一条都没生效。
 *
 * 字段名与 `qianye/modules/lottery/api_admin.go` 的 `activityInput`
 * 逐字对齐，一个字都不能改。
 */
export type QyLotCreateInput = {
  kind: QyLotKind
  /** 定档方式。为空按 `rank` 处理，只对 `kind='draw'` 有意义。 */
  draw_mode?: QyLotDrawMode
  /**
   * 双色球必填：一期双色球必须属于某个期次系列。
   *
   * 号池、投注入池比例、累计发行上限**全部由那个系列决定**，请求体里没有任何
   * 一个能单独覆盖它们的字段 —— 号池若能逐期指定，"各档概率是组合数算出来的"
   * 这条主张就没了，因为管理员可以每期换一个号码空间。
   */
  series_no?: string
  title: string
  intro: string
  stake_quota: number
  open_at: number
  close_at: number
  draw_at: number
  settle_deadline: number
  allow_multi_win: boolean
  fee_bps: number
  min_entries_to_hold: number
  bet_min_quota: number
  bet_max_quota: number
  rules: QyLotRules
  /** 抽奖奖档。竞猜传空数组。后端字段名是 `prizes`。 */
  prizes: QyLotTier[]
  /** 竞猜选项。抽奖传空数组。`opt_no` 由前端按顺序编号。 */
  options: { opt_no: number; label: string; is_catch_all: boolean }[]
}

export type QyLotAdminEntry = {
  entry_no: string
  seq: number
  user_id: number
  username: string
  user_ref: string
  opt_no: number
  amount: number
  status: QyLotEntryStatus
  order_no: string
  chain_hash: string
  fail_code: string
  created_at: number
  settled_at: number
}

export type QyLotAdminPayout = {
  payout_no: string
  entry_no: string
  user_id: number
  username: string
  kind: QyLotPayoutKind
  tier: number
  draw_pos: number
  amount_quota: number
  status: QyLotPayoutStatus
  order_no: string
  attempts: number
  next_attempt_at: number
  last_error: string
  created_at: number
  settled_at: number
}

/**
 * 文本奖履行队列的一行（`GET /admin/lottery/text-prizes`）。
 *
 * ## 为什么它不是 {@link QyLotAdminPayout} 的几个可选字段
 *
 * 两张列表的每一行说的是不同的事：出款列表的一行是**资金单**（重试、卡单、
 * 代次、幂等键），这张的一行是**一件人要去做的事**。共用一个类型会让
 * "还有 3 笔没发出去"与"还有 3 份码没填"在代码里长得一模一样，而它们的
 * 处置方式、升级路径、以及"卡住了谁该被叫醒"完全不同。
 *
 * 后端也是分开的两个 handler，跨活动查询（列表页顶部那个待履行红点直读它的
 * `total`），所以这里带 `act_no` / `title`。
 */
export type QyLotAdminTextPrize = {
  payout_no: string
  act_no: string
  title: string
  tier: number
  user_id: number
  username: string
  /**
   * 兑换码的**掩码**。明文只走 `reveal` 那一个写审计的接口 ——
   * 让列表直接带明文，会使"滑过列表时的随手点击"和"真正的核对"在事后的
   * 审计流水里混成一片，那时审计就不再能区分任何事情。
   */
  secret_mask: string
  fulfilled: boolean
  fulfilled_at: number
  fulfilled_by: number
  fulfill_note: string
  created_at: number
}

/**
 * 揭示明文的返回。**不写进 react-query 缓存**，用完即丢。
 *
 * 明文进了缓存就会在别的页面被无意间读到，刷新时还会被自动重放 ——
 * 而这是一个每次调用都要留一条审计的动作。
 */
export type QyLotPrizeSecret = {
  payout_no: string
  secret: string
  note: string
  /**
   * 被顶替过的历史内容。
   *
   * 撤销履行**不清密文**，但再次履行会把上一串整列覆盖，而 Event 与审计快照
   * 按设计都不含明文。后端因此把被顶替的那几串搬进一张只增不改的履历表，
   * 并只在这个写审计的接口里下发 —— 争议的原话永远是「我用的那串码失效了」，
   * 回答它需要看到当初发出去的那一串。
   */
  superseded: QyLotSupersededSecret[]
}

export type QyLotSupersededSecret = {
  seq: number
  secret: string
  note: string
  fulfilled_at: number
  fulfilled_by: number
  superseded_at: number
  superseded_by: number
}

/** 活动状态机的事件流。**对用户可见、属于证据链**，与管理审计是两回事。 */
export type QyLotAdminEvent = {
  id: number
  from_status: string
  to_status: string
  action: string
  actor_type: string
  actor_user_id: number
  detail: string
  created_at: number
}

/** 对账任务发现的账不平 / 卡单 / 名单漂移。 */
export type QyLotAdminFlag = {
  id: number
  code: string
  detail: string
  resolved: boolean
  resolved_by: number
  created_at: number
  resolved_at: number
}

// ─────────────────────────── 双色球期次系列 ───────────────────────────

/**
 * 一个双色球系列：号池 + 跨期滚存的奖池 + **累计发行闸门**。
 *
 * ## 为什么号池住在系列上而不是每一期上
 *
 * 号池四元组进每期的 `commit_hash` 原像，而它在系列上定死、期与期之间不可变。
 * 可变的号码空间等于可变的中奖概率 —— 而"各档概率是组合数算出来的、不用相信
 * 平台"正是双色球唯一但决定性的优势。
 *
 * ## 为什么 `issue_cap_quota` 创建时就冻结、此后任何接口都改不了
 *
 * 单期的奖品总额上限拦不住滚存：每期注资到上限、连开 N 期无人中奖，池子滚到
 * N 倍，某一期一次性发出去——而每一期看起来都守住了。把上限冻结在系列行上并
 * 让每次注资走条件 UPDATE，才能证明「一整个系列、无论开多少期、无论运气多差，
 * 平台的累计净增发不超过 `issue_cap_quota`」。
 */
export type QyLotSeries = {
  series_no: string
  title: string
  status: 'closed' | 'open' | (string & {})
  red_pool: number
  red_pick: number
  blue_pool: number
  blue_pick: number
  /** 投注入池比例（万分比）。0 = 投注全部归平台，池子只靠注资。 */
  pool_share_bps: number
  /** 当前**尚未被任何一期取走**的池子。发布一期时整块取走并清零。 */
  pool_quota: number
  /** 累计注资，只增不减，是封顶判定的左值。 */
  seed_total_quota: number
  paid_total_quota: number
  /** 创建时冻结的累计注资上限。 */
  issue_cap_quota: number
  /** `issue_cap_quota − seed_total_quota`，即本系列还能再注多少。 */
  headroom_quota: number
  /** 已开出的期数。下一期的 `issue_no` 是它 + 1。 */
  issue_seq: number
  created_at: number
  updated_at: number
}

/** 创建系列的请求体。字段名与后端 `seriesInput` 逐字对齐。 */
export type QyLotSeriesInput = {
  title: string
  red_pool: number
  red_pick: number
  blue_pool: number
  blue_pick: number
  pool_share_bps: number
  issue_cap_quota: number
  /** 创建时的首笔注资，可为 0（之后再注）。 */
  seed_quota: number
}

// ───────────────────────────── 配置 ─────────────────────────────

/**
 * `qy_settings`（scope=lottery）里可在线改的那几项。
 *
 * 键名与后端 `settings.go` 的常量逐字一致 —— 它们同时是表里的行键与 PUT 请求体
 * 的字段名，改一个字就写不进去。
 */
export type QyLotEffective = {
  /** 需求原文的「系统设置前端是否显示」。1 = 显示。 */
  show_entry: number
  /** 同时进行中的活动数上限。防止运营一口气开出几十场自己也管不过来。 */
  max_active_activities: number
  /** 竞猜默认手续费（万分比）。 */
  default_guess_fee_bps: number
  /** 竞猜手续费上限。**防的是把 5% 手滑打成 50%**，不是防恶意。 */
  max_guess_fee_bps: number
  /** 单场奖品总额上限。抽奖派奖是净增发，写错一个零就是直接资损。 */
  max_total_prize_quota: number
  /** 超过它只告警不阻断 —— 运营确实可能办大活动。 */
  large_prize_alert_quota: number
}

export type QyLotBound = { min: number; max: number }

/** YAML 只读段：改它要动配置文件并重载。 */
export type QyLotYamlReadonly = {
  enabled: boolean
  proof_public: boolean
  pay_password_threshold_quota: number
  entry_close_grace_seconds: number
  reveal_delay_seconds: number
  payout_max_attempts: number
  max_total_entries_hard: number
  max_prize_tiers: number
  max_options: number
  /** 单笔扣款硬上限。它决定一次报名/投注最多能从主额度扣走多少。 */
  max_stake_quota: number
  spend_max_lookback_days: number
  /**
   * 「近 N 日消费」这个条件从哪一天起才有数据（YYYYMMDD，0 = 尚未回填）。
   *
   * 上线初期日消费表是空的，此时用"近 30 日消费"当条件会**全员误拒**。
   * 所以创建活动时窗口早于这一天就直接拒绝保存 —— 把误拒挡在配置阶段，
   * 而不是等用户报名时才发现。
   */
  spend_ready_from: number
}

export type QyLotAdminConfig = {
  effective: QyLotEffective
  effective_valid: boolean
  overrides: Record<string, string>
  editable_keys: string[]
  bounds: Record<string, QyLotBound>
  yaml_readonly: QyLotYamlReadonly
  yaml_defaults: QyLotEffective
}
