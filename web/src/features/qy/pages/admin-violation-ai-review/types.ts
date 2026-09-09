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
 * - `granite_guardian` —— IBM Granite Guardian。**一次只审一种风险**,
 *   只回 Yes / No;审哪一种由 `risk_name` 决定,类别因此来自"我们问了什么"
 *   而不是模型的回答。
 * - `llama_guard` —— Meta Llama Guard。一次给出多标签:`safe`,或者
 *   `unsafe` + 一行 S 码(S1…S14)。
 *
 * 几条路并列,不互相取代 —— 代价、延迟、准确性、类型体系都不同,界面上必须
 * 把差别说出来,不能只给一个下拉框。
 */
/**
 * 审核渠道说哪一种"方言"。**必须与后端 normalizeAIProtocol 的取值集一字不差。**
 *
 * 少一档的代价不是"少一个选项":存着那一档的渠道在下拉里匹配不到任何 option,
 * 触发器显示空白、一动就被重置成第一项 —— 表现就是「已添加的渠道再也改不了协议」。
 * `granite_guardian` 后端(归一、校验、aireview_granite.go 的解析器)早就齐了,
 * 而这里漏了一档,于是演示站上那个 granite 渠道整整一段时间改不动。
 */
export type QyAiProtocol =
  | 'json_prompt'
  | 'qwen3guard'
  | 'granite_guardian'
  | 'llama_guard'

/**
 * Granite Guardian 一次要审的那一种风险。**必须与后端 graniteRisks 的键一字不差。**
 *
 * 空串 = `harm` = 不发 system = 这一格出现之前的行为。拼写按 **Ollama 模板**
 * 那一套(`jailbreak` 而不是 HuggingFace 的 `jailbreaking`)—— 模板对 system
 * 做的是精确匹配,拼错会静默落回 harm 档,而那是一次看不见的口径改变。
 *
 * 后端只放行这七档:另外四档(groundedness / answer_relevance /
 * context_relevance / function_calling)要 assistant / context / tools 角色,
 * 转发前审核只有用户消息这一个挂载点,给不出来。
 */
export type QyAiGraniteRisk =
  | ''
  | 'harm'
  | 'jailbreak'
  | 'violence'
  | 'sexual_content'
  | 'social_bias'
  | 'profanity'
  | 'unethical_behavior'

/** 下拉框的固定出场顺序,与后端 graniteRiskOrder 一致。 */
export const QY_AI_GRANITE_RISKS: readonly Exclude<QyAiGraniteRisk, ''>[] = [
  'harm',
  'jailbreak',
  'violence',
  'sexual_content',
  'social_bias',
  'profanity',
  'unethical_behavior',
]

/**
 * 一档作用域策略在它指定的那几个渠道之间怎么分发。
 *
 * - `weighted` —— 按渠道权重加权随机。**零值档**:库里那一列是空串时就是它,
 *   也是这一格存在之前的唯一行为。权重是运营表达"主用哪个、备用哪个"的方式。
 * - `round_robin` —— 按清单顺序轮流,每次请求换一个起点,不看权重。
 *   几台**同规格**的护栏机上它比随机更稳:随机的方差会让某一台在某一分钟里
 *   连吃几倍的量,而小模型机的并发很浅。
 *
 * 轮询游标在后端是**进程内**的:多节点部署时每个节点各转各的,合起来仍然是
 * 均分,但任何单个节点上的顺序都不代表全局顺序。
 */
export type QyAiChannelMode = 'weighted' | 'round_robin'

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
  /**
   * Granite 这一次审的风险。非 Granite 渠道上恒为空串(后端写入侧已清空)。
   *
   * 与 `protocol` 刻意相反,后端**不**把空串补齐成 `harm`:空串就是运营选的
   * "默认",把它画成显式选中的 harm 会让"没动过"与"选了 harm"在界面上
   * 无法区分,而这两者在审计差异页是不同的事。
   */
  risk_name: QyAiGraniteRisk
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
  /** 审核渠道分组。空串是合法值(= 未分组),不是"属于所有分组"。 */
  group: string
  /** 这个渠道的审核提示词基底。空 = 用内置默认(发出去的那一份会拼上类型清单)。 */
  prompt: string
  /** 别用 `prompt !== ''` 自己算:输入框预填之后那个判断永远为真。 */
  prompt_source: QyAiPromptSource
  /** 命中拦截时返回给用户的那句话。空 = 沿用规则自己的那一份。 */
  block_message: string
  /**
   * 这个渠道判出的违规,**计次记到哪个违规类型**上。`0` = 不指定。
   *
   * 护栏协议的类别是训练时钉死的,本站类型表里没有同名标识的那几类会折进兜底
   * 「未分类」,而兜底类型的阈值出厂是 0 —— 判了、记了,却一次都不推进封号线。
   * 指定一个类型之后,这个渠道判出来的每一条都记进它并计次。
   *
   * 优先级是 **作用域 > 渠道 > 规则**:作用域那一格写的是「一律记为」,
   * 而同一个渠道会被多档作用域用到,所以它只在作用域没指定时生效。
   */
  category_id: number
  /** 判出违规时给被判的那个用户发一封邮件。影子命中恒不发。 */
  notify_email: boolean
  /** 邮件标题模板。空 = 用内置默认。 */
  email_subject: string
  /** 邮件正文模板,按 HTML 发送。空 = 用内置默认。 */
  email_body: string
  has_key: boolean
  key_hint: string
  /**
   * 地址被改过，而已存密钥是写给旧地址的。
   *
   * 密钥绑在它写入时的那个 `base_url` 上：不一致时它既不会被发往新地址，
   * 这个渠道也不会参与审核（后端装配期直接跳过）。挡的是「改地址 + 点试跑」
   * 那条把只写密钥取走的路。修法只有一个：在编辑表单里重填一次密钥。
   *
   * 必须画出来 —— 否则这一行看起来配得好好的（有密钥、启用中），
   * 而它一次都不会被调用，且 AI 审核失败的方向是放行。
   */
  key_bound_elsewhere: boolean
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
  /** 审核渠道分组。空串 = 未分组(合法值)。 */
  group: string
  /** 这个渠道的审核提示词。逐字等于内置默认时提交空串,见 qyAiPromptToPayload。 */
  prompt: string
  /** 命中拦截时返回给用户的那句话。空 = 沿用规则自己的那一份。 */
  block_message: string
  /** 判出的违规计次记到哪个类型上。`0` = 不指定,按规则自己绑的类型记。 */
  category_id: number
  /** 判出违规时给被判的那个用户发一封邮件。 */
  notify_email: boolean
  /** 邮件标题模板。空 = 用内置默认。 */
  email_subject: string
  /** 邮件正文模板(HTML)。空 = 用内置默认。 */
  email_body: string
  base_url: string
  model: string
  protocol: QyAiProtocol
  /** 只在 protocol === 'granite_guardian' 时会被提交,见 qyAiDraftToInput。 */
  risk_name: QyAiGraniteRisk
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
  // **没有 prompt。** 2026-09-06 起提示词住在每个审核渠道上(QyAiChannel.prompt),
  // 理由是它与协议绑死:护栏协议压根不发提示词。
  max_input_chars: number
  third_party_notice_ack: boolean
  /** 审核明细里留不留一份(脱敏并截断后的)送审内容。 */
  log_content: boolean
  /**
   * 判定违规的行**始终留存、且留完整**(不受上面那个开关与下面那个字数上限约束)。
   *
   * 后端是三态列(NULL = 从没设置过 = 按开处理),但 GET 回显时已经折成布尔,
   * 所以这里是 `boolean`。保存时原样回传即可 —— 那一次就写下了显式值。
   */
  log_content_violation_full: boolean
  /** 留存内容的字符上限。生效范围见 effective.log_content_range。 */
  log_content_max_chars: number
  /** 审核明细的保留天数,1..effective.max_log_retention_days。没有「永久保留」这一档。 */
  log_retention_days: number
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

/**
 * cyber 会话屏蔽设置 —— 与 AI 审核是两套东西:AI 审核判**内容**,这里认
 * **上游的拒绝码**(如 cyber_policy),命中一次就把整条会话在本地拉黑。
 */
export type QyCyberSetting = {
  id: number
  enabled: boolean
  /**
   * 受屏蔽的**模型分组**名单(逗号/换行分隔)。空 = 全部模型分组。
   * 与 AI 审核作用域同口径:比的是请求实际使用的分组(UsingGroup)。
   */
  group_scope: string
  group_scope_mode: 'include' | 'exclude'
  /** 拉黑存活时长(秒),到期自动解封。0 = 用默认值(1 小时)。 */
  ttl_seconds: number
  /**
   * **触发过滤规则**:哪些上游拒绝算 cyber 命中。一行一条(也认逗号)，
   * 子串匹配、大小写不敏感，同时比对上游错误码与错误正文。
   * 空 = 运行期回落默认（不会变成"谁都不拦"）。
   */
  trigger_codes: string
  /** 命中是否**计入自动封号计数**（推进账号总量线与类型线，达阈值自动处置）。 */
  count_toward_ban: boolean
  /** 计数类型绑定：命中计到哪个违规类型上。0 = 不指定，落「未分类」兜底。 */
  category_id: number
}

export type QyCyberSettingResponse = {
  setting: QyCyberSetting
  /** 「还原默认过滤内容」按钮要填回的出厂触发过滤规则。 */
  default_trigger_codes: string
  effective: {
    /** 快照里**真正生效**吗。开了设置但 YAML violation 总开关关着时为 false。 */
    active: boolean
    /** YAML violation.enabled —— 基础设施级总闸,关着时本功能一律不生效。 */
    module_on: boolean
    default_ttl: number
    max_ttl: number
  }
}

export type QyAiSettingResponse = {
  setting: QyAiSetting
  default_prompt: string
  /** 违规类型表里参与 AI 审核的 key,**由后端从类型表现算**,不是写死的闭集。 */
  categories: string[]
  /** 自动生成的那一段类型清单。前端用它在本地渲染预览与做同一套对账。 */
  category_block: string
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
    /**
     * 审核明细写在独立的台账库(log_database.dsn)里吗。
     *
     * 界面据此说明日志落在哪 —— 没有它,运维改完那一段配置无法确认它到底
     * 生效没有,而这一段最安静的失败模式是段名写错 → 整段被忽略 → 一切照旧。
     */
    log_db_separate: boolean
    log_content_range: { min: number; max: number }
    max_log_retention_days: number
  }
}

/**
 * 保存设置的回显。与 GET 同形,因为提示词把类型闭集改坏时接口仍然返回 200
 * (收窄类型是合法用法,不该被拒),"哪里坏了"必须随这一次响应一起回来。
 */
export type QyAiSettingSaveResult = {
  setting: QyAiSetting
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
   * 这一档要用的**审核渠道分组**(见 QyAiChannel.group)。
   *
   * 与 `channel_ids` 二选一,而且启用中的策略不允许两个都空(后端 400):
   * 两个都空的旧含义是"发给全部启用渠道",那会让之后新加的任何渠道自动开始
   * 收到用户内容。两格同时填时 `channel_ids` 是主选,分组是它们全挂之后的补位池。
   *
   * **提示词不在这里了** —— 2026-09-06 起它住在渠道上。
   */
  channel_group: string
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
  channel_ids: number[]
  /**
   * 这几个渠道之间怎么分发。空串 = `weighted`(存量行的零值)。
   *
   * 库里那一列可能是空串,而汇总表 (`summary`) 下发的是**归一之后**的值 ——
   * 两处形态不同是刻意的:这里是策略行的回显(表单要能区分"没存过"),
   * 那里回答的是"这一档实际会怎么跑"。
   */
  channel_mode: QyAiChannelMode | ''
  /**
   * 「指定的渠道都不可用时,退到加权随机池」。只在清单非空时有意义
   * (没指定时本来就走全部启用渠道),后端会在清单为空时把它一并归零。
   *
   * **默认关**:打开它把「只发给这几个」变成「它们不行就发给池子里的任何
   * 一个」,也就是把用户内容的出境目的地从一组变成全部。那不该由一次升级
   * 替站点决定 —— 存量配置的行为因此逐字节不变。
   */
  channel_failover: boolean
  remark: string
  created_at: number
  updated_at: number
}

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
   * 这一档用的审核渠道分组(空 = 用下面的 channel_ids 指定)。
   *
   * 摆在汇总表上而不是只在编辑表单里:"这一档的用户内容会流到哪个池子"是这张表
   * 最该一眼看清的事,而一条指错分组的策略与一条正常的在列表上长得完全一样。
   */
  channel_group: string
  /** 这一档指定的「命中一律记为」类型 id,0 = 不指定。类型名去违规类型清单里 join。 */
  category_id: number
  /**
   * 这一档指定的审核渠道,空 = 不指定(在全部启用渠道之间分发)。
   *
   * 同样只给 id,名字去 `channels` 里 join —— 那张表上还有 `enabled`,
   * 而「指定的渠道被停用了」正是这一格最要紧的一种状态,只有 join 之后
   * 才看得出来。
   */
  channel_ids: number[]
  /**
   * 这几个渠道之间怎么分发。汇总表给的是**归一之后**的值(不会是空串):
   * 这张表回答的是"这一档实际会怎么跑",而空串在那个问题下没有答案。
   */
  channel_mode: QyAiChannelMode
  /**
   * 「指定的渠道都不可用时退到加权随机池」。清单为空时恒为 `false`。
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
   * 渠道清单,只够 join 用的四样。它让界面把 `channel_ids` 变成名字,
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
    /** 快照里那一档用的渠道分组。与表单里的不一致说明还没重载到。 */
    channel_group: string
    category_id: number
    channel_ids: number[]
    /** 快照里那一档的分发方式(已归一)。与表单里的不一致说明还没重载到。 */
    channel_mode: QyAiChannelMode
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
  /** 被审那次请求的模型名(不是审核渠道的模型,那是 review_model)。 */
  model_name: string
  /** 被审那次请求实际使用的用户分组。 */
  using_group: string
  /**
   * 送审内容的**原始**字符数(截断入库之前)。
   *
   * 列表接口不下发 content 本身(一页几十 KB 的 text 列,而表格里也放不下),
   * 只给这个数:> 0 表示这一行有内容可看,点开详情接口去取。
   */
  content_chars: number
  /** 列表里恒为 undefined —— 内容只在详情接口返回。 */
  content?: string
  created_at: number
}

/** 单条审核明细的详情,含送审内容。 */
export type QyAiReviewLogDetail = {
  item: QyAiReviewLog & { content: string }
  /** 留存的那一段被截断过(content 的字数 < content_chars)。 */
  truncated: boolean
  /** 内容留存开关**当前**是否打开 —— 用来解释"为什么这条没有内容"。 */
  log_content_enabled: boolean
}

/** 审核日志列表的筛选条件。空串/undefined 一律不参与筛选。 */
export type QyAiLogFilters = {
  group?: string
  model?: string
  /** 按**审核渠道 id** 筛。用 id 不用名字:渠道改名之后历史明细里存的是旧名。 */
  channel_id?: number
  phase?: string
  outcome?: string
  /** '1' 只看判违规的,'0' 只看判未违规的,'' 全部。 */
  violated?: string
  user_id?: number
  request_id?: string
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
