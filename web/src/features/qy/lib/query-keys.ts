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

  commissionSummary: () => [...qyKeys.all, 'commission', 'summary'] as const,
  commissionInvitees: (params: unknown) =>
    [...qyKeys.all, 'commission', 'invitees', params] as const,
  commissionRecords: (params: unknown) =>
    [...qyKeys.all, 'commission', 'records', params] as const,

  withdrawConfig: () => [...qyKeys.all, 'withdraw', 'config'] as const,
  withdrawRecords: (params: unknown) =>
    [...qyKeys.all, 'withdraw', 'records', params] as const,
  withdrawRecord: (id: number | string) =>
    [...qyKeys.all, 'withdraw', 'record', id] as const,
  withdrawPayees: () => [...qyKeys.all, 'withdraw', 'payees'] as const,

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
   * 用户可选的 API 地址（只含已启用的）。
   *
   * 消费方是**上游**密钥列表上的「复制链接信息」，不是某个 qy 页面。它仍然挂在
   * `qy` 前缀下：管理员改完地址簿后一次 `invalidateQueries({ queryKey: qyKeys.all })`
   * 必须能把它一起冲掉，否则用户会拿到一份已经被删掉的地址。
   */
  apiAddresses: () => [...qyKeys.all, 'api-addresses'] as const,

  violationMyRecords: (params: unknown) =>
    [...qyKeys.all, 'violation', 'my-records', params] as const,
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

  adminCommissionConfig: () =>
    [...qyKeys.all, 'admin', 'commission', 'config'] as const,
  adminCommissionRecords: (params: unknown) =>
    [...qyKeys.all, 'admin', 'commission', 'records', params] as const,
  adminCommissionHealth: () =>
    [...qyKeys.all, 'admin', 'commission', 'health'] as const,
  adminCommissionBalances: (params: unknown) =>
    [...qyKeys.all, 'admin', 'commission', 'balances', params] as const,
  /** AFF 关系列表（绑定中 / 已解绑两个 scope 共用，scope 在 params 里）。 */
  adminCommissionRelations: (params: unknown) =>
    [...qyKeys.all, 'admin', 'commission', 'relations', params] as const,
  /** 「用户佣金」列表：一行一个用户（余额 + 上下线 + 关系状态）。 */
  adminCommissionUsers: (params: unknown) =>
    [...qyKeys.all, 'admin', 'commission', 'users', params] as const,
  /** 某个用户的下线列表（权威口径来自主库 users.inviter_id）。 */
  adminCommissionUserDownlines: (params: unknown) =>
    [
      ...qyKeys.all,
      'admin',
      'commission',
      'users',
      'downlines',
      params,
    ] as const,

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

  adminWithdrawals: (params: unknown) =>
    [...qyKeys.all, 'admin', 'withdraw', 'list', params] as const,
  adminWithdrawal: (id: number | string) =>
    [...qyKeys.all, 'admin', 'withdraw', 'detail', id] as const,
  adminWithdrawStats: () =>
    [...qyKeys.all, 'admin', 'withdraw', 'stats'] as const,
  adminWithdrawPiiAudits: (params: unknown) =>
    [...qyKeys.all, 'admin', 'withdraw', 'pii-audits', params] as const,

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
