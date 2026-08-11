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
 * `LAB MEMO — NN` 序号的唯一真源。
 *
 * ── 为什么是一张冻结的字面量表，而不是从导航结构派生 ──
 * 序号原本取自 `nav.ts` 里页面的声明顺序。那等于把「实验记录编号」这个**对外
 * 可见的稳定标识**绑死在「侧边栏怎么排」这个**随时会调整的展示决定**上：这次
 * 把 23 个页面按语义拆进上游分组，声明顺序必然重排，23 个编号会集体错位 ——
 * 用户昨天记下的 `LAB MEMO — 14` 今天指向另一个页面。
 *
 * 所以这里把编号显式冻结成字面量，导航怎么重排都不影响它。
 *
 * ── 维护规则 ──
 *   1. **新页面一律追加到末尾**，取下一个空号；
 *   2. **不要重排、不要插空**，中间的页面下线时保留空洞或直接删行都会让后面
 *      所有页面移位 —— 若真要下线一个页面，删行是可接受的（历史编号本就随
 *      页面消失），但绝不允许为了"好看"重新排序；
 *   3. 本表与 `lib/pages.ts` 的 url 集合必须完全一致，由
 *      `__tests__/page-order.test.ts` 双向断言（少一个 = 新页面忘了登记编号，
 *      多一个 = 页面已删但编号残留）。
 */
export const QY_PAGE_URL_ORDER: readonly string[] = [
  '/qy/affiliate',
  '/qy/invitees',
  '/qy/transfer',
  '/qy/transfer-logs',
  '/qy/pay-password',
  '/qy/withdraw',
  '/qy/withdrawals',
  '/qy/violations',
  '/qy/availability',
  '/qy/admin/commission',
  '/qy/admin/commission-records',
  '/qy/admin/transfer-records',
  '/qy/admin/transfer-group-rules',
  '/qy/admin/transfer-config',
  '/qy/admin/withdrawals',
  '/qy/admin/violation-rules',
  '/qy/admin/violations',
  // `/qy/admin/user-group` 曾占本表的第 18 号。它整页只有一个下拉，已经降级成
  // 「系统设置 → 计费与支付 → 用户分组」上的一张卡片，不再是本表登记的页面，
  // 因此按维护规则 2 删行 —— 它是**末尾之前**的一行，后面三页（资金订单、
  // 审计日志、健康检查）各前移一号。这是删行不可避免的代价，规则 2 已经把它
  // 说成可接受（历史编号本就随页面消失）；被动的三页都是管理端页面，
  // `LAB MEMO` 编号在那几页上是装饰，不像用户侧页面那样会被记下来引用。
  '/qy/admin/fund-orders',
  '/qy/admin/audit-logs',
  '/qy/admin/health',
  '/qy/lottery',
  '/qy/lottery-records',
  '/qy/admin/lottery',
  '/qy/admin/lottery-config',
  '/qy/admin/api-address',
  '/qy/tickets',
  '/qy/admin/tickets',
  // `/qy/admin/group-matrix` 曾占本表的第 29 号。它整体搬进了上游抽屉的
  // 「计费与支付 → 用户分组」，不再是本表登记的页面，因此按维护规则 2 删行 ——
  // 它是**末尾之前**的一行，删掉会让 `/qy/lottery-guess` 从 30 号变成 29 号。
  // 这是可接受的：`LAB MEMO` 编号跟随页面消失，而竞猜页是本轮同批新增的，
  // 还没有用户记下过它的号；重排既有编号仍然禁止。
  // 竞猜从大厅的一个筛选值升成独立页面（需求 2 的选择夹第二张标签）。
  // 按本表的维护规则**追加到末尾取下一个空号**，不与 `/qy/lottery`、
  // `/qy/lottery-records` 排在一起 —— 那两页的编号是用户已经记下的。
  '/qy/lottery-guess',
  // 佣金余额与 AFF 关系此前**没有**登记进本表：它们靠 `page-meta.ts` 的最长前缀
  // 规则继承了 `/qy/admin/commission-records` 的编号（11 号）。现在它们是侧栏上
  // 各自独立的一行，就该有自己的号 —— 按维护规则 1 追加到末尾取空号，既有编号
  // 一个都不动。
  '/qy/admin/commission-records/balances',
  '/qy/admin/commission-records/relations',
  // 违规类型（需求：类型可增删改、规则绑到类型、类型计次触发处置、用户端公示）。
  // 按本表的维护规则**追加到末尾取下一个空号**，绝不插到
  // `/qy/admin/violation-rules` 后面 —— 那会让它之后的每一页集体错位。
  '/qy/admin/violation-categories',
  // AI 内容审核(需求:随机抽样 + 可配审核渠道 + 转发前/后两个时机)。
  // 同样**追加到末尾取下一个空号**,不与 violation-rules / violation-categories
  // 排在一起 —— 那两页的编号已经发出去了,重排会让用户记下的 LAB MEMO 指向别处。
  '/qy/admin/violation-ai-review',
  // 用户佣金（一行 = 一个用户）。它是佣金管理选择夹的宿主，另外两张标签
  // （AFF 关系 / 佣金余额）的编号原地不动 —— 被收进选择夹不改变"它们各自
  // 是一个页面"这件事，`page-meta.ts` 仍然按最长前缀把它们认出来。
  // 按维护规则 1 追加到末尾取空号，既有编号一个都不动。
  '/qy/admin/commission-records/users',
]
