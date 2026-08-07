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
import {
  isQyLotBallPoolValid,
  qyLotBallUnreachableTiers,
  type QyLotBallPool,
} from '../../lottery/lib/ball'
import {
  QY_LOT_EMPTY_RULES,
  type QyLotDrawMode,
  type QyLotKind,
  type QyLotRules,
  type QyLotTier,
} from '../../lottery/types'
import type { QyLotCreateInput, QyLotSeries, QyLotYamlReadonly } from '../types'

/**
 * 创建向导的草稿与校验。
 *
 * ## 为什么校验放在这里而不是散在四个步骤里
 *
 * 四步向导最容易出的错是"第 2 步填的和第 3 步填的合起来不成立"（奖品总额超上限、
 * 开奖时刻早于封盘 + 强制间隔、竞猜只有一个选项）。逐步就地校验看不到这类跨步
 * 冲突，用户会一路点到最后一步才被后端 400 顶回来，而那时前三步的输入都还在
 * 表单里，他不知道该改哪一个。
 *
 * ## 前端校验**不是**权威
 *
 * 下面每一条在后端都有对应的判定（`config.ValidateLottery` 与创建接口）。
 * 这里复现它们只是为了"别让运营白填一遍"，取值区间一律来自接口下发的
 * `yaml_readonly` / `bounds`，前端不自己抄一份常量。
 */

export type QyLotOptionDraft = {
  /**
   * 仅存在于表单期的行标识。
   *
   * 不能用数组下标当 React key：删掉中间一行之后，后面每一行的下标都会前移，
   * React 会把"第 3 行的内容"复用到"原第 4 行的 DOM"上 —— 表现是删了一行，
   * 剩下那些行的输入框内容错位一格。也不能用 `label`：它在用户敲字的过程中
   * 会重复、会为空。
   */
  id: string
  label: string
  is_catch_all: boolean
}

/** 新增一行选项。`id` 只活在表单里，提交时被丢弃。 */
export function qyLotNewOption(isCatchAll = false): QyLotOptionDraft {
  return { id: crypto.randomUUID(), label: '', is_catch_all: isCatchAll }
}

export type QyLotDraft = {
  kind: QyLotKind
  /** 定档方式。只对 `kind='draw'` 有意义，竞猜恒走 `rank` 那一支。 */
  draw_mode: QyLotDrawMode
  /** 双色球所属的期次系列编号；其余玩法恒为空串。 */
  series_no: string
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
  max_entries_per_user: number
  max_attempts_per_user: number
  max_total_entries: number
  max_total_users: number
  cooldown_seconds: number
  dedup_ip: boolean
  tiers: QyLotTier[]
  options: QyLotOptionDraft[]
  rules: QyLotRules
}

/**
 * 新建时的初值。
 *
 * 竞猜默认**带一个「以上都不是」兜底项**：没有它，「全部猜错」会频繁发生，
 * 而那一分支下全场退款、平台零收益。删掉它要走二次确认并明写代价。
 */
export function qyLotEmptyDraft(defaultFeeBps: number): QyLotDraft {
  const now = Math.floor(Date.now() / 1000)
  const hour = 3600
  return {
    kind: 'draw',
    draw_mode: 'rank',
    series_no: '',
    title: '',
    intro: '',
    stake_quota: 0,
    open_at: now + hour,
    close_at: now + 25 * hour,
    draw_at: now + 26 * hour,
    settle_deadline: now + 49 * hour,
    allow_multi_win: false,
    fee_bps: defaultFeeBps,
    min_entries_to_hold: 0,
    max_entries_per_user: 1,
    max_attempts_per_user: 4,
    max_total_entries: 0,
    max_total_users: 0,
    cooldown_seconds: 0,
    dedup_ip: false,
    tiers: [
      {
        tier: 1,
        name: '',
        amount_quota: 0,
        count: 1,
        red_match: 0,
        blue_match: 0,
        pool_share_bps: 0,
      },
    ],
    options: [qyLotNewOption(), qyLotNewOption(), qyLotNewOption(true)],
    rules: { ...QY_LOT_EMPTY_RULES },
  }
}

/** 奖品总额 = Σ(单档金额 × 档位数量)。抽奖派奖是**净增发**，没有奖池兜着。 */
export function qyLotTotalPrizeQuota(draft: QyLotDraft): number {
  if (draft.kind !== 'draw') return 0
  return draft.tiers.reduce(
    (sum, tier) =>
      sum + Math.max(0, tier.amount_quota) * Math.max(0, tier.count),
    0
  )
}

/**
 * 保本参与人数 = ⌈奖品总额 ÷ 单次参与费⌉。
 *
 * 这是运营最需要、也最容易算错的那个数。把它顶在发布之前，等于把一次必然发生的
 * 配置事故（"多写了一个零"）挡在按钮前面，而不是等对账任务事后告警。
 */
export function qyLotBreakEvenEntries(draft: QyLotDraft): number {
  const total = qyLotTotalPrizeQuota(draft)
  if (total <= 0 || draft.stake_quota <= 0) return 0
  return Math.ceil(total / draft.stake_quota)
}

/**
 * 跨步校验。返回 i18n key 列表，空 = 可以提交。
 *
 * 顺序按"用户改起来的成本"排：先说没填的，再说填错的，最后说组合不成立的。
 */
export function qyLotValidateDraft(
  draft: QyLotDraft,
  yaml: QyLotYamlReadonly | undefined,
  maxPrizeQuota: number,
  maxFeeBps: number,
  /** 双色球选中的那个系列（号池与入池比例都在它上面）。其余玩法传 undefined。 */
  series?: QyLotSeries
): string[] {
  const errors: string[] = []

  if (draft.title.trim() === '') errors.push('qy_lot_v_title_required')
  // v1 没有免费场：`stake_quota` 必须 > 0。两阶段入口本身就要求金额 > 0，
  // 0 元要另开一条不动钱的路径 = 第二套状态机 + 第二套幂等 + 第二套补偿。
  if (draft.stake_quota <= 0) errors.push('qy_lot_v_stake_required')

  if (
    draft.open_at <= 0 ||
    draft.close_at <= draft.open_at ||
    draft.draw_at < draft.close_at
  ) {
    errors.push('qy_lot_v_time_order')
  }
  if (draft.settle_deadline > 0 && draft.settle_deadline < draft.draw_at) {
    errors.push('qy_lot_v_deadline_order')
  }
  // 封盘与开奖之间必须留出强制间隔：名单哈希在封盘时公开、种子在开奖时公布，
  // 中间这段时间才是任何人都能抓一份快照的窗口。它是协议成立的前提，不是留白。
  if (
    yaml != null &&
    draft.draw_at - draft.close_at < yaml.reveal_delay_seconds
  ) {
    errors.push('qy_lot_v_reveal_delay')
  }

  if (draft.kind === 'draw') {
    const tiers = draft.tiers
    const isBall = draft.draw_mode === 'ball'
    if (tiers.length === 0) errors.push('qy_lot_v_tier_required')
    if (tiers.some((tier) => tier.name.trim() === '')) {
      errors.push('qy_lot_v_tier_name')
    }
    if (isBall) {
      errors.push(...validateBallDraft(draft, yaml, series))
    } else if (
      tiers.some((tier) => tier.amount_quota <= 0 || tier.count <= 0)
    ) {
      errors.push('qy_lot_v_tier_amount')
    }
    if (new Set(tiers.map((tier) => tier.tier)).size !== tiers.length) {
      errors.push('qy_lot_v_tier_dup')
    }
    if (
      yaml != null &&
      yaml.max_prize_tiers > 0 &&
      tiers.length > yaml.max_prize_tiers
    ) {
      errors.push('qy_lot_v_tier_too_many')
    }
    // 唯一能拦住"多写一个零"的闸门。没有任何下游环节能补救它 —— 派奖是对
    // 用户额度的净增发，钱发出去就回不来了。
    if (maxPrizeQuota > 0 && qyLotTotalPrizeQuota(draft) > maxPrizeQuota) {
      errors.push('qy_lot_v_prize_over_cap')
    }
  } else {
    const labels = draft.options.map((option) => option.label.trim())
    if (labels.length < 2) errors.push('qy_lot_v_option_min')
    if (labels.some((label) => label === '')) {
      errors.push('qy_lot_v_option_label')
    }
    if (new Set(labels).size !== labels.length) {
      errors.push('qy_lot_v_option_dup')
    }
    if (
      yaml != null &&
      yaml.max_options > 0 &&
      draft.options.length > yaml.max_options
    ) {
      errors.push('qy_lot_v_option_too_many')
    }
    if (draft.options.filter((option) => option.is_catch_all).length > 1) {
      errors.push('qy_lot_v_catch_all_dup')
    }
    if (draft.fee_bps < 0 || (maxFeeBps > 0 && draft.fee_bps > maxFeeBps)) {
      errors.push('qy_lot_v_fee_over_cap')
    }
  }

  // 「近 N 日消费」在日消费表回填完成之前会**全员误拒**。把它挡在配置阶段，
  // 而不是等用户报名时才发现自己怎么都不够格。
  if (
    draft.rules.recent_spend_quota > 0 &&
    draft.rules.recent_spend_days > 0 &&
    yaml != null
  ) {
    if (yaml.spend_ready_from === 0) {
      errors.push('qy_lot_v_spend_not_ready')
    } else if (
      yaml.spend_max_lookback_days > 0 &&
      draft.rules.recent_spend_days > yaml.spend_max_lookback_days
    ) {
      errors.push('qy_lot_v_spend_window_too_long')
    }
  }

  if (
    draft.max_entries_per_user > 0 &&
    draft.max_attempts_per_user > 0 &&
    draft.max_attempts_per_user < draft.max_entries_per_user
  ) {
    errors.push('qy_lot_v_attempts_lt_entries')
  }
  if (
    yaml != null &&
    yaml.max_total_entries_hard > 0 &&
    draft.max_total_entries > yaml.max_total_entries_hard
  ) {
    errors.push('qy_lot_v_total_entries_hard')
  }

  return errors
}

/**
 * 双色球独有的那几条。
 *
 * 每一条在后端都有对应判定（`applyBallSpec` / `checkBallTierInput` /
 * `checkBallTierReachability`），这里复现它们只是为了在创建按钮**之前**说清是
 * 哪一档配错了 —— 后端只会回一句 400，而运营那时已经填完四步。
 */
function validateBallDraft(
  draft: QyLotDraft,
  yaml: QyLotYamlReadonly | undefined,
  series: QyLotSeries | undefined
): string[] {
  const errors: string[] = []
  if (draft.series_no === '' || series == null) {
    errors.push('qy_lot_v_ball_series_required')
    return errors
  }
  const pool: QyLotBallPool = {
    redPool: series.red_pool,
    redPick: series.red_pick,
    bluePool: series.blue_pool,
    bluePick: series.blue_pick,
  }
  if (!isQyLotBallPoolValid(pool)) {
    errors.push('qy_lot_v_ball_pool_invalid')
    return errors
  }

  // 本场理论上可能出现的最大有效票数。固定奖档的预算必须不小于它，否则超募时
  // 会有中奖者被摊薄到 0 额度 —— 而派奖计划会跳过 amount<=0 的行，那是一次
  // 静默漏发：用户真的中了，却什么都收不到，也没有任何报错。
  const entriesCap =
    draft.max_total_entries > 0
      ? draft.max_total_entries
      : (yaml?.max_total_entries_hard ?? 0)

  let shareSum = 0
  for (const tier of draft.tiers) {
    const red = tier.red_match ?? 0
    const blue = tier.blue_match ?? 0
    if (red < 0 || red > pool.redPick || blue < 0 || blue > pool.bluePick) {
      errors.push('qy_lot_v_ball_match_range')
    }
    // 一档"红 0 蓝 0"意味着全场每个人都中它，而且它会把后面所有奖级吃光。
    if (red + blue <= 0) errors.push('qy_lot_v_ball_match_zero')

    const share = tier.pool_share_bps ?? 0
    if (share < 0 || share > 10_000) errors.push('qy_lot_v_ball_share_range')
    shareSum += Math.max(0, share)
    if (share > 0) {
      // 浮动奖与固定奖互斥：一档同时写"每人 1000"和"占池 30%"时，到底按哪个发
      // 只能靠代码里的先后顺序回答，而那是最坏的一种规则来源。
      if (tier.amount_quota !== 0) errors.push('qy_lot_v_ball_share_amount')
      continue
    }
    if (tier.amount_quota <= 0 || tier.count <= 0) {
      errors.push('qy_lot_v_ball_fixed_amount')
      continue
    }
    if (entriesCap > 0 && tier.amount_quota * tier.count < entriesCap) {
      errors.push('qy_lot_v_ball_budget_short')
    }
  }
  if (shareSum > 10_000) errors.push('qy_lot_v_ball_share_sum')
  // 门槛不严于更低奖级的那一档永远开不出来：界面上明明写着五等奖，实际一个人
  // 都不可能中，而这种错配在后端之外不会有任何报错。
  if (qyLotBallUnreachableTiers(draft.tiers).length > 0) {
    errors.push('qy_lot_v_ball_unreachable')
  }

  // ── 后端 `checkBallPoolCovers` 的第二次实现 ──────────────────────────
  //
  // 它此前**没有**被复现，于是四步全绿的草稿可以是一个永远发布不出去的活动：
  // 创建成功 → 点发布 → claimSeriesPool 取走池子 → checkBallPoolCovers 判不够
  // → errSeriesPoolShort → 整个事务回滚 → 活动永远停在草稿。复核屏把「系列当前
  // 池子」「固定奖预算合计」「浮动奖占池合计」三个数并排摆着，却从不做那一次减法。
  //
  // `open` 用 `series.pool_quota`：发布期 `act.PoolOpenQuota = s.PoolQuota`
  // 逐字如此（series.go:198），所以这不是估算，是同一个数。池子在创建与发布
  // 之间可能被注资变大，所以这条判定是**保守**的一侧 —— 拦下来的补救办法就在
  // 同一个页面上（系列面板的「注资」），比造出一个自己修不好的草稿好。
  const open = series.pool_quota
  let fixedSum = 0
  for (const tier of draft.tiers) {
    const share = tier.pool_share_bps ?? 0
    if (share > 0) {
      // 浮动奖的"人均至少 1 额度"下限。固定奖那一条在上面按 amount×count 判过，
      // 浮动奖的预算要等取走池子才知道，所以后端把它放在发布期。
      if (entriesCap > 0 && Math.floor((open * share) / 10_000) < entriesCap) {
        errors.push('qy_lot_v_ball_pool_share_short')
      }
      continue
    }
    fixedSum += Math.max(0, tier.amount_quota) * Math.max(0, tier.count)
  }
  if (
    fixedSum + Math.floor((open * Math.min(shareSum, 10_000)) / 10_000) >
    open
  ) {
    errors.push('qy_lot_v_ball_pool_short')
  }

  return errors
}

/**
 * 草稿 → 创建请求体。`opt_no` 在这一刻按顺序编号（1 起，0 是抽奖的保留值）。
 *
 * ## 频次闸门在这里被合进 `rules`
 *
 * 表单把"每人几次 / 冷却 / 同 IP 去重"放在草稿顶层（运营的心智模型是分开的），
 * 但请求体里它们**必须**在 `rules` 内部：后端的 `activityInput` 顶层没有这些
 * 字段，而 `ShouldBindJSON` 对未知字段是**静默丢弃** —— 发错位置不会 400，
 * 只会让整组风控在界面上显示"已设置"而实际一条都没生效，而且这份被丢空的
 * 规则还会进 `rules_hash` → `commit_hash`，公开的承诺文本里写的也是"不限"。
 * 那时连"我确实配过"都举证不出来。
 */
export function qyLotDraftToInput(draft: QyLotDraft): QyLotCreateInput {
  const isBall = draft.kind === 'draw' && draft.draw_mode === 'ball'
  return {
    kind: draft.kind,
    // 竞猜没有定档方式这回事，恒发 rank：后端 `normalizeDrawMode` 只在
    // kind='draw' 那一支读它，但发一个 'ball' 过去会让请求体自相矛盾。
    draw_mode: draft.kind === 'draw' ? draft.draw_mode : 'rank',
    series_no: isBall ? draft.series_no : '',
    title: draft.title.trim(),
    intro: draft.intro,
    stake_quota: draft.stake_quota,
    open_at: draft.open_at,
    close_at: draft.close_at,
    draw_at: draft.draw_at,
    settle_deadline: draft.settle_deadline,
    allow_multi_win: draft.allow_multi_win,
    fee_bps: draft.kind === 'guess' ? draft.fee_bps : 0,
    min_entries_to_hold: draft.min_entries_to_hold,
    bet_min_quota: 0,
    bet_max_quota: 0,
    rules: {
      ...draft.rules,
      max_entries_per_user: draft.max_entries_per_user,
      max_attempts_per_user: draft.max_attempts_per_user,
      max_total_entries: draft.max_total_entries,
      max_total_users: draft.max_total_users,
      cooldown_seconds: draft.cooldown_seconds,
      dedup_ip: draft.dedup_ip,
    },
    prizes: draft.kind === 'draw' ? draft.tiers : [],
    options:
      draft.kind === 'guess'
        ? draft.options.map((option, index) => ({
            opt_no: index + 1,
            label: option.label.trim(),
            is_catch_all: option.is_catch_all,
          }))
        : [],
  }
}
