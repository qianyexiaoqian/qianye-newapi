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
import { parseQyLotRules } from '../../lottery/lib/rules'
import {
  QY_LOT_EMPTY_RULES,
  type QyLotDrawMode,
  type QyLotKind,
  type QyLotOption,
  type QyLotRules,
  type QyLotTier,
} from '../../lottery/types'
import type {
  QyLotAdminActivity,
  QyLotCreateInput,
  QyLotSeries,
  QyLotYamlReadonly,
} from '../types'
import { qyLotEntriesCap, qyLotTierBudgetShort } from './advice'

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
  /**
   * 卡片背景图，两种来源互斥：填一个外链地址，或上传一张图拿到 `cover_ref`。
   *
   * 草稿里两个字段都留着（而不是一个联合类型），是为了让运营在两种来源之间
   * 来回切时不丢掉上一次填的东西 —— 提交时由 {@link qyLotDraftToInput}
   * 按"上传优先"归一成互斥的一对。
   */
  cover_url: string
  cover_ref: string
  stake_quota: number
  /**
   * 竞猜单注上下限，`0 = 不限`。抽奖恒为 0（后端只在 `kind='guess'` 那一支读它）。
   *
   * 它们此前不在草稿里、提交时恒发 0 —— 创建流程因此从来配不出单注上限，
   * 而没有上限时一个大户可以在封盘前几秒压满获胜选项吃掉整个奖池，散户的
   * 期望收益归零。同一个缺口在编辑流程上更糟：一份用接口建出来、配了上限的
   * 草稿被界面改一次就会被静默清零，而界面上没有任何一格提示过这件事。
   */
  bet_min_quota: number
  bet_max_quota: number
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
    cover_url: '',
    cover_ref: '',
    stake_quota: 0,
    bet_min_quota: 0,
    bet_max_quota: 0,
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
        win_ppm: 0,
        red_match: 0,
        blue_match: 0,
        pool_share_bps: 0,
      },
    ],
    options: [qyLotNewOption(), qyLotNewOption(), qyLotNewOption(true)],
    rules: { ...QY_LOT_EMPTY_RULES },
  }
}

/**
 * 已有草稿 → 表单草稿。**编辑流程的唯一入口。**
 *
 * ## 为什么必须从活动行 + 从表整份重建
 *
 * PUT 是**整体替换**语义（后端 `draftUpdates` 写四十来列、奖档与选项整表删了重建），
 * 不是 PATCH。表单里少带回一个字段，提交时那一列就会被写成零值 —— 而界面上
 * 没有任何一格显示过它，运营看不出自己刚刚把"每人限 2 次"改成了"不限"。
 * 这条正是"草稿能改"这件事最容易做成半成品的地方。
 *
 * ## 三处刻意不还原成"运营当初填的那个值"
 *
 * 后端在落库前做过归一化，而**库里那一份才是生效值**。把它显示成运营的原始输入
 * 是撒谎，所以这里一律照抄库值：
 *
 *   · `max_total_entries`：填 0 会被归一成系统硬上限（名单冻结必须有上界）。
 *   · `allow_multi_win`：`draw_mode != 'rank'` 时后端强制为真。
 *   · `fee_bps`：竞猜没填时取运营配置里的默认值。
 *
 * ## 频次那六格从活动行读，不从 `rules_text` 读
 *
 * 两处存的是同一份数据（后端把 `rules` 归一化之后既写进 `rules_text` 也写进
 * 活动行的六列）。取活动行是因为它是**归一化之后**的那一份，而 `rules_text`
 * 里的 `max_total_entries` 同样已经被归一化过 —— 两者一致，但活动行少一次
 * JSON 解析，也不依赖解析成功。`rules_text` 解析失败时退回"全部不限"，
 * 那只影响分组/余额那一批条件，不会把频次闸门一起丢掉。
 */
export function qyLotDraftFromActivity(
  activity: QyLotAdminActivity,
  prizes: QyLotTier[],
  options: QyLotOption[]
): QyLotDraft {
  const rules = parseQyLotRules(activity.rules_text) ?? {
    ...QY_LOT_EMPTY_RULES,
  }
  const drawMode: QyLotDrawMode =
    activity.kind === 'guess' ? 'rank' : (activity.draw_mode ?? 'rank')
  return {
    kind: activity.kind,
    draw_mode: drawMode === '' ? 'rank' : drawMode,
    series_no: activity.series_no ?? '',
    title: activity.title,
    intro: activity.intro,
    cover_url: activity.cover_url ?? '',
    cover_ref: activity.cover_ref ?? '',
    stake_quota: activity.stake_quota,
    bet_min_quota: activity.bet_min_quota,
    bet_max_quota: activity.bet_max_quota,
    open_at: activity.open_at,
    close_at: activity.close_at,
    draw_at: activity.draw_at,
    settle_deadline: activity.settle_deadline,
    allow_multi_win: activity.allow_multi_win,
    fee_bps: activity.fee_bps,
    min_entries_to_hold: activity.min_entries_to_hold,
    max_entries_per_user: activity.max_entries_per_user,
    max_attempts_per_user: activity.max_attempts_per_user,
    max_total_entries: activity.max_total_entries,
    max_total_users: activity.max_total_users,
    cooldown_seconds: activity.cooldown_seconds,
    dedup_ip: activity.dedup_ip,
    // 奖档整份带回来，包括表单上没有输入格的 `prize_type` / `text_desc`：
    // 提交时 `qyLotDraftToInput` 原样透传它们，界面改不了的字段也就不会被
    // 一次"只改了标题"的保存悄悄清空。
    tiers: activity.kind === 'draw' ? prizes.map((tier) => ({ ...tier })) : [],
    options:
      activity.kind === 'guess'
        ? options.map((option) => ({
            id: crypto.randomUUID(),
            label: option.label,
            is_catch_all: option.is_catch_all,
          }))
        : [],
    // 频次那六格在提交时由 `qyLotDraftToInput` 从草稿顶层覆盖回 `rules`，
    // 所以这里放解析出来的原值即可，覆不覆盖结果相同。
    rules: {
      ...rules,
      max_per_inviter: activity.max_per_inviter,
    },
  }
}

/**
 * 玩法 —— **界面上的一级选择**，三选一（需求 6）。
 *
 * ── 为什么前端要多一个概念，而不是照搬后端的 kind × draw_mode ──
 *
 * 项目方原话：「抽奖竞猜页面，没有发现"双色球"活动 UI 界面和配置活动界面。」
 * 双色球的前端其实早就写完了，问题是它埋在 `kind='draw'` 之下的二级「摇号方式」
 * 里 —— 要先选「抽奖」，再在另一个下拉里选「双色球」。而对运营来说它根本不是
 * 抽奖的一个参数：用户自己选号、按红蓝球命中数定档、奖池由期次系列滚存，
 * 三件事没有一件与「排名抽奖」共享。埋两层的直接后果就是这条反馈：找不到。
 *
 * 后端的 `kind` / `draw_mode` **一个字节都不改**：`kind` 是生命周期任务的扫表
 * 维度，新增一个 kind 要在四处各补一个分支，漏一处就是一条静默死路（见
 * `types.ts` 上的说明）。所以这里只是一个**纯展示投影** —— 三个一级选项在提交
 * 时仍然落回 (kind, draw_mode) 那两个字段。
 */
export type QyLotPlay = 'ball' | 'draw' | 'guess'

/** 草稿 → 它此刻属于哪个玩法。竞猜没有 `draw_mode` 这回事，先判 kind。 */
export function qyLotPlayOf(draft: QyLotDraft): QyLotPlay {
  if (draft.kind === 'guess') return 'guess'
  return draft.draw_mode === 'ball' ? 'ball' : 'draw'
}

/**
 * 一级选择 → 要打进草稿的补丁。
 *
 * 三条归位规则少一条都会留下一个自相矛盾的草稿，而表单上看不出来：
 *   · 切到竞猜：`draw_mode` 必须回 `rank` —— 提交时 `kind='guess'` 那一支不读
 *     它，但切回抽奖时会留着一个上次选的 `ball`，而对应的号池表单不显示。
 *   · 切离双色球：`series_no` 必须清掉 —— 后端对带期次的非双色球活动是拒绝，
 *     而那个字段此刻在界面上已经不可见，运营看不出请求为什么失败。
 *   · 切到双色球：`draw_mode` 置 `ball`，`series_no` 留着（同一个系列反复开期
 *     是常态，清掉只会让人每次重选）。
 */
export function qyLotDraftForPlay(
  draft: QyLotDraft,
  play: QyLotPlay
): QyLotDraft {
  if (play === 'guess') {
    return { ...draft, kind: 'guess', draw_mode: 'rank', series_no: '' }
  }
  if (play === 'ball') {
    return { ...draft, kind: 'draw', draw_mode: 'ball' }
  }
  return {
    ...draft,
    kind: 'draw',
    // 从双色球切回普通抽奖时 `draw_mode` 必须换掉，否则表单显示的是「抽奖」
    // 而提交的是一场双色球。已经是 rank/prob 的就别动 —— 那是运营自己选的。
    draw_mode: draft.draw_mode === 'ball' ? 'rank' : draft.draw_mode,
    series_no: '',
  }
}

/** 概率制：全部奖档的中奖概率之和（ppm）。剩下的那一段就是未中奖区间。 */
export function qyLotTotalWinPpm(draft: QyLotDraft): number {
  return draft.tiers.reduce(
    (sum, tier) => sum + Math.max(0, tier.win_ppm ?? 0),
    0
  )
}

/**
 * 「允许多次中奖」的**生效值**。
 *
 * 后端对 `draw_mode != 'rank'` 无条件置真（`api_admin.go` 的 buildActivity）：
 * 概率制每张票独立摇号，按 user_ref 去重会让单票概率变成 1-(1-p)^k，公示的
 * 概率就不再为真；双色球同理。这个字段仍然原样进 commit 原像，所以复核屏
 * 必须显示生效值 —— 在一屏标着「即将被永久冻结」的地方显示草稿值是撒谎。
 */
export function qyLotEffectiveAllowMultiWin(draft: QyLotDraft): boolean {
  if (draft.kind !== 'draw') return draft.allow_multi_win
  return draft.draw_mode === 'rank' ? draft.allow_multi_win : true
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
  // 外链的形状校验。后端 `normalizeCoverURL` 有一份对应判定，这里复现它只是
  // 为了别让运营走完四步才吃一个 400 —— 而"地址填错了"是这一格唯一会犯的错。
  if (draft.cover_ref === '' && draft.cover_url.trim() !== '') {
    if (!/^https?:\/\//i.test(draft.cover_url.trim())) {
      errors.push('qy_lot_v_cover_url')
    }
  }
  // v1 没有免费场：`stake_quota` 必须 > 0。两阶段入口本身就要求金额 > 0，
  // 0 元要另开一条不动钱的路径 = 第二套状态机 + 第二套幂等 + 第二套补偿。
  if (draft.stake_quota <= 0) errors.push('qy_lot_v_stake_required')
  // 站点自选的单笔扣款硬顶，**0 = 不限**（默认）。后端 `buildActivity` 有一份
  // 对应判定，这里复现它只是为了别让运营走完四步才吃一个 400。
  if (
    yaml != null &&
    yaml.max_stake_quota > 0 &&
    draft.stake_quota > yaml.max_stake_quota
  ) {
    errors.push('qy_lot_v_stake_over_cap')
  }

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
    // 概率制的三条硬约束，后端 `normalizeWinPpm` / `Bands` 各有一条对应判定。
    // 少了它们，运营会走完四步、在复核屏看到全绿，然后吃一个 400。
    if (draft.draw_mode === 'prob') {
      if (
        tiers.some(
          (tier) => (tier.win_ppm ?? 0) <= 0 || (tier.win_ppm ?? 0) > 1_000_000
        )
      ) {
        errors.push('qy_lot_v_win_ppm_range')
      }
      if (qyLotTotalWinPpm(draft) > 1_000_000) {
        errors.push('qy_lot_v_win_ppm_sum')
      }
      // 均分制唯一的新失败态：预算摊到人均不足 1 额度时会有人分到 0，
      // 而那个人连 payout 行都不会有 —— 一个真中了奖的人被静默漏发。
      //
      // 判据走 `qyLotTierBudgetShort`，不在这里就地写一遍不等式：字段旁边的
      // 实时提示与「按参与上限自动填」用的是同一个函数，三处分叉的表现就是
      // "界面说 OK、后端拒绝"，而那会让人从此不信任何一个自动填。
      const entriesCap = qyLotEntriesCap(draft, yaml?.max_total_entries_hard)
      if (
        tiers.some((tier) =>
          qyLotTierBudgetShort(entriesCap, tier.count, tier.amount_quota)
        )
      ) {
        errors.push('qy_lot_v_prob_budget_short')
      }
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
    // 单注上下限：后端 `applyBetBounds` 的三条判定各复现一次。
    // `0 = 不限`，所以只有上限非零时才比大小。
    if (draft.bet_min_quota < 0 || draft.bet_max_quota < 0) {
      errors.push('qy_lot_v_bet_negative')
    } else if (
      draft.bet_max_quota > 0 &&
      draft.bet_min_quota > draft.bet_max_quota
    ) {
      errors.push('qy_lot_v_bet_order')
    }
    // 单注上限同样受站点硬顶约束（0 = 不限）。它与 `stake_quota` 共用
    // `max_stake_quota`：后端 `applyBetBounds` 与 `acceptAmount` 也是同一个值。
    if (
      yaml != null &&
      yaml.max_stake_quota > 0 &&
      draft.bet_max_quota > yaml.max_stake_quota
    ) {
      errors.push('qy_lot_v_bet_over_cap')
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
  const entriesCap = qyLotEntriesCap(draft, yaml?.max_total_entries_hard)

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
    if (qyLotTierBudgetShort(entriesCap, tier.count, tier.amount_quota)) {
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
export function qyLotDraftToInput(
  draft: QyLotDraft,
  /**
   * 「我看清了这场活动最坏会发出多少站内余额」的回执。
   *
   * 默认 0 = **没确认**。调用方只有在运营真的勾过那个不可逆确认框之后才把
   * {@link qyLotTotalPrizeQuota} 传进来 —— 无条件回填等于让这道确认自我满足，
   * 那样它一行代码都没少写，却什么都没拦住。
   */
  confirmNetIssueQuota = 0
): QyLotCreateInput {
  const isBall = draft.kind === 'draw' && draft.draw_mode === 'ball'
  return {
    kind: draft.kind,
    // 竞猜没有定档方式这回事，恒发 rank：后端 `normalizeDrawMode` 只在
    // kind='draw' 那一支读它，但发一个 'ball' 过去会让请求体自相矛盾。
    draw_mode: draft.kind === 'draw' ? draft.draw_mode : 'rank',
    series_no: isBall ? draft.series_no : '',
    title: draft.title.trim(),
    intro: draft.intro,
    // 两种来源在这一刻归一成互斥的一对，**上传优先**：后端对两者同时非空是
    // 直接 400，而运营在表单里先传了图又顺手粘了个地址是很正常的操作序列。
    // 归一放在提交这一刻而不是编辑时，草稿里就还留着他填过的另一个。
    cover_url: draft.cover_ref === '' ? draft.cover_url.trim() : '',
    cover_ref: draft.cover_ref,
    stake_quota: draft.stake_quota,
    open_at: draft.open_at,
    close_at: draft.close_at,
    draw_at: draft.draw_at,
    settle_deadline: draft.settle_deadline,
    allow_multi_win: draft.allow_multi_win,
    fee_bps: draft.kind === 'guess' ? draft.fee_bps : 0,
    min_entries_to_hold: draft.min_entries_to_hold,
    // 单注上下限只对竞猜有意义（后端 `applyBetBounds` 只在那一支被调用）。
    // 抽奖恒发 0，与 `fee_bps` 同一条口径：发一个不会生效的值过去，
    // 界面上就会显示"已设置"而实际一条都没生效。
    bet_min_quota: draft.kind === 'guess' ? draft.bet_min_quota : 0,
    bet_max_quota: draft.kind === 'guess' ? draft.bet_max_quota : 0,
    rules: {
      ...draft.rules,
      max_entries_per_user: draft.max_entries_per_user,
      max_attempts_per_user: draft.max_attempts_per_user,
      max_total_entries: draft.max_total_entries,
      max_total_users: draft.max_total_users,
      cooldown_seconds: draft.cooldown_seconds,
      dedup_ip: draft.dedup_ip,
    },
    // 奖档按玩法归一化，与后端强制的那组恒等式逐条对齐：
    //   · rank / ball：win_ppm 必须为 0（后端对填了它的请求直接 400）
    //   · rank / prob：红蓝命中数与占池比例必须为 0（后端静默忽略它们，
    //     于是一个从双色球切回普通抽奖的运营会以为占池比例还在生效）
    // 归一化放在提交这一刻而不是切换玩法时：草稿里留着上次填的值，
    // 运营切回去还能看到自己填过什么。
    prizes:
      draft.kind === 'draw'
        ? draft.tiers.map((tier) => ({
            ...tier,
            win_ppm: draft.draw_mode === 'prob' ? (tier.win_ppm ?? 0) : 0,
            red_match: isBall ? (tier.red_match ?? 0) : 0,
            blue_match: isBall ? (tier.blue_match ?? 0) : 0,
            pool_share_bps: isBall ? (tier.pool_share_bps ?? 0) : 0,
          }))
        : [],
    options:
      draft.kind === 'guess'
        ? draft.options.map((option, index) => ({
            opt_no: index + 1,
            label: option.label.trim(),
            is_catch_all: option.is_catch_all,
          }))
        : [],
    confirm_net_issue_quota: confirmNetIssueQuota,
  }
}
