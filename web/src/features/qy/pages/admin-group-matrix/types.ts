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
 * 用户分组 × 模型分组 矩阵的管理端 DTO。
 *
 * 与后端 `qianye/modules/groupmatrix/` 的 `api_admin.go`（路 B）与
 * `preview.go` / `orphans.go`（路 C）逐字对齐。本文件是三路之间**唯一**的
 * 契约声明处，任何一侧改字段都必须先改这里。
 *
 * ── 两个轴的命名（方案 3 的正名）──
 *
 *   · `user_group`  = `users.group`，一个人属于哪一档；
 *   · `model_group` = `channels.group` / `abilities.group` / `UsingGroup`，
 *                     一次请求路由到哪一批渠道。
 *
 * 底层是同一个字符串命名空间（方案 3 的已知代价），所以两个轴上可能出现
 * **同名但语义不同**的项。界面必须始终分列显示，DTO 也刻意用两个不同的字段名
 * 而不是复用一个 `group` —— 一旦复用，后面每一处都要靠上下文猜它是哪一边。
 *
 * ── 倍率为什么是 `number | null` 而不是 `number` ──
 *
 * `null` = 未配置（继承模型分组的兜底倍率），`0` = 显式免费，两者行为完全不同：
 * 前者回落 `GetGroupRatio(模型分组)`，后者命中 `GetGroupGroupRatio` 的整体替换
 * 分支。序列化上把它们混成一个可选 number，运营填的 0 会在 `omitempty` 里消失，
 * 一个本该免费的分组静默变成兜底价。**因此 TS 侧一律 `number | null`，
 * 禁止 `number | undefined`，禁止把两者混用。**
 */

// ───────────────────────────── 矩阵本体 ─────────────────────────────

/**
 * 已设定范围的用户分组，它的范围以什么力度生效。
 *
 *   - `shadow`  范围已设定、但只记录不阻断。灰度的中间档。
 *   - `enforce` 范围是权威的：不在范围里的模型分组，令牌选不了、请求被 403。
 *
 * 「未设定范围」不是第三个值而是 `managed:false` —— 行不存在 = **全部模型分组
 * 可用，各按自己的兜底倍率**，与「设定了范围、但范围是空的」必须能分开。
 * 后者合法且危险（隔离组：一个模型分组都不许用），前者是零行为变更的默认态。
 * 用一个枚举表达三态会让这两件事再次不可区分，而两个错误方向分别是整组用户
 * 403 与整组用户全放行，都静默。
 */
export type QyGmMode = 'enforce' | 'shadow'

/** 矩阵的一行。 */
export type QyGmUserGroup = {
  name: string
  /** 在册人数（`users.group` 命中数）。 */
  user_count: number
  /** 启用且近 30 天有访问的令牌数 —— 「撤销会打断谁」的分母。 */
  active_token_count: number
  /**
   * 是否**已设定范围**（`qy_group_scopes` 有没有这一行）。
   *
   * `false` = 未设定范围 = 全部模型分组可用、各按兜底倍率，与上游逐位一致。
   * 界面文案一律说「未设定范围 / 已设定范围」，**绝不使用「已接管 / 未接管」** ——
   * 新口径下「未接管」会被读成「不能用」，而它的真实含义恰好相反。
   */
  managed: boolean
  /**
   * 行头三态的**权威取值**，由后端下发。
   *
   *   - `unset` 未设定范围：全部模型分组可用，各按兜底倍率。
   *   - `set`   已设定范围：只可用清单里的那些。
   *   - `empty` 已设定范围但清单为空：一个模型分组都不可用（红色）。
   *
   * **必须按它渲染，不得用 `managed` + 可见列里勾了几个自己推。** 两套判据在
   * 一种真实输入上会给出相反答案：范围里只有一个模型分组、而那个分组后来被从
   * 「分组倍率」里删掉了 —— 后端仍是 `set`（清单里确实有一项，且另出一条
   * 「引用了已消失的模型分组」告警），前端却因为列轴上没有那一列而算出 0，
   * 画成红色「已设定范围·空」。运营照着红色去随便勾几个把它消掉，真正的问题
   * （悬空授权）既没被修，也不再有提示。
   */
  scope_state: 'empty' | 'set' | 'unset'
  mode: QyGmMode
  /** 是否把 `auto` 伪分组注回可选清单。 */
  allow_auto: boolean
  /** 运营备注，接管理由写在这里。 */
  note?: string
}

/** 矩阵的一列。 */
export type QyGmModelGroup = {
  name: string
  /**
   * 该模型分组的初始/兜底倍率（`options.GroupRatio`）。
   *
   * 十进制字符串：它会被展示在继承格里，也会被拿去和格子上的覆盖值比较，
   * 走一遍 float64 往返会把 `0.1` 变成 `0.10000000000000001`。
   */
  base_ratio: string
  /** 该模型分组下是否还有启用的渠道。false 时格子上出「没有渠道」的提示。 */
  has_channels: boolean
}

/** 倍率的来源。`inherit` 时 `ratio` 恒为 `null`。 */
export type QyGmRatioSource = 'inherit' | 'override'

/**
 * 一个格子的服务端真实状态。
 *
 * `granted` 与 `ratio` 是**两件独立的事**：可选性落扩展库（`qy_group_grants`），
 * 倍率落上游 `options.GroupGroupRatio`。一个格子可以「不可选但配了倍率」
 * （历史遗留）也可以「可选但没配倍率」（继承兜底）。合并成一个状态枚举会让
 * 其中一半的组合无法表达，而它们在生产上都真实存在。
 */
export type QyGmCell = {
  user_group: string
  model_group: string
  granted: boolean
  /** `null` = 未配置（继承）。`0` = 显式免费。 */
  ratio: number | null
  source: QyGmRatioSource
  /** 继承时会落到的兜底倍率（十进制字符串，= 该列的 `base_ratio`）。 */
  inherited_from: string
  /**
   * 这一格的**可达性从哪来**。
   *
   * ── 为什么不能只看 `granted` ──
   *
   * 套餐可以解锁模型分组，而且**不绑定用户分组**。于是存在这样一格：用户分组 A
   * 设了范围、范围里没有 G，(A,G) 也没有交叉倍率格 —— 但 A 组里买了那个套餐的
   * 用户**照样能用 G**，按 `GroupRatio[G]` 计费。
   *
   * 只按 `granted` 渲染的话，这一格在界面上根本不存在，运营看这张页面得到的
   * 结论是「A 组的人永远碰不到 G」。几周后他为了别的目的把 `GroupRatio[G]` 从
   * 1.0 调到 3.0，A 组所有持有该套餐的用户当场按 3 倍扣费，而站内没有任何一个
   * 页面显示过 A 组的人可以到达 G。这不是倍率数值的漂移，是**可达性认知的漂移**，
   * 后果直接是钱。
   *
   * 因此可达性必须按 `范围授予 ∪ 套餐解锁` 计算并**分状态渲染**。
   *
   * 后端尚未下发时为 `undefined` —— 此时退化成只按 `granted` 渲染，
   * 与本字段出现之前的行为逐位一致。
   */
  reachable_via?: 'both' | 'none' | 'plan' | 'scope'
  /** 让这一格可达的套餐名。`reachable_via` 含 `plan` 时才有值。 */
  plan_titles?: string[]
  /**
   * 实际生效的倍率来自哪一层：交叉格 or 该模型分组的兜底倍率。
   *
   * 与 `source` 的区别：`source` 描述**这一格配没配**，本字段描述**扣钱时会乘
   * 哪个数**。两者在 `inherit` 时结论相同，但后端将来若引入第三层回落，
   * 只有这一个字段说得出真话。后端尚未下发时为 `undefined`。
   */
  ratio_source?: 'cross_cell' | 'group_ratio'
  /**
   * 该模型分组**不在** `options.GroupRatio` 里。
   *
   * 此时上游 `GetGroupRatio` 已经 fail-open 静默返回 1，而那个 1 不是任何人配
   * 出来的。界面必须把这一格标红写「兜底缺失·按 1.0 计费」，不能显示成一个
   * 正常的 1。**判据只能是这个字段**：早先前端用「列头 `base_ratio` 是不是空串」
   * 自己推，而列轴本身就是从 `GroupRatio` 的键生成的，`base_ratio` 永不为空 ——
   * 那个红色态在结构上永远不可能出现。后端未下发时为 `undefined`。
   */
  base_missing?: boolean
  /**
   * 这一格此刻**真的会被乘进账单**的那个数（十进制字符串）。
   *
   * 由后端 `service.GetUserGroupRatio` 现算，与三条计费路径同源。
   * 与 `ratio` / `inherited_from` 的区别：那两个说"这一格配没配、继承谁"，
   * 这个说"扣钱时乘几"。后端未下发时为 `undefined`。
   */
  effective_ratio?: string
}

/** 快照健康度。`loaded:false` 时全站按上游白名单放行，必须显式说出来。 */
export type QyGmSnapshotInfo = {
  loaded: boolean
  age_seconds: number
  version: number
  /** 超过 `max_stale_seconds`。陈旧不丢弃快照，但必须让人看见。 */
  stale?: boolean
  /** 编译快照时被剔除的清单项（模型分组已从倍率表删除）。 */
  dropped_grants?: string[]
}

/**
 * 影子期的一次令牌写入拒绝。
 *
 * 这是影子模式**唯一可归因**的证据来源：读侧的挂载点拿得到 userGroup 与整张
 * 可选 map，却拿不到被查询的那个 key，因此记不出「这次查的是哪个模型分组」。
 * 不把它渲染出来，`qy_group_write_denies` 就是一张只写不读的表 —— 而只写不读
 * 的观测数据与没有观测是同一回事，只是更容易让人以为自己有证据。
 */
export type QyGmWriteDeny = {
  user_group: string
  model_group: string
  count: number
  first_seen: number
  last_seen: number
  sample_user_id: number
}

/**
 * `GET /api/qy/admin/group-matrix` 的响应，也是 PUT 成功后**强制回读**的形状。
 *
 * 保存后回读同一个结构而不是让前端乐观合并：两库不原子，部分失败时运营必须
 * 立刻在界面上看到「倍率已生效、清单未生效」，而乐观渲染画出来的是一个从未
 * 存在过的成功状态。
 */
export type QyGmMatrixResponse = {
  user_groups: QyGmUserGroup[]
  model_groups: QyGmModelGroup[]
  cells: QyGmCell[]
  /**
   * 当前 `options.GroupGroupRatio` 规范化 JSON 的哈希。
   *
   * 上游「系统设置 → 计费与支付 → 模型分组定价」页改的是同一份数据且**刻意不锁死**（扩展关掉
   * 之后运营必须还能在原地改倍率）。所以每次打开都比一次哈希，对不上就出
   * 「在别处被改过」的横幅，而不是默默把别人的改动覆盖掉。
   */
  base_ratio_hash: string
  snapshot: QyGmSnapshotInfo
  /** 保存前应当被看见、但**不拦截**的问题（大小写近似、授权了空池子…）。 */
  warnings: string[]
  /** 影子期的写入拒绝计数。见 {@link QyGmWriteDeny}。 */
  shadow_write_denies: QyGmWriteDeny[]
  /**
   * 当前口径本身，由接口下发而**不是前端写死**。
   *
   * 「未设定范围 = 全部模型分组可用」这一条刚被反转过一次；把它硬编码在前端，
   * 下一次反转就会出现「后端按新口径判定、界面按旧口径解释」的组合。
   * 后端未下发时为 `undefined`，此时界面退化成只描述可见状态、不解释口径。
   */
  scope_policy?: QyGmScopePolicy
}

/** 口径与三态计数。见 {@link QyGmMatrixResponse.scope_policy}。 */
export type QyGmScopePolicy = {
  /**
   * 恒为 `true`：**本页不限制**未设定范围的用户分组。
   *
   * ⚠ 不是「它们能用到分组倍率表里的每一个模型分组」。未设定范围时后端逐位返回
   * 上游 `GetUserUsableGroups` 的结果 —— 全局「用户可选分组」清单 + `+:` / `-:`
   * 差分 + 无条件补入用户分组自己。全局清单没有列出全部模型分组的站点上，
   * 这两句话结论不同。界面文案必须按前一句写。
   */
  unset_means_all: boolean
  unset_groups: number
  scoped_groups: number
  empty_scoped_groups: number
  /**
   * 套餐解锁此刻整体开着吗（`plan_entitlement.enabled`）。
   *
   * 关掉时「经套餐可达」的格子全部失效，而套餐照常收钱 —— 这一栏是它唯一的形状。
   */
  subscription_unlock_on: boolean
}

// ───────────────────────────── 写入 ─────────────────────────────

/**
 * 一格的显式动作。
 *
 * **刻意不是「整张矩阵 diff」。** 传一整张 map 让后端自己算差集，等于把
 * 「屏幕上没显示的格子」也一起提交了 —— 一次列筛选之后的局部保存会把没看到的
 * 格子全清掉，而且事后从请求体里根本看不出运营到底想改什么。
 *
 *   - `grant` / `revoke`      改可选性（扩展库）
 *   - `set_ratio`             写 `GroupGroupRatio[用户分组][模型分组]`，`ratio` 必填
 *   - `clear_ratio`           删那个 key，回落兜底；`ratio` 恒为 `null`
 */
export type QyGmAction = 'clear_ratio' | 'grant' | 'revoke' | 'set_ratio'

export type QyGmCellChange = {
  user_group: string
  model_group: string
  action: QyGmAction
  /** 仅 `set_ratio` 有值。其余动作恒为 `null`（不是省略，见文件头的说明）。 */
  ratio: number | null
}

/**
 * `PUT /api/qy/admin/group-matrix` 的请求体。
 *
 * 三个哈希构成闸门：`draft_hash` 证明「保存的和预览的是同一份草稿」，
 * `impact_hash` 证明「运营看到的影响面还没变」，`base_ratio_hash` 证明
 * 「这期间没人在上游页面改过倍率」。任一不符后端返回 409。
 */
export type QyGmSaveRequest = {
  cells: QyGmCellChange[]
  draft_hash?: string
  impact_hash?: string
  base_ratio_hash: string
}

/**
 * 两阶段写入的结果。**只在部分失败时出现**，成功时后端不带这个字段。
 *
 * 倍率落上游 `options`、清单落扩展库，两库不原子，且顺序是**先倍率后清单**：
 * 倍率成功 / 清单失败 → 新的可达组合还没打开，没有任何流量按新倍率走，
 * 零金额影响；反过来 → 用户立刻能选中该模型分组，而倍率还是旧的或回落兜底，
 * 钱当场按错误的价走。
 *
 * 这个字段存在的唯一理由是：半成状态必须**在界面上被看见**。运营按完保存
 * 看到一个绿色的「已保存」然后走人，是这套两库设计最坏的失败方式。
 */
export type QyGmSavePartial = {
  ratio_applied: boolean
  membership_applied: boolean
  /** 后端下发的原文，前端不二次转译。 */
  message: string
}

/** PUT 的响应 = 强制回读的真实状态（+ 部分失败时的说明）。 */
export type QyGmSaveResponse = QyGmMatrixResponse & {
  partial?: QyGmSavePartial
}

/** `PUT /api/qy/admin/group-matrix/scope/:user_group` 的请求体。 */
export type QyGmScopeRequest = {
  managed: boolean
  mode: QyGmMode
  allow_auto: boolean
  note: string
  /**
   * 切到 `enforce` 时必填 —— 与 PUT 矩阵同一道闸门。
   *
   * 设计文档把「切 enforce 需要双哈希」写在矩阵 PUT 那一节，而 mode 实际是
   * 由本端点改的；这里按**行为**归位：真正开始阻断流量的那一次写入必须带闸门，
   * 无论它走哪个 URL。切回 `shadow` / 取消接管不需要（都不会打断流量）。
   */
  draft_hash?: string
  impact_hash?: string
}

// ───────────────────────────── 影响面 ─────────────────────────────

/** 一条令牌样本。只给管理员，且每对上限 `preview_sample_limit`。 */
export type QyGmTokenSample = {
  token_id: number
  token_name: string
  /** 掩码后的 key，够运营去后台定位即可。 */
  key_masked: string
  user_id: number
  username: string
  group: string
  /** 上游 token 状态码，1 = 启用。 */
  status: number
  /** unix 秒，0 = 从未使用。 */
  accessed_time: number
}

/** 一个 (用户分组, 模型分组) 组合上的影响面。 */
export type QyGmImpactPair = {
  user_group: string
  model_group: string
  user_count: number
  token_count: number
  token_enabled_count: number
  /** 近 30 天 `accessed_time` 有更新的令牌数 —— 「真的在用」的静态近似。 */
  token_active_30d: number
  /**
   * 近 `log_days` 天的真实请求数（L2 日志聚合）。
   *
   * `undefined` = 日志库没查（超时/超限），**不是 0**。把「没查」画成 0
   * 会让运营以为这条边没人在用而直接切 enforce。
   */
  traffic_requests?: number
  /** 同一窗口里的去重用户数。语义同上：`undefined` = 没查，不是 0。 */
  traffic_users?: number
  samples: QyGmTokenSample[]
  samples_truncated: boolean
}

/** 放开一条边同样是金额事件：它会让用户走到哪个倍率上。 */
export type QyGmAllowPair = {
  user_group: string
  model_group: string
  user_count: number
  /** 该组合当前有没有 `GroupGroupRatio` 覆盖。 */
  has_override: boolean
  /** 有覆盖时是这个值，没有时是兜底值。十进制字符串。 */
  effective_ratio: string
  source: QyGmRatioSource
  /** 该模型分组下还有没有启用的渠道。false = 放开了一个空池子。 */
  has_channels: boolean
}

/** 大小写近似项。**不折叠、只告警**，理由见 `lib/draft.ts` 的说明。 */
export type QyGmCaseNearMiss = {
  left: string
  right: string
  /** 两者分别出现在哪里，例如 `users.group` / `grants`。 */
  left_source: string
  right_source: string
}

/** 不在 `options.GroupRatio` 里的分组名 —— 这批正按 1.0 兜底扣费。 */
export type QyGmOrphanName = {
  group: string
  /** `grants` / `users.group` / `tokens.group`。 */
  source: string
  count: number
}

/** `POST /api/qy/admin/group-matrix/preview` 的响应。纯只读。 */
export type QyGmPreviewResponse = {
  draft_hash: string
  impact_hash: string
  base_ratio_hash: string
  /**
   * 超时 / 超过 `max_preview_pairs`。**为 true 时禁止切 enforce**：
   * 宁可切不了，也不能在看不见影响面的情况下切。
   */
  preview_incomplete: boolean
  /**
   * `logs.group` 是**当时的**使用分组，`users.group` 是**当前**的用户分组，
   * 期间改过分组的会归错组。为 true 时界面必须说出来，不许假装精确。
   */
  approximate_user_group: boolean
  /** L2 聚合实际用了几天。 */
  log_days: number

  /** A：本次改动**新造出来**的断链。运营点保存前唯一必须看懂的数字。 */
  newly_broken: QyGmImpactPair[]
  /** B：本来就已经断的（本站 545 条在这里）。**单独合计，不与 A 合并。** */
  already_broken: QyGmImpactPair[]
  /** C：新放开的边。 */
  newly_allowed: QyGmAllowPair[]
  /** E：权威清单里不含用户分组自己。**警告而不是拦截** —— 项目方要它能被推翻。 */
  self_excluded: string[]
  /** F：大小写近似项。 */
  case_near_miss: QyGmCaseNearMiss[]
  /** G：不在分组倍率表里的分组名。 */
  orphan_group_names: QyGmOrphanName[]

  /**
   * `tokens.group = ''` 的令牌数。
   *
   * 它们**结构性免疫**：上游 `middleware/auth.go` 的 `if tokenGroup != ""`
   * 让整段可选性检查被跳过，直接拿 `users.group` 当使用分组。这一栏必须单独
   * 显示并说明，否则运营看到站里 200 个空分组令牌会以为要炸。
   */
  empty_group_tokens: number
  /**
   * `auto_groups` 里含已失效分组的令牌数。
   *
   * 这条失败形状**没有任何其它信号**：候选列表被静默缩短，请求照常成功，
   * 只是永远轮不到那个分组。所以哪怕本站当前是 0 也必须留这一栏。
   */
  auto_groups_shrink: number

  /** 块 A / 块 B 的令牌合计。**分开下发，界面也绝不合并。** */
  total_newly_broken_tokens: number
  total_already_broken_tokens: number
}

// ───────────────────────────── L0 基线 ─────────────────────────────

/** 按分组聚合的一行孤儿统计。 */
export type QyGmOrphanRow = {
  /** 展示名。令牌类是 `用户分组 → 模型分组`，用户类就是分组名本身。 */
  group: string
  /** 可机读的原始字段，CSV 与后续排查用。 */
  user_group: string
  model_group?: string
  count: number
  enabled_count: number
  active_30d: number
  /**
   * 这批令牌会撞上的**上游原话**（后端下发中文原文，前端不二次转译）。
   * 例如「无权访问 %s 分组」/「分组 %s 已被弃用」。
   */
  reason: string
  samples: QyGmTokenSample[]
}

/**
 * `GET /api/qy/admin/group-matrix/orphans` —— 常驻基线，与本次草稿无关。
 *
 * 它必须在运营按保存**之前**就摆在那里并标明「与本次改动无关」：没有基线的
 * 影响面报告不可读，第一次预览弹出 545 这个数字会让人直接放弃这个功能。
 */
export type QyGmOrphansResponse = {
  token_total: number
  user_total: number
  /** 令牌分组非空、且不在其属主用户分组的可选清单里。 */
  orphan_tokens: QyGmOrphanRow[]
  /** 令牌分组不在 `GroupRatio` 里（对应上游「分组 %s 已被弃用」）。 */
  deprecated_tokens: QyGmOrphanRow[]
  /** `auto_groups` 含已失效分组。 */
  auto_group_tokens: QyGmOrphanRow[]
  /** 用户分组不在 `GroupRatio` 里 —— 这批人的请求正按 1.0 倍扣费。 */
  orphan_users: QyGmOrphanRow[]
  /** 结构性免疫的空分组令牌数，见 `QyGmPreviewResponse.empty_group_tokens`。 */
  empty_group_tokens: number
  incomplete: boolean
  /**
   * 这份基线的**时点**与**口径**。
   *
   * 判据走 `service.GetUserUsableGroups`，而那个函数已被本功能接管：
   * `enforced_groups` 非空时，被本功能打断的令牌**也会出现在这份报告里**。
   * 「与本次改动无关」这句话只在 `enforced_groups` 为空时严格成立，
   * 事故复盘必须以这两个字段为准。
   */
  measured_at: number
  enforced_groups: string[]
}

/** `POST /api/qy/admin/group-matrix/repair-token` 的请求体。 */
export type QyGmRepairTokenRequest = {
  token_id: number
}
