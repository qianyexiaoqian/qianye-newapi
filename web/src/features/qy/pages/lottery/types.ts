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
 * 抽奖 / 竞猜的前端 DTO。
 *
 * 与 `qianye/modules/lottery` 的 HTTP 契约一一对应（设计文档 §11）。
 * **管理端与用户端共用本文件的类型**：同一个活动在两边显示的是同一件事，
 * 两份声明必然漂移成"管理员看到的字段用户端读不到"。
 *
 * 字段名一个都不能改：证据链的验证脚本（`lib/verify.ts` 与仓库里的
 * `qianye/docs/lottery-verify.py`）逐字节依赖它们，改名等于换了一套协议。
 */

/** `draw` 抽奖（平台出奖品），`guess` 竞猜（奖池再分配）。 */
export type QyLotKind = 'draw' | 'guess'

/**
 * 开奖模式（协议 `lot-v2`）。**它是活动行上的一列，不是新的 `kind`**。
 *
 * - `rank`：按名次抽 N 个中奖者（`lot-v1` 的唯一行为，空串等价于它）。
 * - `prob`：每张票各摇一次号，落进哪个概率区间就中哪一档；**可以没有人中**。
 * - `ball`：双色球，全场共摇一次号，用户自选号码，按命中数定档。
 *
 * 为什么不是新的 `kind`：生命周期任务按 `kind='draw'` / `kind='guess'` 扫表，
 * 报名与资格判定也在 kind 上分支。新增一个 kind 要在四处各补一个分支，漏一处
 * 就是一条静默死路；新增 draw_mode 则整条生命周期一行不用改。
 *
 * 老活动不下发这个字段，因此**必须容忍 `undefined`**：一律按 `rank` 处理。
 */
export type QyLotDrawMode = '' | 'ball' | 'prob' | 'rank' | (string & {})

/**
 * 奖品形态。`quota` = 额度（走资金链路），`text` = 一段文本（兑换码 / CDK /
 * 实物说明，**只对中奖者展示、由管理员手动履行**）。
 *
 * 单选，不允许一档既给额度又给文本 —— 那会让派奖在同一行里分叉。要两者请配两档。
 */
export type QyLotPrizeType = '' | 'quota' | 'text' | (string & {})

/** 概率的分母。`win_ppm` 是百万分比，1_000_000 = 必中。 */
export const QY_LOT_PPM_DEN = 1_000_000

/**
 * 活动状态机。**单向线性，没有回退边**。
 *
 * `draft → published → locked → settling → finished`
 */
export type QyLotStatus =
  | 'draft'
  | 'finished'
  | 'locked'
  | 'published'
  | 'settling'
  | (string & {})

/**
 * 结局。与 {@link QyLotStatus} 分开是刻意的：取消 / 流局 / 全错三种结局复用
 * 同一条 `settling → finished` 的退款路径，不各建一个状态。
 *
 * 空串 = 还没有结局（活动进行中）。
 */
export type QyLotOutcome =
  | ''
  | 'cancelled'
  | 'drawn'
  | 'void_all_correct'
  | 'void_deadline'
  | 'void_min_entries'
  | 'void_no_winner'
  | (string & {})

/** 参与条目状态。`excluded` = 封盘时仍未落定，退款由资金单终态驱动。 */
export type QyLotEntryStatus =
  | 'excluded'
  | 'failed'
  | 'pending'
  | 'refunded'
  | 'success'
  | (string & {})

/**
 * 派奖行状态。`held` = 自动重试已放弃，等人工。
 *
 * `granted` 是**文本奖专用的终态**：文本奖不动钱，开奖事务提交那一刻就已到位，
 * 因此它天然不在出款 worker 的扫描集合（`planned/paying/failed`）里。
 * 安全性来自这个结构，而不是靠某处记得写一个 `prize_type != 'text'` 的过滤。
 */
export type QyLotPayoutStatus =
  | 'failed'
  | 'granted'
  | 'held'
  | 'paid'
  | 'paying'
  | 'planned'
  | (string & {})

/**
 * 出款类型：中奖 / 竞猜赔付 / 退款 / 文本奖。
 *
 * 前三者都是"主库加额度"；`text` 一分钱都不动，它只是"这张票中了一份文本奖"
 * 的账面记录 —— 兑现由管理员手工完成。
 */
export type QyLotPayoutKind =
  | 'prize'
  | 'refund'
  | 'text'
  | 'win'
  | (string & {})

// ───────────────────────────── 奖档 / 选项 ─────────────────────────────

/**
 * 抽奖奖档。一行进一次 `spec_hash` 原像：`tier␟name␟amount_quota␟count`。
 */
export type QyLotTier = {
  tier: number
  name: string
  amount_quota: number
  count: number

  // ── 协议 lot-v2 追加的六列。全部**可选**：`lot-v1` 的活动不下发它们，
  //    而历史活动的证据链必须原样验得通，所以这里绝不能改成必填。
  //    进 `spec_hash` 原像的顺序见 `lib/verify.ts` 的 `qyLotSpecLineV2`。

  /** 奖品形态。缺省（`lot-v1`）等价于 `quota`。 */
  prize_type?: QyLotPrizeType
  /**
   * 中奖概率（百万分比）。仅 `draw_mode='prob'` 有意义，其余模式恒为 0。
   *
   * 各档按 tier 升序累加，占据互不相交的左闭右开区间；摇号量落在全部区间之外
   * 即未中奖 —— 这是一等公民结果，不是异常分支。
   */
  win_ppm?: number
  /**
   * 文本奖的**公开**履行说明（如"请在 8 月 31 日前联系客服领取"）。
   *
   * 它明文进 `spec_hash` 原像，因为它本来就公开。**绝不能往这里写兑换码**：
   * 证据链是匿名可访问的。真正的码由管理员在履行时单独填入，不进任何哈希。
   */
  text_desc?: string
  /** 双色球：本档要求命中的红球 / 蓝球个数。 */
  red_match?: number
  blue_match?: number
  /** 双色球：本档从奖池里分走的万分比（浮动奖）。 */
  pool_share_bps?: number
}

/**
 * 竞猜选项。进 `spec_hash` 原像的只有前三个字段
 * （`opt_no␟label␟is_catch_all`）——后两个是实时计数，会随投注变化，
 * 放进承诺就没人能复算了。
 *
 * `bet_quota` / `bet_count` 标为可选：证据链端点只返回被承诺的那三个字段，
 * 活动详情才带实时盘口。UI 拿不到时整列隐藏，而不是显示 0（那是错的数）。
 */
export type QyLotOption = {
  opt_no: number
  label: string
  is_catch_all: boolean
  bet_quota?: number
  bet_count?: number
  is_winner?: boolean
}

/**
 * 奖档 / 选项集合在**线上的形状**：一个扁平数组，抽奖填 tier 那一组字段、
 * 竞猜填 opt_no 那一组。
 *
 * 它不是 `{tiers, options}` 那种更顺手的对象，而且不能改成那样：`spec` 的逐行
 * 编码进了 `spec_hash` → `commit_hash`，字段名与顺序是协议的一部分，
 * 三份实现（Go / 本文件 / verify.py）逐字节对齐。要分组请用下面两个取数函数。
 */
export type QyLotSpecItem = {
  tier?: number
  name?: string
  amount_quota?: number
  count?: number
  prize_type?: QyLotPrizeType
  win_ppm?: number
  text_desc?: string
  red_match?: number
  blue_match?: number
  pool_share_bps?: number
  opt_no?: number
  label?: string
  is_catch_all?: boolean
  bet_quota?: number
  bet_count?: number
  is_winner?: boolean
}

/** 从扁平 spec 里取出抽奖奖档，按 tier 升序（与 `spec_hash` 的原像顺序一致）。 */
export function qyLotTiers(spec: QyLotSpecItem[] | undefined): QyLotTier[] {
  return (spec ?? [])
    .filter((item) => item.tier != null && item.tier > 0)
    .map((item) => ({
      tier: item.tier ?? 0,
      name: item.name ?? '',
      amount_quota: item.amount_quota ?? 0,
      count: item.count ?? 0,
      // v2 六列一律补齐成"恒等式取值"（quota 档 text_desc=''、非 prob
      // win_ppm=0、非 ball 三列 =0），这样 `qyLotSpecLineV2` 可以无分支地
      // 拼原像 —— 后端创建活动时强制的正是同一组恒等式。
      prize_type: item.prize_type ?? '',
      win_ppm: item.win_ppm ?? 0,
      text_desc: item.text_desc ?? '',
      red_match: item.red_match ?? 0,
      blue_match: item.blue_match ?? 0,
      pool_share_bps: item.pool_share_bps ?? 0,
    }))
    .sort((a, b) => a.tier - b.tier)
}

/** 该档是不是文本奖。缺省（`lot-v1`）一律是额度奖。 */
export function isQyLotTextPrize(tier: Pick<QyLotTier, 'prize_type'>): boolean {
  return tier.prize_type === 'text'
}

/** 从扁平 spec 里取出竞猜选项，按 opt_no 升序。 */
export function qyLotOptions(spec: QyLotSpecItem[] | undefined): QyLotOption[] {
  return (spec ?? [])
    .filter((item) => item.opt_no != null && item.opt_no > 0)
    .map((item) => ({
      opt_no: item.opt_no ?? 0,
      label: item.label ?? '',
      is_catch_all: item.is_catch_all ?? false,
      bet_quota: item.bet_quota,
      bet_count: item.bet_count,
      is_winner: item.is_winner,
    }))
    .sort((a, b) => a.opt_no - b.opt_no)
}

// ───────────────────────────── 参与条件 ─────────────────────────────

/**
 * 参与条件。整体规范化成 JSON 落库并进 `rules_hash`，**publish 后只读**。
 *
 * 数值型条件一律 `0 = 不限`；布尔型 `false = 不限制`。这条口径必须与后端
 * `Rules.Normalize()` 完全一致，否则同一份规则在两边给出两种结论。
 */
export type QyLotRules = {
  /** 分组白名单。空 = 不限分组。 */
  allow_groups: string[]
  /** 分组黑名单。与白名单同时存在时黑名单优先（"排除"永远压过"允许"）。 */
  deny_groups: string[]
  /** 注册满 N 天。 */
  min_account_age_days: number
  /** 当前余额不低于 N（额度）。判定时刻是**报名那一瞬间**，不是长期持有。 */
  min_quota: number
  /** 累计消费不低于 N（额度，全时段，取自主库 `used_quota`，不可切窗）。 */
  min_used_quota: number
  /** 「近 N 日」的窗口长度；配合 {@link recent_spend_quota} 才生效。 */
  recent_spend_days: number
  /** 近 N 日消费不低于 N（额度）。 */
  recent_spend_quota: number
  /** 有违规记录的排除。影子命中与已撤销记录**不计入**（后端口径）。 */
  exclude_violation: boolean
  /**
   * 滚动窗口内允许的违规命中数上限；0 = 不限。
   *
   * 与 {@link exclude_violation} 并列而不是二选一：运营可能只想挡"最近很闹腾"
   * 的人，而不是任何一条历史记录都一票否决。
   */
  max_violation_hits: number
  /** 被自动封禁过的排除（查 `qy_violation_ban`）。 */
  exclude_ever_auto_banned: boolean
  /** 当前被禁用的排除（查 `users.status`，管理员手工封禁只体现在这里）。 */
  exclude_currently_disabled: boolean
  /** 要求已绑定邮箱。 */
  require_email: boolean
  /** 要求已绑定任一 OAuth。 */
  require_oauth: boolean
  /** 要求已设置支付密码。 */
  require_pay_password: boolean

  // ── 频次与去重 ──
  //
  // 它们同样是参与条件，所以住在 `rules` 里而不是请求体顶层：必须进
  // `rules_hash` → `commit_hash`，否则事后改上限就是改规则而不被任何验证检出。
  /** 每人参与上限；0 = 不限。 */
  max_entries_per_user: number
  /** 每人提交上限（**含失败的尝试**）；0 = 不限。 */
  max_attempts_per_user: number
  /** 全场参与条目上限；0 = 用系统硬上限。 */
  max_total_entries: number
  /** 全场人数上限；0 = 不限。与条目上限分开：允许多次参与时人数上限拦不住刷单。 */
  max_total_users: number
  /** 同一邀请人名下最多 N 个下线参与；0 = 不限。 */
  max_per_inviter: number
  /** 两次参与的最小间隔（秒）；0 = 不限。 */
  cooldown_seconds: number
  /** 同一 IP 只算一人。**默认关闭**，代价见管理端表单上的说明。 */
  dedup_ip: boolean
}

/** 全部条件为空的规则。新建表单的初值，也是"不限"的规范写法。 */
export const QY_LOT_EMPTY_RULES: QyLotRules = {
  allow_groups: [],
  deny_groups: [],
  min_account_age_days: 0,
  min_quota: 0,
  min_used_quota: 0,
  recent_spend_days: 0,
  recent_spend_quota: 0,
  exclude_violation: false,
  max_violation_hits: 0,
  exclude_ever_auto_banned: false,
  exclude_currently_disabled: false,
  require_email: false,
  require_oauth: false,
  require_pay_password: false,
  max_entries_per_user: 0,
  max_attempts_per_user: 0,
  max_total_entries: 0,
  max_total_users: 0,
  max_per_inviter: 0,
  cooldown_seconds: 0,
  dedup_ip: false,
}

/**
 * 一条没满足的条件。
 *
 * `need` / `have` 是任意标量（额度、天数、分组名、布尔），前端按 `code` 决定
 * 怎么把它变成人话 —— 后端不负责措辞，它只负责说清"差在哪、差多少"。
 */
export type QyLotMissing = {
  code: string
  need?: boolean | number | string | null
  have?: boolean | number | string | null
}

export type QyLotEligibility = {
  eligible: boolean
  missing: QyLotMissing[]
}

// ───────────────────────────── 活动 ─────────────────────────────

/** 大厅卡片。刻意不带 `rules_text` / `commit_hash`：列表页用不上，白占带宽。 */
export type QyLotActivityBrief = {
  act_no: string
  kind: QyLotKind
  status: QyLotStatus
  outcome: QyLotOutcome
  title: string
  stake_quota: number
  open_at: number
  close_at: number
  draw_at: number
  active_count: number
  pool_quota: number
  /** 抽奖没有奖池概念（平台出奖品），这里给的是奖品总额度 —— 对用户而言那才是"能赢多少"。 */
  prize_total_quota: number
  my_entry_count: number
}

export type QyLotActivityDetail = {
  act_no: string
  kind: QyLotKind
  /** 协议 lot-v2；`lot-v1` 的活动不下发，按 `rank` 处理。 */
  draw_mode?: QyLotDrawMode
  status: QyLotStatus
  outcome: QyLotOutcome
  title: string
  intro: string
  stake_quota: number
  open_at: number
  close_at: number
  draw_at: number
  settle_deadline: number
  /** publish 时冻结的承诺哈希。`draft` 阶段为空串。 */
  commit_hash: string
  /** 规范化 JSON 原文。**哈希的就是这份字节**，前端只解析、绝不重序列化。 */
  rules_text: string
  spec: QyLotSpecItem[]
  active_count: number
  pool_quota: number
  fee_bps: number
  min_entries_to_hold: number
  allow_multi_win: boolean
  my_entry_count: number
  /** 竞猜已公布的获胜选项号；0 = 还没录。 */
  win_opt_no: number
  /** 结果凭据（URL / 文本 / 哈希），对用户公开。 */
  result_evidence: string
  /** 单注上下限，0 = 不限（仍受全局上限兜底）。抽奖恒为 0。 */
  bet_min_quota: number
  bet_max_quota: number
  max_entries_per_user: number
  cooldown_seconds: number
  dedup_ip: boolean
  /** 按活动的基准参与费判定是否要验支付密码；竞猜自选更大的金额时由后端按本次金额重算。 */
  pay_password_required: boolean
  /** 阈值本身，0 = 关闭。让投注额输入框旁边能直接写清"超过多少要验密码"。 */
  pay_password_threshold_quota: number
}

// ───────────────────────────── 我的参与 ─────────────────────────────

/** 报名回执。**这就是用户手里的凭据**，前端必须持久展示而不是弹一下就没。 */
export type QyLotEntryReceipt = {
  entry_no: string
  seq: number
  chain_hash: string
  commit_hash: string
  user_ref: string
  amount: number
}

export type QyLotMyEntry = {
  entry_no: string
  act_no: string
  title: string
  kind: QyLotKind
  seq: number
  chain_hash: string
  user_ref: string
  amount: number
  status: QyLotEntryStatus
  opt_no: number
  /**
   * 中奖 / 赔付 / 退款 / 文本奖的结果；未中奖或尚未结算为 `null`。
   *
   * 玩法（`draw_mode`）**刻意不在这里**：「为什么是这个结果」的每一个数字都
   * 从证据链里现算，玩法自然也从那份文档里读。列表行上再放一份，就多了一个
   * 会与证据链漂移的取值。
   */
  won: {
    /** `text` = 文本奖：它没有金额，要点开才看得到内容。 */
    kind: QyLotPayoutKind
    tier: number
    amount: number
    status?: QyLotPayoutStatus
    /**
     * **取文本奖内容的唯一钥匙**（`GET /lottery/my/prizes/:payout_no`）。
     * 由服务端 crypto/rand 生成、不可枚举，所以拿它当路径参数是安全的。
     */
    payout_no?: string
    /** 文本奖是否已由管理员履行。未履行时点开看到的是"等履行"而不是空白。 */
    fulfilled?: boolean
  } | null
  created_at: number
}

// ───────────────────────────── 文本奖 ─────────────────────────────

/**
 * 「我中的那一份文本奖」。
 *
 * ## 为什么是逐条拉取而不是一张列表
 *
 * 一个返回全部正文的列表接口，意味着**一次越权 bug 就是全量泄漏**。逐条拉取
 * 把泄漏面压到单条，而 `payout_no` 不可枚举，因此这个限制没有可用性代价。
 *
 * ## 为什么 `secret` 可以缺席
 *
 * 项目方已拍板"管理员手动履行"，所以码在开奖时**根本不存在**——它由管理员在
 * 履行那一刻填入。未履行时这里返回 `status='pending'` 与公开的 `text_desc`，
 * 而不是一个空字符串：空串会让界面显示成"奖品是空的"。
 */
export type QyLotMyPrize = {
  payout_no: string
  act_no: string
  title: string
  tier: number
  /** 奖档名（公开，进 `spec_hash`）。 */
  name: string
  /** 公开的履行说明（进 `spec_hash`）。 */
  text_desc: string
  /** `pending` = 管理员还没履行；`fulfilled` = 已填入内容。 */
  status: 'fulfilled' | 'pending' | (string & {})
  /** 实际的兑换码 / CDK。**只有 `fulfilled` 时才有值**。 */
  secret?: string
  /** 管理员填的备注（领取方式、有效期之类）。 */
  note?: string
  fulfilled_at: number
  /**
   * 那条诚实边界，**由后端随奖品一起下发**：这串码没有进入承诺。
   *
   * 写死在前端也能显示，但那样它就是一句可以被悄悄改掉的营销话术；从后端来
   * 意味着它与发放这件事同源，改它要动的是同一份代码。
   */
  notice: string
}

// ───────────────────────────── 证据链 ─────────────────────────────

/**
 * 证据链条目。字段名与设计文档 §3 的验证伪代码逐字一致。
 *
 * `order_no` 是给非 `success` 条目交叉复核用的：哈希链刻意不含 `status`
 * （否则每次扣费失败都会破链），所以"这一条到底成没成"由资金单佐证。
 */
export type QyLotProofEntry = {
  seq: number
  entry_no: string
  user_ref: string
  opt_no: number
  amount: number
  status: QyLotEntryStatus
  prev_hash: string
  chain_hash: string
  order_no: string
  /**
   * 双色球选号，规范化格式 `03,05,12|08`（`lot-v2`，其余模式为空串）。
   *
   * 它**必须**进哈希链与名单行的原像：否则平台可以在开奖后把某个人的号改成
   * 中奖号，而链尾、计数、名单重算三道校验会照常全部通过。
   */
  pick?: string
}

export type QyLotProofWinner = {
  pos: number
  tier: number
  entry_no: string
  user_ref: string
  amount: number
  /** `lot-v2`：`text` 档的中奖位。金额恒为 0，兑现由管理员手工完成。 */
  prize_type?: QyLotPrizeType
  /**
   * 文本奖是否已履行。**只有布尔，绝不带内容、也不带内容的哈希** ——
   * 证据链是匿名可访问的，往里放一个可离线爆破的兑换码哈希，与直接公开
   * 发放只差几分钟算力。
   */
  fulfilled?: boolean
}

/**
 * 一笔出款。`kind` 必须在:同一场活动里可能同时存在赔付与退款
 * （封盘时还没落定的条目走退款），复算分配时只能比对赔付那一类，
 * 把退款混进来会让一场诚实的结算被判成"逐笔赔付对不上"。
 */
export type QyLotProofPayout = {
  entry_no: string
  kind: QyLotPayoutKind
  amount: number
  status: QyLotPayoutStatus
}

/**
 * 完整证据链。**自洽**：验证者拿到这一份 JSON 就能离线复算，不需要再访问本站。
 *
 * `entries` 分页，`?format=ndjson` 可全量下载。分页取回的那一份**不能**用来
 * 验证链与名单 —— `lib/verify.ts` 会先检查 `entries.length === total`。
 */
export type QyLotProof = {
  /** `lot-v1` 或 `lot-v2`。**未知版本一律拒绝验证**，不给一个可能是假的绿勾。 */
  algo: string
  act_no: string
  kind: QyLotKind
  status: QyLotStatus
  outcome: QyLotOutcome
  title: string

  rules_text: string
  rules_hash: string
  spec: QyLotSpecItem[]
  spec_hash: string

  stake_quota: number
  open_at: number
  close_at: number
  draw_at: number
  settle_deadline: number
  allow_multi_win: boolean
  fee_bps: number
  /** 恒为 `refund_all`。留字段是为了让历史活动自解释，代码里不做分支。 */
  no_winner_policy: string
  min_entries_to_hold: number

  /** 种子原文（hex）。**只有揭示之后才有值**，未揭示时是空串。 */
  seed: string
  commit_hash: string
  chain_head: string

  entries: QyLotProofEntry[]
  /** `entries` 的总条数（含分页之外的）。 */
  total: number

  roster_hash: string
  roster_count: number

  /** 抽奖：中奖名单。竞猜为空数组。 */
  winners: QyLotProofWinner[]

  /** 竞猜：奖池与分配。抽奖时这四项无意义。 */
  pool_quota: number
  win_opt_no: number
  fee_quota: number
  payouts: QyLotProofPayout[]

  /**
   * **实际发生**的三个时刻（不是计划时刻）。
   *
   * 协议最核心的一条主张是"名单先于种子公开"，而 `locked_at` → `revealed_at`
   * 的间隔就是它唯一的落地证据：不下发它，这条主张只能靠验证者自己恰好在那个
   * 窗口里抓过一次快照才成立。
   */
  locked_at: number
  revealed_at: number
  settled_at: number

  // ─────────────────── 协议 lot-v2 追加的字段 ───────────────────
  //
  // 全部**可选**：`lot-v1` 的证据链一个字节都没变，历史活动必须原样验得通。
  // 但只要 `algo === 'lot-v2'`，它们就全部参与 `commit_hash` 的原像 ——
  // **少一个占位就等于允许管理员把一场普通抽奖悄悄改成双色球**，所以
  // `lib/verify.ts` 在 v2 分支里用 `?? 0` / `?? ''` 补齐，而不是跳过。

  /** 开奖模式。缺省按 `rank` 处理。 */
  draw_mode?: QyLotDrawMode

  /** 双色球期次归属。非双色球恒为空串 / 0。 */
  series_no?: string
  issue_no?: number
  /** 本期池子：平台注资 + 上期滚存 = 开局池；三者都进承诺，可跨期复核守恒式。 */
  pool_seed_quota?: number
  pool_carry_quota?: number
  pool_open_quota?: number
  /** 本期投注额进池子的万分比。 */
  pool_share_bps?: number
  /** 号池。它们进承诺，因此运营不能靠调号码空间悄悄改中奖概率。 */
  ball_red_pool?: number
  ball_red_pick?: number
  ball_blue_pool?: number
  ball_blue_pick?: number
  /** 本期开奖号，规范化格式 `03,09,12|05`。开奖前为空串。 */
  ball_result?: string

  /**
   * 平台对本份证据"证不了什么"的自陈。
   *
   * 这不是免责声明而是协议的一部分：文本奖的兑换码在承诺时**根本不存在**
   * （管理员开奖后才填），种子的真随机性密码学证不了，`user_ref` 与真人的
   * 对应关系永不公开。把这几条写进文档本身，比让用户自己去发现要诚实。
   */
  notice?: string
}

// ───────────────────────────── 工具 ─────────────────────────────

/** 活动是否还能报名。**只用于决定按钮亮不亮**，放行永远由后端说了算。 */
export function isQyLotOpen(
  activity: Pick<
    QyLotActivityDetail,
    'close_at' | 'open_at' | 'status' | 'kind'
  >,
  nowSeconds: number
): boolean {
  return (
    activity.status === 'published' &&
    nowSeconds >= activity.open_at &&
    nowSeconds < activity.close_at
  )
}

/** 结局是否属于"整场退款"那一类（取消 / 流局 / 全错 / 逾期）。 */
export function isQyLotVoided(outcome: QyLotOutcome): boolean {
  return outcome === 'cancelled' || outcome.startsWith('void_')
}
