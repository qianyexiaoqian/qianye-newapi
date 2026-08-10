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
 * 与 `qianye/modules/violation/model.go` + `api_admin.go` 对齐。
 *
 * 金额与倍数一律是 **string**：后端用 `shopspring/decimal`，默认序列化成带引号的
 * 字符串；同时入参也刻意收 string —— JSON number 在前端是 float64，`0.1`
 * 往返一次会变成 `0.10000000000000001`，而这个值会被直接乘进用户的账单。
 * 前端全程按字符串透传，绝不 `parseFloat` 后再回写。
 */

/** 规则生效阶段。 */
export type QyViolationPhase = 'prompt' | 'reject_reason' | 'upstream_err'

/**
 * 规则的执行模式 —— **「影子 / 真实」唯一的开关**。
 *
 * 曾经是两层:全局开关(YAML + qy_settings + 一条已删除的 PUT 路由)与一个规则级的
 * 布尔开关，叠加时取更保守者胜。项目方拍板删掉全局层，因为他要的用例只有一个：
 * 把某一条规则设成影子、只记录不处罚、拿它抓到的日志做分析 —— 而两层结构让这个
 * 用例必须先确认全局在哪一档，全局默认为影子时规则级怎么调都不生效。
 *
 * 后端对**未知取值一律按影子处理**(判据是 `mode === 'enforce'`)，所以前端
 * 读到空串或陌生值时也必须显示成影子，不能显示成真实。
 */
export type QyViolationMode = 'enforce' | 'shadow'

/** 规则来源。空串按 `manual` 读 —— 那是这一列出现之前的唯一语义。 */
export type QyViolationSource = '' | 'builtin' | 'manual'

/**
 * 匹配方式。
 *
 * `error_code` / `status_code` / `upstream_text` 只能用于上游阶段（prompt 阶段
 * 拿不到上游错误）；`request_rate` 恰好相反，只能用于转发前 —— 它数的是
 * 「即将发往上游的非流式请求条数」，挂在上游阶段就只会数到失败的那些。
 */
export type QyViolationMatchType =
  | 'error_code'
  | 'keyword'
  | 'regex'
  | 'request_rate'
  | 'status_code'
  | 'upstream_text'

/**
 * 分组作用域的名单方向。
 *
 * `include`（默认）：名单非空时只对名单内的分组生效。
 * `exclude`：名单非空时对名单内的分组**豁免**，其余分组全部生效。
 *
 * 名单为空时后端强制折回 `include` —— 「空黑名单」与「空白名单」都表示
 * 「全部分组生效」，留两个等价状态只会让界面上出现一个什么都不改变的开关。
 */
export type QyViolationGroupScopeMode = 'exclude' | 'include'

/** 处置动作。含 block 的动作只在 prompt 阶段有意义。 */
export type QyViolationAction =
  | 'block_and_charge'
  | 'block'
  | 'charge'
  | 'record'

export type QyViolationFeeMode = 'fixed' | 'model_price_multiple' | 'none'

export type QyViolationRule = {
  id: number
  name: string
  remark: string
  /** 写给用户看的对外文案。与内部 `name` 分开，后者常含规则代号。 */
  public_reason: string
  enabled: boolean
  /** 影子 / 真实。这是决定「要不要真的扣钱、封号」的唯一开关。 */
  mode: QyViolationMode
  /** `builtin` 表示由内置防护规则包导入；空串或 `manual` 表示运营自己写的。 */
  source: QyViolationSource
  /** 内置规则的稳定标识（如 `jailbreak.dan_persona`），手写规则为空串。 */
  builtin_key: string
  /** 导入（或上次升级）时内置目录里那条规则的版本号。 */
  builtin_version: number
  /**
   * 导入时**后端发出去的** pattern 的 sha256。
   *
   * 升级判据的另一半：它与规则当前 pattern 的指纹不等就表示运营改过，
   * 而改过的规则**任何情况下都不会被升级覆盖**。
   */
  builtin_fingerprint: string
  priority: number
  phase: QyViolationPhase
  match_type: QyViolationMatchType
  pattern: string
  case_sensitive: boolean
  /**
   * 上游 HTTP 状态码前置条件。空 = 不限；否则逗号分隔的状态码或区间
   * （`400` / `400,403` / `400-499`），语法与 `status_code` 匹配方式的 pattern 相同。
   *
   * 它**不是**第七种匹配方式，是一道与全部匹配方式正交的作用域闸 ——
   * 「status_code + 正文」因此可以写成**一条**规则。拆成两条的话，
   * 一次上游拒绝会被两条规则各命中一次、各计一次数、各扣一次费。
   *
   * 只对上游阶段有意义：prompt 阶段还没有上游响应，后端会拒绝保存。
   */
  status_scope: string
  model_scope: string
  group_scope: string
  group_scope_mode: QyViolationGroupScopeMode
  action: QyViolationAction
  fee_mode: QyViolationFeeMode
  fee_fixed: string
  fee_multiple: string
  fee_max_quota: number
  count_weight: number
  severity: number
  archive_context: boolean
  block_message: string
  created_at: number
  updated_at: number
  created_by: number
  updated_by: number
}

/** 新建 / 编辑的请求体（`ruleUpsertReq`）。 */
export type QyViolationRuleInput = {
  name: string
  remark: string
  public_reason: string
  enabled: boolean
  mode: QyViolationMode
  priority: number
  phase: QyViolationPhase
  match_type: QyViolationMatchType
  pattern: string
  case_sensitive: boolean
  /** 上游状态码作用域。见 `QyViolationRule.status_scope`。 */
  status_scope: string
  model_scope: string
  group_scope: string
  group_scope_mode: QyViolationGroupScopeMode
  action: QyViolationAction
  fee_mode: QyViolationFeeMode
  fee_fixed: string
  fee_multiple: string
  fee_max_quota: number
  count_weight: number
  severity: number
  archive_context: boolean
  block_message: string
}

/** 规则试跑结果。`scope_ok=false` 表示作用域没覆盖到试跑用的模型/分组。 */
export type QyViolationRuleTestResult = {
  scope_ok: boolean
  matched: boolean
  terms: string[]
  snippet: string
  elapsed_us?: number
}

/**
 * 熔断状态。
 *
 * **这里没有「全局模式」了。** 曾经的 `shadow` / `shadow_reason` /
 * `config_shadow` / `shadow_override` / `global_shadow` 五个字段随全局层一起
 * 删除 —— 它们描述的是一个已经不存在的开关，留着只会让界面继续渲染一个
 * 改不动任何东西的控件。
 *
 * 剩下的 `forced_shadow` 是熔断:拦截率或封号速率异常时**自动**把全部规则临时
 * 按影子执行。它不是模式，是机器踩的刹车 —— 没有人打开过它，人只需要在它响的
 * 时候收到告警，所以界面只在 `forced_shadow === true` 时才渲染那块告警。
 */
export type QyViolationBreaker = {
  forced_shadow: boolean
  /** 熔断的触发描述，由后端拼好（如 `block_rate 43/200 超过 500 bps`）。 */
  forced_shadow_reason: string
  forced_shadow_until: number
  forced_shadow_count: number
  window_scanned: number
  window_blocked: number
  block_rate_limit_bps: number
  ban_window_count: number
  ban_rate_limit_hour: number
  scan_total: number
  block_total: number
  shadow_hits: number
  record_drops: number
  scan_timeouts: number
  rule_refresh_fails: number
  /**
   * `request_rate` 判据的计数降级可见性。
   *
   * `rate_local_hits > 0` 表示计数正落在**每节点各数各的**进程内兜底上
   * （站点没配 Redis，或 Redis 正在报错）。多节点部署时单节点看到的条数只有真实值
   * 的约 1/N，阈值等于被放大了 N 倍。不把它摆出来，运营会照着被稀释的数字一路
   * 调低阈值，等 Redis 恢复、真实计数回来时一次性误伤一大批人。
   */
  rate_redis_fails: number
  rate_local_hits: number
  rate_local_full: number
}

export type QyViolationStatBucket = {
  key: string
  cnt: number
  fee_quota: number
}

export type QyViolationStats = {
  hours: number
  record_count: number
  blocked: number
  /** 影子模式下的命中量：切真实模式前唯一的决策依据。 */
  shadow_count: number
  fee_quota: number
  clamp_count: number
  ban_count: number
  by_rule: QyViolationStatBucket[]
  by_model: QyViolationStatBucket[]
  breaker: QyViolationBreaker
  rules: {
    version: number
    loaded_at: number
    prompt_rule: number
    post_rule: number
    /**
     * 已启用规则按模式的分布。
     *
     * 删掉全局开关之后，「现在到底有没有规则在真实扣钱」不再是一个布尔值。
     * 这两个数由后端从规则快照直接算出 —— 前端自己按分页拉规则再数一遍
     * 既是同一事实的第二份拷贝，也数不全（列表是分页的）。
     */
    shadow_rule: number
    enforce_rule: number
  }
  policy: {
    insufficient_balance: string
    auto_ban_threshold: number
    auto_ban_window_h: number
    max_fee_quota: number
  }
}

/**
 * 用户维度的滚动窗口违规计数。
 *
 * `hit_count` 是自动封号判据的唯一输入。本轮之前影子命中也会推进它，
 * 所以现网的这一列里混着影子命中，而历史行无法分辨 —— 重置动作因此存在。
 */
export type QyViolationCounter = {
  user_id: number
  window_start: number
  hit_count: number
  total_count: number
  ban_cycle: number
  last_hit_at: number
  updated_at: number
}

export type QyViolationCounterPage = {
  items: QyViolationCounter[]
  total: number
  /** 自动封号阈值。由后端下发，前端不再抄一份。 */
  threshold: number
  window_hours: number
}

/* ─────────────────────────── 内置防护规则包 ─────────────────────────── */

/** 四大类，与后端 `builtin.go` 的 Cat* 常量一一对应。 */
export type QyViolationBuiltinCategoryId =
  | 'distill'
  | 'jailbreak'
  | 'pressure'
  | 'reverse'
  | 'upstream'

export type QyViolationBuiltinCategory = {
  id: QyViolationBuiltinCategoryId
  name_zh: string
  name_en: string
  desc: string
}

/**
 * 一条内置规则在**这个站点**上的状态。
 *
 * `modified` 是最要紧的一档：它表示运营改过这条规则的 pattern，因此升级
 * **任何情况下都不会覆盖它**（哪怕版本很旧）。界面必须把这一档单独标出来，
 * 否则「为什么点了升级它没变」就成了一个只能读源码才能回答的问题。
 */
export type QyViolationBuiltinState =
  | 'modified'
  | 'not_imported'
  | 'up_to_date'
  | 'upgradable'

export type QyViolationBuiltinItem = {
  key: string
  category: QyViolationBuiltinCategoryId
  version: number
  name: string
  public_reason: string
  /** 这条防什么。 */
  guards: string
  /** 典型误杀场景。缺了它，运营在收到第一个工单时无从判断该改窄还是该停用。 */
  false_positive: string
  /** 模式串的出处，便于核对。 */
  origin: string
  /** 建议阈值 / 建议用法。 */
  advice: string
  phase: QyViolationPhase
  match_type: QyViolationMatchType
  pattern: string
  case_sensitive: boolean
  /** 上游拒绝类条目自带的状态码作用域；其余类别为空串。 */
  status_scope: string
  priority: number
  count_weight: number
  severity: number

  state: QyViolationBuiltinState
  /** 未导入时为 0。 */
  rule_id: number
  imported_version: number
  rule_enabled: boolean
  /** 已导入规则**当前**的模式；未导入时为空串。 */
  rule_mode: '' | QyViolationMode
}

export type QyViolationBuiltinCatalog = {
  categories: QyViolationBuiltinCategory[]
  items: QyViolationBuiltinItem[]
  /** 导入时统一落的模式。由后端下发而不是前端写死一句文案。 */
  import_mode: QyViolationMode
}

export type QyViolationImportOutcome = {
  key: string
  action: 'created' | 'skipped' | 'upgraded'
  reason?: string
  rule_id?: number
}

export type QyViolationImportResult = {
  results: QyViolationImportOutcome[]
  changed: number
  mode: QyViolationMode
}

/* ─────────────────────────── 影子命中分析 ─────────────────────────── */

/**
 * 影子原因。与后端 `guard.go` 的 ShadowReason* 常量对齐。
 *
 * 必须分开统计：`rule_mode` 是预期内的观察样本，`breaker` 是事故现场，
 * 混在一起算出来的误判率没有意义。
 */
export type QyViolationShadowReason =
  | ''
  | 'breaker'
  | 'dup_builtin_fee'
  | 'rule_mode'

/**
 * 命中记录（管理端列表口径，与后端 `Record` 对齐的子集）。
 *
 * 这一页只用它做「按规则看影子命中」的分析，不做处置 —— 撤销 / 退款 / 申诉
 * 都在违规记录页。
 */
export type QyViolationRecord = {
  id: number
  rec_no: string
  request_id: string
  user_id: number
  username: string
  token_id: number
  token_name: string
  ip: string
  rule_id: number
  rule_name: string
  phase: QyViolationPhase
  action: QyViolationAction
  shadow: boolean
  shadow_reason: QyViolationShadowReason
  blocked: boolean
  model_name: string
  using_group: string
  matched_terms: string
  match_snippet: string
  /** 若真实执行会扣多少（quota）。影子模式的全部价值就在这一列。 */
  fee_quota_want: number
  fee_quota: number
  fee_status: string
  count_weight: number
  counted: boolean
  has_payload: boolean
  created_at: number
}

/* ─────────────────────────── 多选批量操作 ─────────────────────────── */

/**
 * 单条规则在一次批量里的结局。
 *
 * **三档而不是成功/失败两分**。`skipped` = 库里本来就是目标状态，一个字节都不用动，
 * 它**不是失败**：把它算进失败里，一次「全选 → 批量启用」会报「18 条启用失败」，
 * 管理员会去排查一个根本不存在的故障（上游渠道批量接口正是栽在这里）。
 */
export type QyViolationBatchOutcome = 'failed' | 'ok' | 'skipped'

export type QyViolationBatchItem = {
  id: number
  /** 规则名。失败列表里只有 id 的话，管理员得回列表页一个个对照才知道是哪条。 */
  name: string
  outcome: QyViolationBatchOutcome
  /** 稳定标识，前端据此映射 i18n 文案。 */
  code?: string
  /** 后端中文兜底，只在 `code` 未被前端登记时显示，不保证可翻译。 */
  detail?: string
}

/**
 * 批次结果。
 *
 * `total === succeeded + skipped + failed` 是后端保证的恒等式，也是前端唯一能信的
 * 东西：少了它，「选 20 条、成功 18 条」就无法回答剩下 2 条是失败了还是本来就不用动。
 *
 * 整批**一律 200**，即使一条都没成功 —— 逐条明细才是这个接口的产品，而 qy 的
 * `unwrap` 在 `success:false` 时直接抛错并丢掉 data。判据是响应体里的
 * `succeeded` / `failed`，不是 HTTP 状态码。
 */
export type QyViolationBatchResult = {
  total: number
  succeeded: number
  skipped: number
  failed: number
  items: QyViolationBatchItem[]
}

/**
 * 批量作用分组的三种写法。
 *
 *   `replace` 覆盖：整串换成填的这几个，方向一起换。填空 = 对全部分组生效。
 *   `append`  追加：并进现有名单末尾，已有的不重复。
 *   `remove`  移除：从现有名单里摘掉。
 *
 * 界面上**必须**说清楚当前是哪一种。让人猜「批量设置分组」到底是覆盖还是追加，
 * 一次误判就是一批规则的作用域被整串抹掉，而列表上那几条看起来一个字都没改。
 */
export type QyViolationBatchScopeOp = 'append' | 'remove' | 'replace'
