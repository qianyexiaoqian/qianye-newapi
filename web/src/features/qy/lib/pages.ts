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
  ClipboardList,
  Gauge,
  HandCoins,
  HeartPulse,
  LifeBuoy,
  Megaphone,
  ReceiptText,
  Repeat,
  ScrollText,
  ShieldAlert,
  Ticket,
  TriangleAlert,
} from 'lucide-react'
import type { ElementType } from 'react'

import type { QyFeatures } from './types'

/**
 * qy 扩展所有页面的**单一登记表**。
 *
 * ── 为什么合并 ──
 * 同一份页面清单此前散在三个文件里：`nav.ts`（标题 + 功能开关）、
 * `lib/page-meta.ts`（日文副标）、`hooks/use-qy-nav-en-label.ts`（英文副标），
 * 本次重排还要再引入第四份（lucide 图标 —— 上游每个侧栏项都有图标，qy 项
 * 一个都没有，混进上游分组后会左对齐错位）。四份拷贝各自漂移是本项目反复
 * 出现的缺陷形状，所以这里一次性收敛成一张表，三个消费方改成从这里派生。
 *
 * 新增一个页面 = 本表加一行 + `lib/page-order.ts` 末尾追加 url，别处零改动。
 */

// ───────────────────────────── 分组 ─────────────────────────────

/**
 * 页面在侧边栏里的落点。
 *
 * `general` / `personal` / `admin` 是**上游已有的分组 id**
 * （见 `hooks/use-sidebar-data.ts`）：qy 的页面按语义并进去，而不是自己
 * 平铺成一长条 —— 「模型可用率」是只读数据面板、和上游的仪表盘同类；
 * 「支付密码」是账户安全设置、属于 Profile 一带；「分组定价」是模型侧的
 * 管理项、紧跟 Models。
 *
 * `qy-*` 是新增分组，只在语义上确实无处可挂时才建，且每组行数受控
 * （最大 6 行，不超过上游 admin 组的 7 行）。
 *
 * {@link QY_SETTINGS_GROUP} 是个**哨兵**而不是根侧栏上的分组：标了它的页面
 * 根本不出现在根侧栏，而是并进上游「系统设置」的抽屉里（见 `system-settings.ts`）。
 * 用同一个 `group` 字段表达是刻意的 —— 落点始终是"恰好一个"，多加一个布尔
 * 字段就会出现"既声明了分组又声明了进设置"这种自相矛盾的行。
 */
export type QyNavGroupId =
  | 'general'
  | 'personal'
  | 'admin'
  | 'qy-growth'
  | 'qy-settlement'
  | 'qy-risk'
  | typeof QY_SETTINGS_GROUP

/**
 * 「并进上游系统设置抽屉」的落点标记（需求 8）。
 *
 * 项目方原话：「对于管理员的一些菜单，你可以加入到系统设置里面的菜单去。」
 * 界线是**配置 vs 流水**：改了会影响后续计费/风控行为的页面进设置抽屉，
 * 审核与记录类留在根侧栏 —— 后者是日常运营每天要开的，埋进抽屉里反而难找。
 */
export const QY_SETTINGS_GROUP = 'system-settings'

/** 新增分组的定义。上游分组不在此列 —— 它们由上游自己声明。 */
export type QyNavGroupDef = {
  id: Extract<QyNavGroupId, `qy-${string}`>
  titleKey: string
  /** 插到哪个上游分组之后。同一锚点的多个新组按本表声明顺序排列。 */
  afterGroup: 'general' | 'personal' | 'admin'
}

/**
 * ⚠️ 新增分组**没有** `requiredRole` / `adminOnly` 之类的字段，这是刻意的。
 *
 * 上游 `hooks/use-sidebar-view.ts` 的角色过滤只裁项、不裁"裁空之后的组"，
 * 所以任何靠 `NavItem.requiredRole` 隐藏的管理分组，都会给普通用户留下一个
 * 只剩标题的空「结算」分组 —— 标题本身就是信息泄漏。
 *
 * 这里的规则只有一条、且只有一处实现：**成员一个都不可见的分组直接不生成**
 * （`nav.ts` 的 `mergeQyNavGroups`）。成员可见性由 `isQyPageVisible` 统一判定
 * （角色 × 功能开关），分组只是跟着走。多一个"分组也自己判一次角色"的字段
 * 就是同一概念的第二份拷贝，迟早与成员判定漂移。
 */

export const QY_NAV_GROUPS: readonly QyNavGroupDef[] = [
  {
    id: 'qy-growth',
    titleKey: 'qy_nav_group_growth',
    afterGroup: 'personal',
  },
  {
    id: 'qy-settlement',
    titleKey: 'qy_nav_group_settlement',
    afterGroup: 'admin',
  },
  {
    id: 'qy-risk',
    titleKey: 'qy_nav_group_risk',
    afterGroup: 'admin',
  },
]

// ─────────────────────────── 选项卡组 ───────────────────────────

/**
 * 「选择夹」——把若干张页面收进同一个宿主页面的一组标签页。
 *
 * ── 为什么是一张表而不是各写各的 ──
 * 这件事有三个消费方：① 侧栏（被收进去的页面**不再有独立入口**）；
 * ② 宿主页面（要按同一顺序渲染标签）；③ 旧路由（要重定向到宿主 + 对应
 * 标签）。三处各写一份清单就是本仓反复出现的「同一概念的第 N 份拷贝」，
 * 迟早出现"侧栏删了但页面还在旧位置"这类断链。所以清单只此一张。
 *
 * `pages[0]` 允许等于 `host` 本身：推广佣金的第一张标签就是宿主页
 * `/qy/affiliate` 自己（它仍然要有侧栏入口，所以它不算"被收进去"）。
 *
 * 宿主是**上游页面**时（`/wallet`），标签状态只能走 URL hash：上游钱包路由
 * 的 `validateSearch` 用 zod object 校验，未登记的 query 参数会被路由器
 * 直接抹掉，而 hash 不经过校验。两个宿主统一用 hash，省得出现两套机制。
 */
export type QyTabGroupDef = {
  /** 宿主页面 url。 */
  host: string
  /**
   * 选项卡组自己的名字。
   *
   * 宿主页是 qy 页面时（`/qy/affiliate`），它同时也是**侧栏那一行的标题** ——
   * 侧栏上写「推广佣金」而第一张标签写「推广概览」，是刻意的：前者命名的是
   * 整组，后者命名的是组里的一张表。
   */
  titleKey: string
  /** 标签顺序。可见性仍由 {@link isQyPageVisible} 逐页判定。 */
  pages: readonly string[]
}

export const QY_TAB_GROUPS: readonly QyTabGroupDef[] = [
  {
    // 需求 2：余额划转移进钱包页，选择夹含发起划转 / 划转记录 / 支付密码。
    host: '/wallet',
    titleKey: 'qy_nav_transfer',
    pages: ['/qy/transfer', '/qy/transfer-logs', '/qy/pay-password'],
  },
  {
    // 需求 3：提现移进推广板块，选择夹含概览 / 已邀请用户 / 提现 / 提现记录。
    host: '/qy/affiliate',
    titleKey: 'qy_nav_commission_hub',
    pages: ['/qy/affiliate', '/qy/invitees', '/qy/withdraw', '/qy/withdrawals'],
  },
  {
    // 需求 2（抽奖轮）原话：「抽奖竞猜和我的参与放到一个页面，不要单独写一个
    // 菜单」。三张标签逐字对应那句话，**标签数固定为三**：后续新增玩法
    // （双色球）走活动行上的 `draw_mode` 而不是新标签，导航维度不随数据维度
    // 增长 —— 一个会随配置变多变少的标签栏，用户每次进来看到的东西都不一样。
    host: '/qy/lottery',
    titleKey: 'qy_nav_lottery_hub',
    pages: ['/qy/lottery', '/qy/lottery-guess', '/qy/lottery-records'],
  },
]

/**
 * 该页是否已被收进别人的选项卡组（因而**不该有独立的导航入口**）。
 *
 * 宿主页自己不算：`/qy/affiliate` 是选项卡组的第一张标签，同时仍是侧栏入口。
 */
export function isQyPageHosted(url: string): boolean {
  return QY_TAB_GROUPS.some(
    (group) => group.host !== url && group.pages.includes(url)
  )
}

/**
 * 页面 url → 标签的 hash 片段（不含 `#`）。
 *
 * 纯函数、可逆、无表可漂移：`/qy/transfer-logs` → `qy-transfer-logs`。
 * 旧路由重定向与宿主页选中标签用的是同一个函数，所以不可能对不上。
 */
export function qyTabHash(url: string): string {
  return url.replace(/^\/+/, '').replaceAll('/', '-')
}

/**
 * 页面 url → 现在真正该去的地方。
 *
 * 收进选择夹之后，`navigate({ to: '/qy/transfer-logs' })` 仍然能到 —— 旧路由
 * 会重定向 —— 但那是**先离开宿主页再被弹回来**：整个钱包页会卸载重挂一次，
 * 用户看到的是一次白闪。发起划转成功后跳去划转记录、提交提现后跳去提现记录，
 * 都是这种"跳到自己隔壁那张标签"的场景。
 *
 * 所以动作完成后的跳转一律走这个函数，直接落到宿主页 + 对应 hash。
 * 不在选择夹里的页面原样返回，调用方不需要分支。
 */
export function qyTabTarget(url: string): { to: string; hash?: string } {
  const group = QY_TAB_GROUPS.find((item) => item.pages.includes(url))
  if (group == null) return { to: url }
  return { to: group.host, hash: qyTabHash(url) }
}

// ───────────────────────────── 页面 ─────────────────────────────

/**
 * 站点级「前端是否显示」开关（需求原文：「系统设置前端是否显示」）。
 *
 * 与 {@link QyFeatures} 是**并列**而不是重复：`features.*` 是 YAML 里"这个功能
 * 装没装"（改它要动配置文件），这里是运营在系统设置里随时可关的"这一期要不要
 * 在前台露出入口"。
 *
 * 只对**用户侧**页面生效 —— 管理端入口永远不受它影响，否则关掉之后就再也没有
 * 地方能把它打开了。这条约束在 {@link isQyPageVisible} 里只有一处实现。
 */
export type QyEntrySwitches = {
  lottery: boolean
}

export type QyPageDef = {
  url: string
  /** 侧栏与工作区索引页共用的标题 i18n key。 */
  titleKey: string
  /** 该页依赖的功能开关；`undefined` 表示只要扩展开着就显示。 */
  feature?: keyof QyFeatures
  /**
   * 额外的站点展示开关（见 {@link QyEntrySwitches}）。
   *
   * 与 `feature` 是"且"的关系。管理页写不写都一样 —— 它对管理页恒不生效。
   */
  entry?: keyof QyEntrySwitches
  /**
   * 落点：根侧栏的某个分组，或 {@link QY_SETTINGS_GROUP}（并进系统设置抽屉）。
   *
   * 本页若已被 {@link QY_TAB_GROUPS} 收进别人的选择夹，则**不写**（它没有独立
   * 入口了）。`__tests__/pages-table.test.ts` 双向断言。
   */
  group?: QyNavGroupId
  /**
   * 插到该 url 之后（仅对上游分组有意义）。缺省 = 追加到组尾。
   * 锚点在上游改名/消失时不会吞掉本项，合并函数会兜底追加到组尾。
   */
  after?: string
  /** lucide 图标。折叠项的子项不需要（上游二级菜单本来就不带图标）。 */
  icon?: ElementType
  /**
   * Steins Gate 区段头的日文副标 key（见 `lib/page-meta.ts`）。
   *
   * ── 为什么没有对应的 `enKey` ──
   * 曾经有：侧栏每个 qy 菜单项下面挂一行英文小号大写副标。项目方的反馈是
   * 「新增的二开功能下面你都加了英文，原有的你没有加，好丑，你要统一下」。
   * 统一的方向选了做减法 —— 给上游 20+ 个菜单项编英文副标既要长期维护，
   * 又会在上游增删菜单时漂移回"有的有、有的没有"，正是这次被投诉的形态。
   * 区段头的日文副标留着：它挂在 qy 自己的页面标题上，不与上游菜单并排，
   * 不存在"隔壁那一行没有"的对照。
   */
  jpKey: string
}

/**
 * 页面表。**顺序 = 同一分组内的插入顺序**（折叠项的位置由它第一个子项的
 * 位置决定），与 `lib/page-order.ts` 的编号顺序无关，两者已彻底解耦。
 *
 * `/qy/admin/*` 前缀即"仅管理员可见"，不另设字段 —— 多一个字段就多一处
 * 可能与 url 对不上的地方。
 */
export const QY_PAGES: readonly QyPageDef[] = [
  // ── 上游 general：全体可见的只读数据面板，和仪表盘同类 ──
  {
    url: '/qy/availability',
    titleKey: 'qy_nav_availability',
    feature: 'availability',
    group: 'general',
    after: '/dashboard/models',
    icon: Gauge,
    jpKey: 'qy_sg_jp_availability',
  },

  // ── 钱包页「余额划转」选项卡组（需求 2）──
  // 三页都收进 /wallet 的选择夹，因此**不声明 group / cluster / icon**：
  // 它们已经没有独立的侧栏入口，旧路由只做重定向。
  {
    url: '/qy/transfer',
    titleKey: 'qy_nav_transfer_send',
    feature: 'transfer',
    jpKey: 'qy_sg_jp_transfer',
  },
  {
    url: '/qy/transfer-logs',
    titleKey: 'qy_nav_transfer_logs',
    feature: 'transfer',
    jpKey: 'qy_sg_jp_transfer_logs',
  },
  {
    url: '/qy/pay-password',
    titleKey: 'qy_nav_pay_password',
    feature: 'transfer',
    jpKey: 'qy_sg_jp_pay_password',
  },

  // ── 新组「推广」──
  {
    // 选项卡组的宿主：侧栏那一行写组名「推广佣金」（QY_TAB_GROUPS.titleKey），
    // 组内第一张标签才写本行的 `titleKey`（推广概览）。
    url: '/qy/affiliate',
    titleKey: 'qy_nav_affiliate',
    feature: 'commission',
    group: 'qy-growth',
    icon: Megaphone,
    jpKey: 'qy_sg_jp_affiliate',
  },
  {
    url: '/qy/invitees',
    titleKey: 'qy_nav_invitees',
    feature: 'commission',
    jpKey: 'qy_sg_jp_invitees',
  },
  {
    url: '/qy/withdraw',
    titleKey: 'qy_nav_withdraw',
    feature: 'withdraw',
    jpKey: 'qy_sg_jp_withdraw',
  },
  {
    url: '/qy/withdrawals',
    titleKey: 'qy_nav_withdrawals',
    feature: 'withdraw',
    jpKey: 'qy_sg_jp_withdrawals',
  },
  {
    // 工单落在「推广」组是权宜：它不是推广，但它是**用户主动发起的自助事务**，
    // 与「我的违规」同类（都是"我这边出了状况，去看看/去处理"）。真正的理由是
    // 分组数受控——为一页新建一个组会让侧栏多一行组标题，而这一组当前只有 5 行，
    // 离 6 行的上限还有余量。
    url: '/qy/tickets',
    titleKey: 'qy_nav_tickets',
    feature: 'ticket',
    group: 'qy-growth',
    icon: LifeBuoy,
    jpKey: 'qy_sg_jp_tickets',
  },
  {
    url: '/qy/violations',
    titleKey: 'qy_nav_my_violations',
    feature: 'violation',
    group: 'qy-growth',
    icon: ShieldAlert,
    jpKey: 'qy_sg_jp_violations',
  },
  // 抽奖落在「推广」组：它和邀请返佣一样是拉新与促活的手段，用户的心智
  // 也在同一处（"平台给我的东西在哪"）。三行都挂 `entry: 'lottery'`——
  // 站点在系统设置里关掉展示时，整组消失，而不是留下点进去空空如也的页面。
  //
  // 侧栏上只剩**一行**（组名「抽奖竞猜」= QY_TAB_GROUPS.titleKey），后两行
  // 是选择夹成员：它们不写 group / icon，否则侧栏会多出两行点了就被重定向
  // 甩走的入口（`pages-table.test.ts` 双向断言）。
  {
    url: '/qy/lottery',
    titleKey: 'qy_nav_lottery',
    feature: 'lottery',
    entry: 'lottery',
    group: 'qy-growth',
    icon: Ticket,
    jpKey: 'qy_sg_jp_lottery',
  },
  {
    // 竞猜从大厅的一个筛选值升成一张独立标签：它与抽奖的**资金语义不同**
    // （竞猜可能亏本金、抽奖只是参与费不退），混在同一个列表里靠一个下拉框
    // 区分，用户按上一次的预期下注就会亏。
    url: '/qy/lottery-guess',
    titleKey: 'qy_nav_lottery_guess',
    feature: 'lottery',
    entry: 'lottery',
    jpKey: 'qy_sg_jp_lottery_guess',
  },
  {
    url: '/qy/lottery-records',
    titleKey: 'qy_nav_lottery_records',
    feature: 'lottery',
    entry: 'lottery',
    jpKey: 'qy_sg_jp_lottery_records',
  },

  // ── 上游 admin：各自紧跟语义最近的上游管理项 ──
  {
    url: '/qy/admin/health',
    titleKey: 'qy_nav_a_health',
    group: 'admin',
    after: '/system-info',
    icon: HeartPulse,
    jpKey: 'qy_sg_jp_a_health',
  },

  // ── 新组「结算」：钱怎么流动，管理员视角 ──
  {
    url: '/qy/admin/commission-records',
    titleKey: 'qy_nav_a_commission_records',
    feature: 'commission',
    group: 'qy-settlement',
    icon: ReceiptText,
    jpKey: 'qy_sg_jp_a_commission_records',
  },
  {
    url: '/qy/admin/withdrawals',
    titleKey: 'qy_nav_a_withdrawals',
    feature: 'withdraw',
    group: 'qy-settlement',
    icon: HandCoins,
    jpKey: 'qy_sg_jp_a_withdrawals',
  },
  {
    url: '/qy/admin/transfer-records',
    titleKey: 'qy_nav_a_transfer_records',
    feature: 'transfer',
    group: 'qy-settlement',
    icon: Repeat,
    jpKey: 'qy_sg_jp_a_transfer_records',
  },
  {
    url: '/qy/admin/fund-orders',
    titleKey: 'qy_nav_a_fund_orders',
    group: 'qy-settlement',
    icon: ScrollText,
    jpKey: 'qy_sg_jp_a_fund_orders',
  },
  // 抽奖活动管理落在「结算」组而不是「风控」：这一页每天要看的是本场收入 /
  // 奖品支出 / 待派奖笔数，与佣金结算、提现审核是同一类账，不是风控处置。
  {
    url: '/qy/admin/lottery',
    titleKey: 'qy_nav_a_lottery',
    feature: 'lottery',
    group: 'qy-settlement',
    icon: Ticket,
    jpKey: 'qy_sg_jp_a_lottery',
  },

  // ── 新组「风控与审计」──
  {
    url: '/qy/admin/violations',
    titleKey: 'qy_nav_a_violations',
    feature: 'violation',
    group: 'qy-risk',
    icon: TriangleAlert,
    jpKey: 'qy_sg_jp_a_violations',
  },
  {
    // 工单审核台留在根侧栏而不是进系统设置抽屉：它是**每天要开**的流水页，
    // 抽屉里放的是"改一次影响后续每一笔"的配置。界线见本文件顶部的说明。
    url: '/qy/admin/tickets',
    titleKey: 'qy_nav_a_tickets',
    feature: 'ticket',
    group: 'qy-risk',
    icon: LifeBuoy,
    jpKey: 'qy_sg_jp_a_tickets',
  },
  {
    url: '/qy/admin/audit-logs',
    titleKey: 'qy_nav_a_audit_logs',
    group: 'qy-risk',
    icon: ClipboardList,
    jpKey: 'qy_sg_jp_a_audit_logs',
  },

  // ── 并进上游「系统设置」抽屉（需求 8）──
  // 全是"改一次影响后续每一笔"的配置项：佣金比例、划转额度与分组限制、
  // 分组定价、新用户默认分组、违规判定规则。它们与上游的费率/风控设置同类，
  // 打开频率也一样低。审核与流水页（佣金审核 / 提现审核 / 划转流水 / 资金对账 /
  // 违规记录 / 审计留存）刻意**留在根侧栏**：那些是每天要开的，埋进抽屉里更难找。
  // 顺序 = 抽屉里那一组的显示顺序。
  {
    url: '/qy/admin/commission',
    titleKey: 'qy_nav_a_commission',
    feature: 'commission',
    jpKey: 'qy_sg_jp_a_commission',
    group: QY_SETTINGS_GROUP,
  },
  {
    url: '/qy/admin/transfer-config',
    titleKey: 'qy_nav_a_transfer_config',
    feature: 'transfer',
    jpKey: 'qy_sg_jp_a_transfer_config',
    group: QY_SETTINGS_GROUP,
  },
  {
    url: '/qy/admin/transfer-group-rules',
    titleKey: 'qy_nav_a_transfer_group_rules',
    feature: 'transfer',
    jpKey: 'qy_sg_jp_a_transfer_group_rules',
    group: QY_SETTINGS_GROUP,
  },
  // 「分组定价」已下线：同一模型分组内的逐模型定价能力被收敛成
  // (用户分组, 模型分组) 一个标量，由「用户分组」页一处表达。
  //
  // 「分组矩阵」（`/qy/admin/group-matrix`）也已从本表移出：日常配置整体搬进了
  // 「计费与支付 → 用户分组」表行内的编辑弹窗（见
  // `features/system-settings/billing/section-registry.tsx`）。两个页面读写同一组
  // 数据、同一组端点，并存两个**平级**入口就意味着两份互不知情的草稿与两道各自
  // 为政的保存闸门，而写入是两库不原子的。
  //
  // 它本身仍然活着，定位是**高级 / 诊断视图**：整列批量、跨档对比、孤儿令牌基线
  // 这三件事在单档弹窗里表达不出来，而它们只在这一页上。入口是「用户分组」表下方
  // 那条「高级」链接（`user-groups-section.tsx`）—— 不在导航表里再占一项（那正是
  // 项目方说的"一团糟"），但也绝不能一个链接都没有：那等于把这三件能力对不看代码
  // 的人删掉。
  // 「新用户分组」（`/qy/admin/user-group`）也已从本表移出。它整页只有一个下拉
  // ——「新注册用户落进哪个分组」——而那个下拉选的东西与「用户分组」页两张表列的
  // 是同一批名字。项目方原话：「当前为何要有 2 个用户分组？只保留一个新的即可，
  // 旧的这个移除掉。」现在它是那一页上的一张卡片
  // （`features/qy/pages/admin-user-groups/default-group`），后端模块与端点
  // （`PUT /api/qy/admin/user-group/config`）原样不动。
  //
  // 与分组矩阵那次搬家不同，这一次**路由文件也一并删掉**，没有留重定向。代价有
  // 两条，都是有意付的：① 旧书签直接 404；② 新家在 `/system-settings` 下要求
  // role=100，而这一项的后端一直是 `AdminAuth`（role>=10），所以**普通管理员从此
  // 在前端没有入口配置新用户默认分组**。分组矩阵那条路由用「够得着新家的送过去、
  // 够不着的原地渲染」躲开了同一个坑，这里没有照做是因为项目方点名要删的就是
  // 这个页面本身。要恢复那条能力，正确的做法是把新家的准入下调到 role>=10，
  // 而不是把这一页再建回来。
  {
    url: '/qy/admin/violation-rules',
    titleKey: 'qy_nav_a_violation_rules',
    feature: 'violation',
    jpKey: 'qy_sg_jp_a_violation_rules',
    group: QY_SETTINGS_GROUP,
  },
  // 需求原文：「系统设置前端是否显示」。它与手续费上限、单场奖品上限一样，
  // 改一次影响之后每一场，属于抽屉里那类配置，而不是每天要看的流水。
  {
    url: '/qy/admin/lottery-config',
    titleKey: 'qy_nav_a_lottery_config',
    feature: 'lottery',
    jpKey: 'qy_sg_jp_a_lottery_config',
    group: QY_SETTINGS_GROUP,
  },
  // 可选 API 地址簿。改一次影响之后每一个用户复制出来的连接信息，与费率、
  // 门槛同类，属于抽屉里那批配置而不是每天要看的流水。
  // 不挂 `feature`：它没有 YAML 开关（见 `qianye/modules/apiaddr` 的说明），
  // 扩展开着就有 —— 而且它的"关掉"就是一条地址都不配，那时用户侧自动回落
  // 到站点地址，与本功能上线之前完全一致。
  {
    url: '/qy/admin/api-address',
    titleKey: 'qy_nav_a_api_address',
    jpKey: 'qy_sg_jp_a_api_address',
    group: QY_SETTINGS_GROUP,
  },
]

/**
 * 两个工作区索引页（`/qy`、`/qy/admin`）。
 *
 * 它们不是功能页而是分组入口，所以不占 `LAB MEMO` 编号（`qyPageMeta` 给 `00`），
 * 也不出现在侧边栏里 —— 重排之后 23 个页面各自挂在语义相关的上游分组下，
 * 侧栏不再需要"先进工作区再选功能"这一跳。它们仍是有效路由，Steins Gate
 * 主题下渲染「機能一覧」总览（`home/index.tsx`）。
 */
export const QY_INDEX_PAGES: readonly { url: string; jpKey: string }[] = [
  { url: '/qy', jpKey: 'qy_sg_jp_workspace' },
  { url: '/qy/admin', jpKey: 'qy_sg_jp_admin_workspace' },
]

/**
 * 并进系统设置抽屉的那几页，按 {@link QY_PAGES} 的声明顺序。
 *
 * 派生而不是再抄一份 url 清单：`system-settings.ts` 要用它生成菜单项，
 * 还要用它生成 drill-in 视图的 `pathPattern`，两处各抄一份必然漂移成
 * "菜单里点得到、点进去侧栏却掉回根导航"。
 */
export const QY_SETTINGS_PAGES: readonly QyPageDef[] = QY_PAGES.filter(
  (page) => page.group === QY_SETTINGS_GROUP
)

/** `/qy/admin/*` 前缀即管理页。 */
export function isQyAdminPage(url: string): boolean {
  return url.startsWith('/qy/admin/')
}

/**
 * 该页在当前功能开关与角色下是否应该出现在导航里。
 *
 * 只判定"能不能看见入口"；真正的访问控制在路由 guard 与后端，二者独立。
 *
 * `entries` 省略时展示开关一律按"显示"处理：配置还在取数、或调用方压根不关心
 * 展示开关（系统设置抽屉里全是管理页）时，不该把菜单先抹掉再长回来。
 */
export function isQyPageVisible(
  page: QyPageDef,
  features: QyFeatures,
  isAdmin: boolean,
  entries?: QyEntrySwitches
): boolean {
  if (isQyAdminPage(page.url) && !isAdmin) return false
  if (page.feature != null && !features[page.feature]) return false
  // 展示开关只压用户侧：管理端是把它重新打开的唯一地方。
  if (page.entry != null && !isQyAdminPage(page.url) && entries != null) {
    return entries[page.entry]
  }
  return true
}

/**
 * 当前应当**拥有自己那一行入口**的页面。
 *
 * 侧栏（`nav.ts` 的 `mergeQyNavGroups`）与工作区索引页
 * （`getQyWorkspaceNavGroups`）都走这一个函数，因为"哪些页面还算独立入口"
 * 必须只有一处判定：两边各写一遍 filter 的话，收进选择夹的页面会在其中一边
 * 留下一行点了就被重定向甩走的死链 —— 那正是本仓反复出现的断链形状。
 *
 * 返回顺序 = `QY_PAGES` 的声明顺序；索引页另按 `LAB MEMO` 编号重排。
 */
export function qyEntryPages(
  features: QyFeatures,
  isAdmin: boolean,
  entries?: QyEntrySwitches
): QyPageDef[] {
  return QY_PAGES.filter(
    (page) =>
      isQyPageVisible(page, features, isAdmin, entries) &&
      !isQyPageHosted(page.url)
  )
}
