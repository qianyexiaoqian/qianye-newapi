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
 * AI 审核的前端类型。
 *
 * ── 密钥在这里只有两种形态 ──
 * 读:`has_key`(有没有)与 `key_hint`(掩码,尾 4 位)。**没有任何一个字段
 * 承载明文密钥** —— 后端的列表视图是白名单结构体,连密文都不下发。
 * 写:`api_key` 是可选字段,`undefined` 表示"这次不动密钥",空串表示"清除"。
 * 两者必须能分开,否则"改一下模型名"就会把密钥抹掉,而抹掉之后不可恢复。
 */

/** 一次审核调用的结局。除 clean / violation 外全部是失败,而失败一律放行。 */
export type QyAiOutcome =
  | 'clean'
  | 'violation'
  | 'timeout'
  | 'bad_json'
  | 'upstream_error'
  | 'no_channel'

export type QyAiChannel = {
  id: number
  name: string
  base_url: string
  model: string
  has_key: boolean
  key_hint: string
  timeout_ms: number
  weight: number
  enabled: boolean
  price_in_per_m: string
  price_out_per_m: string
  remark: string
  updated_at: number
}

export type QyAiChannelInput = {
  name: string
  base_url: string
  model: string
  /** 省略 = 保持原密钥;空串 = 清除。绝不能把它做成必填。 */
  api_key?: string
  timeout_ms: number
  weight: number
  enabled: boolean
  price_in_per_m: string
  price_out_per_m: string
  remark: string
}

export type QyAiChannelList = {
  items: QyAiChannel[]
  /** 后端有没有配 violation.ai_review_key。没配就存不下密钥。 */
  key_configured: boolean
}

export type QyAiSetting = {
  id: number
  enabled: boolean
  /** 万分比:3000 = 30%。 */
  sample_rate_bps: number
  pre_timeout_ms: number
  async_timeout_ms: number
  /**
   * **空串 = 用内置默认提示词,并跟随它的后续升级**;非空 = 本站自定义的一份。
   * 界面上输入框会被预填成默认全文,所以"输入框非空"绝不等于"已自定义" ——
   * 那一档要看 `prompt_source`。
   */
  prompt: string
  max_input_chars: number
  third_party_notice_ack: boolean
}

/** 提示词属于哪一档。`default` 才会跟随内置默认提示词的后续升级。 */
export type QyAiPromptSource = 'default' | 'custom'

/** 渲染后的提示词与违规类型表的对账结果。 */
export type QyAiPromptCategoryReport = {
  /**
   * 提示词枚举了、违规类型表里没有的类型名 —— 模型按它回会被折进「未分类」。
   * 最常见的来源是上一版留下来的自定义提示词里那一行手抄清单。
   */
  unknown: string[]
  /**
   * 类型表里有、渲染后的提示词却没提到的类型名。
   * 清单是自动拼进去的,所以它非空只意味着渲染这一步坏了。
   */
  missing: string[]
}

/** 类型清单里的一项(管理端视图,正面清单:没有内部备注、没有公示文案)。 */
export type QyAiCategoryDetail = {
  key: string
  name: string
  /** 这一类有没有填「给 AI 的判定说明」。没填时模型只拿到一个英文 key。 */
  has_guidance: boolean
  guidance_runes: number
  /** 兜底「未分类」:模型判了违规却归不了类时用它。 */
  is_fallback: boolean
}

export type QyAiSettingResponse = {
  setting: QyAiSetting
  default_prompt: string
  /** 别用 `setting.prompt !== ''` 自己算:输入框预填之后那个判断永远为真。 */
  prompt_source: QyAiPromptSource
  prompt_categories: QyAiPromptCategoryReport
  /** 违规类型表里参与 AI 审核的 key,**由后端从类型表现算**,不是写死的闭集。 */
  categories: string[]
  /** 自动生成的那一段类型清单。前端用它在本地渲染预览与做同一套对账。 */
  category_block: string
  /** 库里那一份提示词拼上清单之后、**真正发出去**的全文。 */
  prompt_preview: string
  category_details: QyAiCategoryDetail[]
  key_configured: boolean
  effective: {
    /** 快照里**真正生效**的那一份,不是表单回显。两者不同时界面必须说出来。 */
    active: boolean
    channels: number
    pre_rules: boolean
    post_async_rules: boolean
    pre_timeout_hint: string
    max_pre_timeout: number
    max_async_timeout: number
  }
}

/**
 * 保存设置的回显。与 GET 同形,因为提示词把类型闭集改坏时接口仍然返回 200
 * (收窄类型是合法用法,不该被拒),"哪里坏了"必须随这一次响应一起回来。
 */
export type QyAiSettingSaveResult = {
  setting: QyAiSetting
  prompt_source: QyAiPromptSource
  prompt_categories: QyAiPromptCategoryReport
  prompt_preview: string
}

/**
 * 一条 AI 审核作用域策略。
 *
 * `model_scope` / `group_scope` / `group_scope_mode` 三格与**违规规则**上的
 * 同名列语法完全一致(后端共用同一段 compileScope):逗号或换行分隔,
 * 模型支持 `gpt-4*` / `*-vision` 前后缀通配,分组名大小写不敏感。
 *
 * 两个抽样率分开是因为两个时机的代价差一个数量级:转发前是同步的,直接加在
 * 被抽中请求的首字节延迟上;转发后是异步的,只花钱、不花用户的时间。
 * 两个都填 0 是**免审名单**的字面写法,不是"没配"。
 */
export type QyAiScope = {
  id: number
  name: string
  enabled: boolean
  /** 升序,越小越先匹配。第一条匹配的策略说了算,不叠加。 */
  priority: number
  model_scope: string
  group_scope: string
  group_scope_mode: 'include' | 'exclude'
  /** 万分比:5000 = 50%。 */
  pre_sample_rate_bps: number
  async_sample_rate_bps: number
  /**
   * **这一档自己的**审核提示词。空 = 用设置卡片上那一份全局提示词
   * (全局也空则用内置默认)。
   *
   * 它只覆盖「判定说明」那一段:违规类型清单永远由后端从违规类型表现算并拼进去,
   * **不要在这里手抄一份清单** —— 抄了之后运营在类型页新建一个类型,
   * 这一档会静默地永远返回旧类型。要指定清单出现的位置,用
   * {@link QY_AI_CATEGORY_PLACEHOLDER} 占位符。
   */
  prompt: string
  /**
   * 这一档的命中**一律**记为哪个违规类型。0 = 不指定(按规则自己绑的类型记)。
   *
   * 优先级:它覆盖规则绑定的类型;而模型返回的 category 永不直接决定记录类型
   * (它继续只做规则的类型白名单判据,原值留在审核明细上)。
   */
  category_id: number
  remark: string
  created_at: number
  updated_at: number
}

/**
 * 这一档的提示词属于哪一档。
 *
 * 与全局那一格的 `QyAiPromptSource` **不是同一个枚举**:那边的 `default` 指
 * 「用内置默认并跟随它升级」,这边的 `inherit` 指「用全局那一份」,
 * 而全局那一份完全可能是本站自定义的。混用会让界面把「继承」显示成「默认」。
 */
export type QyAiScopePromptSource = 'inherit' | 'custom'

/** 新建/编辑入参。`id` 为 0 或省略表示新建。 */
export type QyAiScopeInput = Omit<
  QyAiScope,
  'id' | 'created_at' | 'updated_at'
> & { id?: number }

/**
 * 汇总表的一行。它不是策略行的回显 —— `fallback` 与 `shadowed` 两列在库里
 * 都不存在,它们是这份配置**作为整体**的性质,而"现在哪些分组在被监控"
 * 问的正是这个整体。
 */
export type QyAiScopeSummaryRow = {
  /** 0 = 兜底档(它没有对应的策略行,取值来自设置页的兜底抽样率)。 */
  id: number
  name: string
  fallback: boolean
  enabled: boolean
  priority: number
  model_scope: string
  group_scope: string
  group_scope_mode: 'include' | 'exclude'
  pre_sample_rate_bps: number
  async_sample_rate_bps: number
  /**
   * 这一档用的是继承来的提示词还是自己写的一份。兜底档恒为 `inherit`。
   *
   * 摆在汇总表上而不是只在编辑表单里:一份写坏的作用域提示词与一份正常的
   * 在列表上长得完全一样,而它的后果是这一档的判定口径整体偏掉。
   */
  prompt_source: QyAiScopePromptSource
  /** 这一档指定的「命中一律记为」类型 id,0 = 不指定。类型名去违规类型清单里 join。 */
  category_id: number
  /**
   * 这一行永远匹配不到:它前面有一条作用域为空(= 匹配一切)的启用策略。
   * 一条被遮住的策略与一条配错作用域的策略在列表上长得一模一样,
   * 而两者的下一步完全不同(调优先级 vs 改作用域)。
   */
  shadowed: boolean
}

export type QyAiScopeList = {
  items: QyAiScope[]
  /** 按匹配顺序排好、末尾恒为兜底档。顺序就是后端热路径的判定顺序。 */
  summary: QyAiScopeSummaryRow[]
  fallback_bps: number
  max_scopes: number
  ai_enabled: boolean
  /** 快照里真正生效的那一份。与 items 不同说明还没重载到。 */
  effective_active: boolean
  active_scopes: {
    id: number
    name: string
    pre_sample_rate_bps: number
    async_sample_rate_bps: number
    /** 提示词原文不下发,只给档位 —— 它已经在表单里了。 */
    prompt_source: QyAiScopePromptSource
    category_id: number
  }[]
}

export type QyAiStatsRow = {
  outcome: QyAiOutcome
  count: number
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  cost_usd: string
}

export type QyAiStats = {
  days: number
  by_outcome: QyAiStatsRow[]
  total_calls: number
  total_tokens: number
  total_cost_usd: string
  violated_calls: number
  /** 发出去了但算不出钱的调用数(渠道没填单价)。> 0 时总额是被低估的。 */
  unpriced_calls: number
}

export type QyAiReviewLog = {
  id: number
  review_no: string
  user_id: number
  username: string
  phase: string
  channel_name: string
  review_model: string
  outcome: QyAiOutcome
  violated: boolean
  category: string
  confidence: string
  reason: string
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  cost_usd: string
  latency_ms: number
  rule_id: number
  record_id: number
  request_id: string
  model_name: string
  using_group: string
  created_at: number
}

export type QyAiChannelTestResult = {
  outcome: QyAiOutcome
  violated: boolean
  category: string
  confidence: string
  latency_ms: number
  tokens: { prompt: number; completion: number; total: number }
  cost_usd: string
  priced: boolean
  message?: string
}
