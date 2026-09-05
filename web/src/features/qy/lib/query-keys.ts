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
 * qy 扩展的 react-query key 规范。
 *
 * **硬性约定：所有 qy 的 queryKey 必须以 `'qy'` 开头。**
 * 跨库两阶段下前端无法判断一次资金操作到底影响了哪些视图，
 * `invalidateQueries({ queryKey: qyKeys.all })` 全量失效是唯一安全的收尾方式，
 * 而它成立的前提就是这个统一前缀。
 *
 * 新增页面时在下面加一行即可；带参数的列表 key 一律把整个 params 对象放最后一段，
 * react-query 会做结构化比较，分页/筛选变化天然是不同的缓存条目。
 */
export const qyKeys = {
  all: ['qy'] as const,

  config: () => [...qyKeys.all, 'config'] as const,

  // ── 用户端 ──
  transferLimits: () => [...qyKeys.all, 'transfer', 'limits'] as const,
  transferRecords: (params: unknown) =>
    [...qyKeys.all, 'transfer', 'records', params] as const,
  transferPreview: (params: unknown) =>
    [...qyKeys.all, 'transfer', 'preview', params] as const,
  transferContacts: () => [...qyKeys.all, 'transfer', 'contacts'] as const,

  // ── 邀请返星屑（用户端，D-14；取代此前的 commission / withdraw 两组）──
  inviteSummary: () => [...qyKeys.all, 'invite', 'summary'] as const,
  inviteInvitees: (params: unknown) =>
    [...qyKeys.all, 'invite', 'invitees', params] as const,
  /** 我的星屑流水里邀请类那五种 kind 的行（按 kind 筛，参数进 key）。 */
  inviteRecords: (params: unknown) =>
    [...qyKeys.all, 'invite', 'records', params] as const,
  /** 我名下的下线在某一天的消费返基数与计提（一天一份）。 */
  inviteInviteeDaily: (day: string) =>
    [...qyKeys.all, 'invite', 'invitee-daily', day] as const,

  // ── 推广佣金（用户端；与上面的邀请返并行，D-16 起两边都是星屑）──
  commissionSummary: () => [...qyKeys.all, 'commission', 'summary'] as const,
  commissionRecords: (params: unknown) =>
    [...qyKeys.all, 'commission', 'records', params] as const,
  /** 自动入账记录（佣金余额到期批量记入星屑余额的那几笔）。 */
  commissionCredits: (params: unknown) =>
    [...qyKeys.all, 'commission', 'credits', params] as const,

  /** 支付密码状态（是否已设置 / 是否锁定 / 剩余次数）。 */
  payPassword: () => [...qyKeys.all, 'pay-password'] as const,

  /**
   * 我的套餐权益：解锁了哪些模型分组、那笔余额能花在什么上。
   *
   * 消费方是**上游**的钱包页套餐卡与购买确认弹窗，不是某个 qy 页面。它挂在
   * `qy` 前缀下是为了买完之后能被 `invalidateQueries({ queryKey: qyKeys.all })`
   * 一起冲掉 —— 否则用户刚买完，卡片上写的仍然是他买之前的解锁清单。
   */
  myEntitlements: () =>
    [...qyKeys.all, 'subscription', 'entitlements'] as const,

  /**
   * 用户可选的 API 地址（只含已启用的），按**展示位置**分键：
   * picker = 密钥列表「复制链接信息 / CC Switch」，console = 控制台「API信息」
   * 卡片。位置过滤在服务端（`?surface=`），两个位置拿到的是不同子集，
   * 混用一个键会让先到的那份冒充另一个位置的清单。
   *
   * 它仍然挂在 `qy` 前缀下：管理员改完地址簿后一次
   * `invalidateQueries({ queryKey: qyKeys.all })` 必须能把两份一起冲掉，
   * 否则用户会拿到一份已经被删掉的地址。
   */
  apiAddresses: (surface: 'picker' | 'console') =>
    [...qyKeys.all, 'api-addresses', surface] as const,

  /**
   * 每一把 API 密钥今天的消费额。
   *
   * 与 `apiAddresses` 同一档：消费方是**上游**密钥列表的「今日消耗」列，不是某个
   * qy 页面。挂在 `qy` 前缀下是因为它的日界来自扩展配置
   * （`invite.day_offset_minutes`），管理员改完日界之后必须能被全量失效冲掉 ——
   * 否则用户看到的还是按旧日界算出来的那一份。
   */
  tokenTodayUsage: () => [...qyKeys.all, 'token-usage', 'today'] as const,

  /**
   * 每一把 API 密钥此刻的在途请求数与近 1 分钟请求数。
   *
   * 与 `tokenTodayUsage` 分成两把 key，因为它们的**新鲜度**差两个数量级：
   * 今日消耗一分钟内不会变，这一份 5 秒就该重看一眼。共用一把 key 的话，
   * 任何一处失效都会把另一边那次昂贵的聚合也一起冲掉。
   */
  tokenLiveStats: () => [...qyKeys.all, 'token-usage', 'live'] as const,

  /**
   * 受限账号公告（管理员配的那段申诉指引）。
   *
   * 只在受限账号上取数（见 `lib/restricted-notice.ts` 的 `enabled` 判据），
   * 正常账号的浏览器不会发出这次请求。
   */
  restrictedNotice: () => [...qyKeys.all, 'restricted-notice'] as const,

  violationMyRecords: (params: unknown) =>
    [...qyKeys.all, 'violation', 'my-records', params] as const,
  /**
   * 违规汇总。**当前没有任何页面订阅它** —— 用户端违规页按项目方要求只留违规
   * 类型，窗口违规次数 / 距离封号还剩余 / 累计扣费三块连同这个查询一起下线。
   * 保留原因见 `pages/violations/types.ts` 的 `QyMyViolationSummary` 顶部。
   */
  violationMySummary: () => [...qyKeys.all, 'violation', 'my-summary'] as const,
  /** 违规类型公示（只含 published 的类型 + 自己在每一类上的计数）。 */
  violationMyCategories: () =>
    [...qyKeys.all, 'violation', 'my-categories'] as const,

  // ── 抽奖 / 竞猜（用户端）──
  lotteryActivities: (params: unknown) =>
    [...qyKeys.all, 'lottery', 'activities', params] as const,
  lotteryActivity: (actNo: string) =>
    [...qyKeys.all, 'lottery', 'activity', actNo] as const,
  /** 资格预检。**仅用于展示**，放行与否永远以报名接口的返回为准。 */
  lotteryEligibility: (actNo: string) =>
    [...qyKeys.all, 'lottery', 'eligibility', actNo] as const,
  lotteryMyEntries: (params: unknown) =>
    [...qyKeys.all, 'lottery', 'my-entries', params] as const,
  /** 双色球期次系列。详情页用它回答「下一期什么时候」。 */
  lotterySeries: (seriesNo: string) =>
    [...qyKeys.all, 'lottery', 'series', seriesNo] as const,
  /** 证据链。匿名可访问，但缓存 key 仍挂在 qy 前缀下以便一起失效。 */
  lotteryProof: (actNo: string, params: unknown) =>
    [...qyKeys.all, 'lottery', 'proof', actNo, params] as const,
  /**
   * 我中的那一份文本奖（兑换码 / CDK）。
   *
   * **逐条一个 key**，没有列表 key：一个返回全部正文的列表接口意味着一次
   * 越权 bug 就是全量泄漏，所以这条路径上根本不存在批量入口。
   */
  lotteryMyPrize: (payoutNo: string) =>
    [...qyKeys.all, 'lottery', 'my-prize', payoutNo] as const,

  // ── 星屑转盘（用户端；`kind='draw', draw_mode='wheel'`，不进大厅 lane）──
  wheelActivities: (params: unknown) =>
    [...qyKeys.all, 'wheel', 'activities', params] as const,
  /** 我在这一场的转动记录（逐次 seq / ppm / 中的档）。 */
  wheelMySpins: (actNo: string, params: unknown) =>
    [...qyKeys.all, 'wheel', 'my-spins', actNo, params] as const,

  // ── 星屑（用户端）──
  /** 余额 + 昨日结算 + 下次结算时刻，一次请求。 */
  stardustMe: () => [...qyKeys.all, 'stardust', 'me'] as const,
  stardustLedger: (params: unknown) =>
    [...qyKeys.all, 'stardust', 'ledger', params] as const,
  /** 日桶（消费返的逐日计提，含 held 的那几天）。 */
  stardustAccruals: (params: unknown) =>
    [...qyKeys.all, 'stardust', 'accruals', params] as const,

  // ── 商城（用户端）──
  mallProducts: (params: unknown) =>
    [...qyKeys.all, 'mall', 'products', params] as const,
  mallProduct: (productNo: string) =>
    [...qyKeys.all, 'mall', 'product', productNo] as const,
  mallOrders: (params: unknown) =>
    [...qyKeys.all, 'mall', 'orders', params] as const,
  mallOrder: (orderNo: string) =>
    [...qyKeys.all, 'mall', 'order', orderNo] as const,

  // ── 工单(用户端)──
  ticketConfig: () => [...qyKeys.all, 'ticket', 'config'] as const,
  ticketList: (params: unknown) =>
    [...qyKeys.all, 'ticket', 'list', params] as const,
  /** 用户端按业务单号寻址（用户视图不下发自增 id），所以 key 也是单号。 */
  ticketDetail: (ticketNo: string) =>
    [...qyKeys.all, 'ticket', 'detail', ticketNo] as const,

  availabilityMatrix: (params: unknown) =>
    [...qyKeys.all, 'availability', 'matrix', params] as const,
  availabilitySeries: (params: unknown) =>
    [...qyKeys.all, 'availability', 'series', params] as const,

  // ── 管理端 ──
  adminHealth: () => [...qyKeys.all, 'admin', 'health'] as const,
  /** 版本三元组。编译期常量，进程不重启就不会变。 */
  adminVersion: () => [...qyKeys.all, 'admin', 'version'] as const,
  adminFundOrders: (params: unknown) =>
    [...qyKeys.all, 'admin', 'fund-orders', params] as const,
  adminAuditLogs: (params: unknown) =>
    [...qyKeys.all, 'admin', 'audit-logs', params] as const,
  adminRequestAudits: (params: unknown) =>
    [...qyKeys.all, 'admin', 'request-audits', params] as const,
  adminLeases: () => [...qyKeys.all, 'admin', 'leases'] as const,

  /**
   * 受限账号公告（管理端草稿档，含长度上限）。
   *
   * 与用户端的 {@link qyKeys.restrictedNotice} 是两个 key，不是同一份数据的
   * 两个视图：管理端读得到关闭状态下留着的草稿标题/正文，用户端在关闭状态下
   * 拿到的是空串。共用一个 key 会让管理端保存后失效缓存时，把一份**含草稿**
   * 的数据塞进用户端那份的位置。
   */
  adminRestrictedNotice: () =>
    [...qyKeys.all, 'admin', 'restricted-notice'] as const,

  /**
   * 受限账号总览（当前受限账号数 + 受限账号仍可到达的接口分档）。
   *
   * 与 {@link qyKeys.adminRestrictedNotice} 分开：那一份是**草稿**（保存后要
   * 失效重取），这一份是**现状**（保存公告不会改变受限账号的数量）。共用一个
   * key 会让每次保存公告都白跑一次主库计数。
   */
  adminRestrictedAccounts: () =>
    [...qyKeys.all, 'admin', 'restricted-accounts'] as const,

  /**
   * 负余额（透支）总览。
   *
   * 与 {@link qyKeys.adminHealth} 分开：那一份要扩展库活着，这一份只查主库
   * users，扩展库挂掉的时候它仍然答得出。共用一个 key 会让扩展库一挂、
   * 欠款数字跟着一起消失，而那正是最需要看它的时刻。
   */
  adminOverdraft: () => [...qyKeys.all, 'admin', 'overdraft'] as const,

  /**
   * 客户端 IP 识别诊断（当前请求被识别成了什么、为什么）。
   *
   * 与 {@link qyKeys.adminHealth} 分开的理由与 {@link qyKeys.adminOverdraft}
   * 相同：后端 `/admin/client-ip` 只读进程内的取值策略，扩展库挂掉时它仍然
   * 是 200，而 `/admin/health` 会 503。共用一个 key 会让「客户端 IP 被识别
   * 成了什么」跟着扩展库一起消失 —— 而令牌 allow_ips 与按 IP 的限流不依赖
   * 扩展库，它们照样在按这个值放行/拒绝。
   */
  adminClientIP: () => [...qyKeys.all, 'admin', 'client-ip'] as const,

  /**
   * 管理端查某个用户的支付密码状态。
   *
   * 逐用户一个 key。消费方是**上游**用户管理表格里的「重置支付密码」弹窗，
   * 不是某个 qy 页面 —— 挂在 qy 前缀下是为了重置成功后能被
   * `invalidateQueries({ queryKey: qyKeys.all })` 一起冲掉。
   */
  adminPayPassword: (userId: number) =>
    [...qyKeys.all, 'admin', 'pay-password', userId] as const,

  // ── 邀请（管理端，D-14；此前挂在 `admin/commission` 下的那几条搬家）──
  /** 邀请关系列表（绑定中 / 已解绑两个 scope 共用，scope 在 params 里）。 */
  adminInviteRelations: (params: unknown) =>
    [...qyKeys.all, 'admin', 'invite', 'relations', params] as const,
  /** 日消费明细：一行一个用户在某个日期区间内的消费额（数据源是主库 logs）。 */
  adminDailyConsume: (params: unknown) =>
    [...qyKeys.all, 'admin', 'invite', 'daily-consume', params] as const,
  /** 日消费明细的按天下钻：一行一天，只查一个人（点开主表某一行才发）。 */
  adminDailyConsumeByDay: (params: unknown) =>
    [
      ...qyKeys.all,
      'admin',
      'invite',
      'daily-consume',
      'by-day',
      params,
    ] as const,
  /** 下线消费返的日结明细（`qy_sd_invite_accrual`），按日 × 邀请人筛。 */
  adminInviteAccruals: (params: unknown) =>
    [...qyKeys.all, 'admin', 'invite', 'accruals', params] as const,

  // ── 推广佣金（管理端）──
  adminCommissionConfig: () =>
    [...qyKeys.all, 'admin', 'commission', 'config'] as const,
  adminCommissionRecords: (params: unknown) =>
    [...qyKeys.all, 'admin', 'commission', 'records', params] as const,
  adminCommissionHealth: () =>
    [...qyKeys.all, 'admin', 'commission', 'health'] as const,
  adminCommissionBalances: (params: unknown) =>
    [...qyKeys.all, 'admin', 'commission', 'balances', params] as const,
  /** 「佣金用户」列表：一行一个用户（余额 + 上下线 + 关系状态）。 */
  adminCommissionUsers: (params: unknown) =>
    [...qyKeys.all, 'admin', 'commission', 'users', params] as const,
  /** 自动入账记录（全站，按用户 / 状态筛）。 */
  adminCommissionCredits: (params: unknown) =>
    [...qyKeys.all, 'admin', 'commission', 'credits', params] as const,

  adminTransferRecords: (params: unknown) =>
    [...qyKeys.all, 'admin', 'transfer', 'records', params] as const,
  adminTransferGroupRules: () =>
    [...qyKeys.all, 'admin', 'transfer', 'group-rules'] as const,
  adminTransferConfig: () =>
    [...qyKeys.all, 'admin', 'transfer', 'config'] as const,
  /**
   * 按用户分组的门槛分档。
   *
   * 与 `adminTransferConfig` 分开：两者写的是不同的表（`qy_settings` 的一组
   * KV vs `qy_transfer_group_limits` 的整行），而且分档页要显示的「生效值」
   * 依赖全站门槛 —— 改全站门槛之后必须把这一条也冲掉，改分档则不必反过来。
   */
  adminTransferGroupLimits: () =>
    [...qyKeys.all, 'admin', 'transfer', 'group-limits'] as const,

  // 提现审核 / 收款人明文那几条 key 已随提现模块整体删除（D-14）。
  adminViolationRules: (params: unknown) =>
    [...qyKeys.all, 'admin', 'violation', 'rules', params] as const,
  /** 内置防护规则包的目录（代码里的模板 + 本站点的导入状态）。 */
  adminViolationBuiltin: () =>
    [...qyKeys.all, 'admin', 'violation', 'builtin'] as const,
  adminViolationRecords: (params: unknown) =>
    [...qyKeys.all, 'admin', 'violation', 'records', params] as const,
  adminViolationEvidence: (id: number | string) =>
    [...qyKeys.all, 'admin', 'violation', 'evidence', id] as const,
  adminViolationBans: (params: unknown) =>
    [...qyKeys.all, 'admin', 'violation', 'bans', params] as const,
  adminViolationAppeals: (params: unknown) =>
    [...qyKeys.all, 'admin', 'violation', 'appeals', params] as const,
  adminViolationStats: () =>
    [...qyKeys.all, 'admin', 'violation', 'stats'] as const,
  adminViolationCounters: (params: unknown) =>
    [...qyKeys.all, 'admin', 'violation', 'counters', params] as const,
  adminViolationBanPolicies: () =>
    [...qyKeys.all, 'admin', 'violation', 'ban-policies'] as const,
  /** 违规类型（含每一类的规则条数）。 */
  adminViolationCategories: () =>
    [...qyKeys.all, 'admin', 'violation', 'categories'] as const,
  /**
   * 内置类型的建议阈值 + 逐类影响面。
   *
   * 与类型列表分开一个 key：应用之后两者都要失效，但打开建议弹窗时不该顺手
   * 重拉一整张列表 —— 那个列表正是弹窗背后那一页，重拉会让它在弹窗底下抖一下。
   */
  adminViolationCategorySuggestions: () =>
    [...qyKeys.all, 'admin', 'violation', 'categories', 'suggestions'] as const,
  /** AI 审核：渠道池、全局设置、成本统计、调用明细。 */
  adminViolationAiChannels: () =>
    [...qyKeys.all, 'admin', 'violation', 'ai-review', 'channels'] as const,
  adminViolationAiSettings: () =>
    [...qyKeys.all, 'admin', 'violation', 'ai-review', 'settings'] as const,
  // 作用域策略与设置分开一个 key,但改任何一个都要让另一个失效:兜底抽样率
  // 存在设置里、策略存在这张表里,而汇总表把两者画在同一张表上 —— 只失效
  // 其中一个,那张表就会一半是新的一半是旧的。
  adminViolationAiScopes: () =>
    [...qyKeys.all, 'admin', 'violation', 'ai-review', 'scopes'] as const,
  // cyber 会话屏蔽设置。与 AI 审核是两套东西,独立一个 key。
  adminViolationCyberSettings: () =>
    [...qyKeys.all, 'admin', 'violation', 'cyber-session', 'settings'] as const,
  // 成本统计带天数：改了回看窗口就必须重算,否则运营看到的还是上一档的数字,
  // 而那个数字正是"这个月花了多少"的答案。
  adminViolationAiStats: (days: number) =>
    [...qyKeys.all, 'admin', 'violation', 'ai-review', 'stats', days] as const,
  adminViolationAiLogs: (params: unknown) =>
    [...qyKeys.all, 'admin', 'violation', 'ai-review', 'logs', params] as const,
  // 影响面预览带参数：阈值/窗口/动作任意一项变了，那个数字就必须重算。
  // 不把参数放进 key 的话，管理员改完阈值看到的仍是上一次的数 —— 而这个数
  // 正是他决定要不要按下保存的唯一依据。
  adminViolationBanPolicyImpact: (params: unknown) =>
    [
      ...qyKeys.all,
      'admin',
      'violation',
      'ban-policies',
      'impact',
      params,
    ] as const,

  // ── 抽奖 / 竞猜（管理端）──
  adminLotteryActivities: (params: unknown) =>
    [...qyKeys.all, 'admin', 'lottery', 'activities', params] as const,
  adminLotteryActivity: (actNo: string) =>
    [...qyKeys.all, 'admin', 'lottery', 'activity', actNo] as const,
  adminLotteryEntries: (actNo: string, params: unknown) =>
    [...qyKeys.all, 'admin', 'lottery', 'entries', actNo, params] as const,
  adminLotteryPayouts: (actNo: string, params: unknown) =>
    [...qyKeys.all, 'admin', 'lottery', 'payouts', actNo, params] as const,
  adminLotteryEvents: (actNo: string) =>
    [...qyKeys.all, 'admin', 'lottery', 'events', actNo] as const,
  /**
   * 文本奖履行队列（一场活动内的 `kind='text'` 出款行）。
   *
   * 与 {@link qyKeys.adminLotteryPayouts} 分开是刻意的：那一张列表的每一行都是
   * 资金单（重试、卡单、代次），这一张的每一行是**一件人要去做的事**。
   * 混在一起会让"还有 3 笔没发出去"和"还有 3 份码没填"看起来是同一个红点。
   */
  adminLotteryTextPrizes: (actNo: string, params: unknown) =>
    [...qyKeys.all, 'admin', 'lottery', 'text-prizes', actNo, params] as const,
  adminLotteryFlags: (actNo: string) =>
    [...qyKeys.all, 'admin', 'lottery', 'flags', actNo] as const,
  adminLotteryConfig: () =>
    [...qyKeys.all, 'admin', 'lottery', 'config'] as const,
  /**
   * 双色球期次系列（管理端）。
   *
   * 与活动列表分开：一个系列跨很多期，它持有号池（期与期之间不可变）与累计
   * 注资上限，而活动行只是它的一期。混进活动的 key 会让"注资一笔"把整张活动
   * 列表也一起失效掉。
   */
  adminLotterySeries: (params: unknown) =>
    [...qyKeys.all, 'admin', 'lottery', 'series', params] as const,

  // ── 星屑（管理端）──
  adminStardustConfig: () =>
    [...qyKeys.all, 'admin', 'stardust', 'config'] as const,
  /** 按用户分组的费率覆盖（消费返 / 邀请返）。 */
  adminStardustGroupRates: () =>
    [...qyKeys.all, 'admin', 'stardust', 'group-rates'] as const,
  /**
   * 单个套餐的一次性返星屑。按 planId 分键，理由同 {@link qyKeys.adminPlanEntitlement}：
   * 保存 A 套餐不该把屏幕上其余套餐的面板一起 refetch。
   */
  adminStardustPlanReward: (planId: number) =>
    [...qyKeys.all, 'admin', 'stardust', 'plan-rewards', planId] as const,
  adminStardustBalances: (params: unknown) =>
    [...qyKeys.all, 'admin', 'stardust', 'balances', params] as const,
  adminStardustLedger: (params: unknown) =>
    [...qyKeys.all, 'admin', 'stardust', 'ledger', params] as const,
  adminStardustAccruals: (params: unknown) =>
    [...qyKeys.all, 'admin', 'stardust', 'accruals', params] as const,
  /** 账本恒等式对账 + 暂缓桶积龄。 */
  adminStardustLedgerCheck: () =>
    [...qyKeys.all, 'admin', 'stardust', 'ledger-check'] as const,
  /**
   * 结算调度状态（上一跑 / 下次结算时刻 / 目标日）。
   *
   * 与 {@link qyKeys.adminStardustLedgerCheck} 分开：体检要扫全表余额与流水，
   * 而这一份只读一行运行记录 —— 重跑一天之后两份都要失效，但刷新调度状态
   * 不该顺手把整张体检重跑一遍。
   */
  adminStardustSettleStatus: () =>
    [...qyKeys.all, 'admin', 'stardust', 'settle-status'] as const,
  /**
   * 套餐清单（上游 `/api/subscription/admin/plans`），给「套餐返还」编辑器的
   * 下拉用。挂在 qy 前缀下是为了与其余星屑配置一起被全量失效冲掉；它不是
   * qy 的接口，所以不进 `route-contract.test.ts` 的对账。
   */
  adminStardustPlanList: () =>
    [...qyKeys.all, 'admin', 'stardust', 'plan-list'] as const,

  // ── 商城（管理端）──
  adminMallProducts: (params: unknown) =>
    [...qyKeys.all, 'admin', 'mall', 'products', params] as const,
  adminMallOrders: (params: unknown) =>
    [...qyKeys.all, 'admin', 'mall', 'orders', params] as const,
  /**
   * 套餐商品表单里「选哪个套餐」的候选清单（上游 `/api/subscription/admin/plans`）。
   *
   * 数据是上游的，key 仍挂在 qy 前缀下：这份清单只在商城管理页被消费，
   * 而运营在上游改完套餐再回到这里时，一次 `invalidateQueries({ queryKey: qyKeys.all })`
   * 必须能把它一起冲掉 —— 否则下拉里还是改名之前的那份。
   */
  adminMallPlanOptions: () =>
    [...qyKeys.all, 'admin', 'mall', 'plan-options'] as const,

  /** API 地址簿（管理端，含已停用的行）。 */
  adminApiAddresses: () => [...qyKeys.all, 'admin', 'api-addresses'] as const,

  // ── 工单(管理端)──
  adminTickets: (params: unknown) =>
    [...qyKeys.all, 'admin', 'ticket', 'list', params] as const,
  adminTicket: (id: number) =>
    [...qyKeys.all, 'admin', 'ticket', 'detail', id] as const,
  adminTicketStats: () => [...qyKeys.all, 'admin', 'ticket', 'stats'] as const,

  adminUserGroupConfig: () =>
    [...qyKeys.all, 'admin', 'user-group', 'config'] as const,

  /**
   * 站点分组候选清单（见 `lib/group-options.ts`）。
   *
   * 刻意与 `adminTransferGroupRules` 分开：那个 key 挂着「规则 + 矩阵」，改一条
   * 划转规则就要把它冲掉；而分组清单只随分组倍率表变化，跟着一起失效只会让每个
   * 用到分组下拉的表单在无关的保存之后重新拉一次。
   */
  adminGroupOptions: () => [...qyKeys.all, 'admin', 'group-options'] as const,

  /**
   * 用户分组 × 模型分组 矩阵的公共前缀。
   *
   * 矩阵与孤儿基线各自一条：孤儿是历史欠账的盘点，跑好几条全表聚合，
   * 跟着每次保存一起 refetch 只是白烧数据库。
   */
  /**
   * 单个套餐的「解锁模型分组 + 余额使用范围」。
   *
   * 按 planId 分键而不是一个大前缀：这一段配置只随该套餐的编辑变化，
   * 挂在共享前缀上会让保存 A 套餐把屏幕上其余套餐的面板一起 refetch。
   */
  adminPlanEntitlement: (planId: number) =>
    [...qyKeys.all, 'admin', 'plan-entitlement', planId] as const,

  adminGroupMatrix: () => [...qyKeys.all, 'admin', 'group-matrix'] as const,
  adminGroupMatrixData: () => [...qyKeys.adminGroupMatrix(), 'data'] as const,
  adminGroupMatrixOrphans: () =>
    [...qyKeys.adminGroupMatrix(), 'orphans'] as const,

  adminModelGroups: () => [...qyKeys.all, 'admin', 'model-groups'] as const,
  /**
   * 单个模型分组的删除影响面。
   *
   * 按名字分键而不是挂在共享前缀上：影响面是一次实时探测（要扫 abilities /
   * channels / tokens），删完 A 之后 refetch 屏幕上其余每一行的影响面，
   * 会在一次点击里发出十几个全表扫描。
   */
  adminModelGroupImpact: (name: string) =>
    [...qyKeys.adminModelGroups(), 'impact', name] as const,

  /** 用户分组**登记表**(`qy_user_groups`)。与矩阵页的行轴不是同一份数据。 */
  adminUserGroupRoster: () => [...qyKeys.all, 'admin', 'user-groups'] as const,
  /**
   * 单个用户分组的删除影响面。
   *
   * 按名字分键、且带上迁移目标:目标一换,可用清单与倍率差整份重算,
   * 而那正是运营在弹窗里唯一要读的东西。共用一个键会让换目标时先渲染一份
   * 属于上一个目标的差异 —— 一份看起来完全正常的、错的报价。
   */
  adminUserGroupImpact: (name: string, target: string) =>
    [...qyKeys.adminUserGroupRoster(), 'impact', name, target] as const,
} as const
