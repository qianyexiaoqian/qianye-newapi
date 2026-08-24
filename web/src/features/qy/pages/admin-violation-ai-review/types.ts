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

/**
 * 一个审核渠道说哪一种「审核方言」。
 *
 * - `json_prompt` —— 通用大模型 + 提示词工程:发一段几百 token 的系统提示词
 *   (判定口径 + 本站违规类型闭集),要求模型吐一个 JSON。**这是零值档,也是
 *   这一列出现之前的唯一行为**,后端把空串与任何不认识的取值都折到它上面。
 * - `qwen3guard` —— 护栏模型:阿里 Qwen3Guard 一类**专门为安全分类微调**的
 *   小模型。不发提示词,它直接吐 `Safety: Unsafe` / `Categories: ...` 标签。
 *
 * 两条路并列,不互相取代 —— 代价、延迟、准确性、类型体系都不同,界面上必须
 * 把差别说出来,不能只给一个下拉框。
 */
export type QyAiProtocol = 'json_prompt' | 'qwen3guard'

/**
 * Qwen3Guard 比常见护栏模型多一档 `Controversial`(有争议)。这一格决定把它
 * 怎么接到本站的处置上。
 *
 * - `safe`(**空串 = 零值档**)—— 不当违规。新增能力不得替站点收紧处置。
 * - `sensitive` —— **参考实现(Wei-Shaw/sub2api)的策略**:只有命中"敏感
 *   类别"时才升级成违规。它把三类钉死在代码里(jailbreak / pii /
 *   suicide_and_self_harm),本站把那份清单做成了可改的一格。
 * - `unsafe` —— 一律当违规。
 *
 * 选了收紧档之后还有第二道旋钮 —— 规则上的 `ai_min_confidence`:
 * Unsafe 与"升级后的 Controversial"记 0.95,普通 Controversial 记 0.6,
 * 阈值填 0.8 就只吃前者。
 *
 * `json_prompt` 渠道上后端会把它清空:留着一个被忽略的取值,下一个人会照着
 * 界面回显去查「为什么设了 unsafe 却没生效」。
 */
export type QyAiGuardControversial = '' | 'safe' | 'sensitive' | 'unsafe'

export type QyAiChannel = {
  id: number
  name: string
  base_url: string
  model: string
  /** 后端下发的恒是归一后的取值,**不会是空串**。 */
  protocol: QyAiProtocol
  /** `json_prompt` 渠道上恒为空串,界面据此决定要不要画那一格。 */
  guard_controversial: QyAiGuardControversial
  /**
   * 启用的类别子集(九类的 snake_case id)。
   *
   * **空数组 = 九类全启用**,不是"一个都不启用" —— 这是零值档,也是这一格
   * 存在之前的唯一行为。停用一个类别不等于丢弃它:后端在
   * 「Unsafe 且解析出的类别全被停用」时仍然判违规,只把置信度降到 0.6。
   */
  guard_categories: string[]
  /**
   * `sensitive` 档下"命中即拦截"的敏感类别。**空数组 = 参考实现的三类**。
   *
   * 想要"完全不升级"请选 `safe` 档 —— 那一格的字面意思就是这个。
   */
  guard_elevate: string[]
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
  protocol: QyAiProtocol
  guard_controversial: QyAiGuardControversial
  guard_categories: string[]
  guard_elevate: string[]
  /** 省略 = 保持原密钥;空串 = 清除。绝不能把它做成必填。 */
  api_key?: string
  timeout_ms: number
  weight: number
  enabled: boolean
  price_in_per_m: string
  price_out_per_m: string
  remark: string
}

/**
 * 护栏模型那 9 个**固定**类别与本站违规类型的对照表。
 *
 * 它由后端下发而不是前端硬编码:硬编码的那一份与后端的映射是两份必须手工
 * 保持一致的事实,而漏改的表现是界面上写着落到 A、实际落到 B。
 */
export type QyAiGuardCategory = {
  /** 类别 id(snake_case),复选框的 value,也是运营可以拿来建类型的标识。 */
  id: string
  /** 官方展示名,例如 `Non-violent Illegal Acts`。 */
  label: string
  /** @deprecated 与 `label` 同值,留给旧调用点。 */
  guard: string
  /** 它会落到的本站违规类型标识。 */
  key: string
  /**
   * 本站类型表里**有没有**这个标识。
   *
   * 这一位是整张表的价值所在:护栏模型的类别改不动(训练时钉死),所以
   * `false` 意味着这一类的判定必然落进兜底「未分类」。运营想单独处置它,
   * 只能去违规类型页新建一个这个标识的类型 —— 改提示词是没用的。
   */
  present: boolean
}

export type QyAiChannelList = {
  items: QyAiChannel[]
  /** 后端有没有配 violation.ai_review_key。没配就存不下密钥。 */
  key_configured: boolean
  /**
   * 护栏九类与本站类型的对照表。名字与渠道上那一格
   * (`QyAiChannel.guard_categories` = 启用子集)分开 —— 两者是不同层级的
   * 东西,同名会让人以为改一个能影响另一个。
   */
  guard_catalog: QyAiGuardCategory[]
  /** `sensitive` 档留空时真正生效的那三类。不下发它,界面上"留空 = 默认"就是一句没人验证得了的话。 */
  guard_elevate_default: string[]
}

/**
 * AI 审核的全局设置。
 *
 * 这里**没有抽样率**:「送不送审」只由作用域策略表回答,一条策略都没有就是
 * 不审核。曾经有一个全局 `sample_rate_bps`(同时是作用域都不命中时的兜底),
 * 它已经下线 —— 它让这一页最重要的问题答不出来:作用域表上一条策略都没有、
 * 看起来什么都没监控,而线上全站 5% 的请求内容正在被发往第三方。
 */
export type QyAiSetting = {
  id: number
  enabled: boolean
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
  /**
   * 这一档送到哪个审核渠道。**0 = 不指定**,含义是「按权重在全部启用渠道里
   * 随机」—— 不是「用某一个固定渠道」,渠道表上没有 priority 这种东西。
   *
   * 指定的渠道被停用或删除时,这一档**不审核**(每次都是「无可用渠道」并直接
   * 放行),运行期绝不回落到随机池:回落会把用户内容发去运营明确没有选的端点,
   * 而「只能发给这一个」往往正是指定渠道的全部理由。
   *
   * 上面这段描述的是 `channel_failover` 关着时的行为,也就是出厂行为。
   */
  channel_id: number
  /**
   * 「指定的渠道不可用时,退到加权随机池」。只在 `channel_id > 0` 时有意义
   * (没指定时本来就走池子),后端会在 `channel_id` 为 0 时把它一并归零。
   *
   * **默认关**:打开它把「只发给这一个」变成「这一个不行就发给池子里的任何
   * 一个」,也就是把用户内容的出境目的地从一个变成一组。那不该由一次升级
   * 替站点决定 —— 存量配置的行为因此逐字节不变。
   */
  channel_failover: boolean
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
 * 汇总表的一行。它不是策略行的回显 —— `shadowed` 在库里不存在,它是这份配置
 * **作为整体**的性质,而"现在哪些分组在被监控"问的正是这个整体。
 *
 * 表里**只有真实存在的策略行**:曾经末尾还有一行「未匹配任何策略」的兜底档,
 * 它连同全局抽样率一起下线了 —— 现在那个问题的答案恒为"不审核",
 * 画一行恒为 0% 的假行只会让人以为那里还有个可调的旋钮。
 */
export type QyAiScopeSummaryRow = {
  id: number
  name: string
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
   * 这一档指定的审核渠道 id,0 = 不指定(按权重随机)。
   *
   * 同样只给 id,名字去 `channels` 里 join —— 那张表上还有 `enabled`,
   * 而「指定的渠道被停用了」正是这一格最要紧的一种状态,只有 join 之后
   * 才看得出来。
   */
  channel_id: number
  /**
   * 「指定的渠道不可用时退到加权随机池」。`channel_id` 为 0 时恒为 `false`。
   *
   * 必须出现在列表上,不能只藏在编辑弹窗里:它改变的是**用户内容会被发到
   * 哪些第三方端点**。运营看到「审核渠道: 内部自建」时的默认理解是"只有它",
   * 而开着这一位时那句话是假的 —— 一次超时就足以让内容去到别处。
   */
  channel_failover: boolean
  /**
   * 这一行没有绑定到任何具体分组:名单为空(= 全部分组),或者方向是排除
   * (= 名单之外的全部分组)。
   *
   * 这样的行现在**存不进来**(后端 validateAIScope 拒绝启用它们),所以它只
   * 可能是存量:「强制绑定分组」之前建的,或者是全局抽样率迁移出来的那一条。
   * 存量行不会被自动改写或停用(静默关掉一条正在生效的风控比留着它更危险),
   * 所以启用中的那些**还在按全站匹配** —— 而它们在这张表上与一条只盯一个
   * 分组的策略长得完全一样。这一列就是把那个差别摆出来的唯一办法。
   */
  group_unbound: boolean
  /**
   * 这一行永远匹配不到:它前面有一条作用域为空(= 匹配一切)的启用策略。
   * 一条被遮住的策略与一条配错作用域的策略在列表上长得一模一样,
   * 而两者的下一步完全不同(调优先级 vs 改作用域)。
   */
  shadowed: boolean
}

export type QyAiScopeList = {
  items: QyAiScope[]
  /** 按匹配顺序排好(没有兜底档)。顺序就是后端热路径的判定顺序。 */
  summary: QyAiScopeSummaryRow[]
  max_scopes: number
  ai_enabled: boolean
  /**
   * 渠道清单,只够 join 用的四样。它让界面把 `channel_id` 变成名字,
   * 并把「指定的渠道已停用 / 已删除」这两种静默失效当场标出来。
   */
  channels: { id: number; name: string; enabled: boolean; model: string }[]
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
    channel_id: number
    /**
     * 快照里那一位。与表单里的不一致说明还没重载到 —— 而它的不一致最难察觉:
     * 关掉之后要等一次重载才真的停,中间这段时间界面写着"关",
     * 线上仍然在往池子里退。
     */
    channel_failover: boolean
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
  /**
   * 花费算不准的审核次数:整条重试链一分钱都没算出来,或者链上有一次产生了
   * token 却没单价(混价链,cost_usd 是正数但只是下界)。> 0 时总额被低估。
   */
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
  /** 整条重试链的合计花费。cost_unknown 为真时它只是下界。 */
  cost_usd: string
  cost_unknown: boolean
  /** 这一次审核实际发出的调用次数(含失败的那几次),0 = 这一列存在之前的行。 */
  attempts: number
  latency_ms: number
  rule_id: number
  record_id: number
  request_id: string
  model_name: string
  using_group: string
  created_at: number
}

export type QyAiChannelTestResult = {
  /** 这一次试跑走的是哪一种协议。与表单里选的不一致说明还没保存。 */
  protocol: QyAiProtocol
  outcome: QyAiOutcome
  violated: boolean
  category: string
  /** 模型给了一个本站类型表里没有的标识时,它的原值。 */
  raw_category: string
  confidence: string
  reason: string
  latency_ms: number
  /** 这一次实际用的预算。护栏渠道试跑时会被抬到冷启动下限,与生产预算不同。 */
  timeout_ms: number
  tokens: { prompt: number; completion: number; total: number }
  cost_usd: string
  priced: boolean
  /**
   * 上游**原样**回的那一段(截断到 2000 字)。
   *
   * 没有它,协议对不上时界面上只有一个 `bad_json`,而它的三种成因(地址指到了
   * 别的服务 / 协议选错了 / 这个部署的输出格式与官方示例不同)长得完全一样。
   * 护栏模型尤其需要:官方没有给出 OpenAI 兼容端点上的字段级规格。
   *
   * 隐私上是干净的:试跑送出去的是后端写死的一句良性文本,响应里不含用户内容。
   */
  raw_response: string
  /** `cold_start` = 护栏渠道首次调用要加载模型,超时是预期的,再点一次即可。 */
  hint?: string
  message?: string
}
