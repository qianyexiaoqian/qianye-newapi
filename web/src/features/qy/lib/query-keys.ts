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

  violationMyRecords: (params: unknown) =>
    [...qyKeys.all, 'violation', 'my-records', params] as const,
  violationMySummary: () => [...qyKeys.all, 'violation', 'my-summary'] as const,

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

  adminTransferRecords: (params: unknown) =>
    [...qyKeys.all, 'admin', 'transfer', 'records', params] as const,
  adminTransferGroupRules: () =>
    [...qyKeys.all, 'admin', 'transfer', 'group-rules'] as const,
  adminTransferConfig: () =>
    [...qyKeys.all, 'admin', 'transfer', 'config'] as const,

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

  adminUserGroupConfig: () =>
    [...qyKeys.all, 'admin', 'user-group', 'config'] as const,

  /**
   * 分组定价的公共前缀。
   *
   * 单独留一个前缀 key 是因为改一次价会同时让规则表与影子对账过期：
   * 逐个失效必然漏掉一个，而漏掉的那个恰好是用来核对这次改价的视图。
   */
  adminGroupPricing: () => [...qyKeys.all, 'admin', 'group-pricing'] as const,
  adminGroupPricingRules: (params: unknown) =>
    [...qyKeys.adminGroupPricing(), 'rules', params] as const,
  adminGroupPricingPreview: (params: unknown) =>
    [...qyKeys.adminGroupPricing(), 'preview', params] as const,
  adminGroupPricingShadow: (params: unknown) =>
    [...qyKeys.adminGroupPricing(), 'shadow', params] as const,
  adminGroupPricingOptions: () =>
    [...qyKeys.adminGroupPricing(), 'options'] as const,
} as const
