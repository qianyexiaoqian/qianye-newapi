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
  QY_LOT_EMPTY_RULES,
  type QyLotKind,
  type QyLotRules,
  type QyLotTier,
} from '../../lottery/types'
import type { QyLotCreateInput, QyLotYamlReadonly } from '../types'

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
    tiers: [{ tier: 1, name: '', amount_quota: 0, count: 1 }],
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
  maxFeeBps: number
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
    if (tiers.length === 0) errors.push('qy_lot_v_tier_required')
    if (tiers.some((tier) => tier.name.trim() === '')) {
      errors.push('qy_lot_v_tier_name')
    }
    if (tiers.some((tier) => tier.amount_quota <= 0 || tier.count <= 0)) {
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
  return {
    kind: draft.kind,
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
