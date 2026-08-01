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
import { QY_PAGE_URL_ORDER } from '../nav'

/**
 * Steins Gate 区段头需要的两样每页文案：
 *   - 序号 `LAB MEMO — NN`：由页面在 {@link QY_PAGE_URL_ORDER} 里的位置自动生成，
 *     不用手工维护，加页面时只要往 `nav.ts` 里加一行就自动获得下一个号；
 *   - 日文副标：装饰性字形，但必须**贴合该页的实际功能**（提现 →「送金申請」），
 *     所以只能一页一条地写。走 i18n（`src/i18n/qy/{en,zh}.json`）而不是硬编码，
 *     以便站点自行调整；en/zh 两份内容相同——它本来就是日文，不是被翻译的对象。
 *
 * 两个工作区首页不在导航页面列表里（它们是分组入口而不是功能页），给 `00`。
 */
const JP_SUBTITLE_KEY: Readonly<Record<string, string>> = {
  '/qy': 'qy_sg_jp_workspace',
  '/qy/admin': 'qy_sg_jp_admin_workspace',
  '/qy/affiliate': 'qy_sg_jp_affiliate',
  '/qy/invitees': 'qy_sg_jp_invitees',
  '/qy/transfer': 'qy_sg_jp_transfer',
  '/qy/transfer-logs': 'qy_sg_jp_transfer_logs',
  '/qy/pay-password': 'qy_sg_jp_pay_password',
  '/qy/withdraw': 'qy_sg_jp_withdraw',
  '/qy/withdrawals': 'qy_sg_jp_withdrawals',
  '/qy/violations': 'qy_sg_jp_violations',
  '/qy/availability': 'qy_sg_jp_availability',
  '/qy/admin/commission': 'qy_sg_jp_a_commission',
  '/qy/admin/commission-records': 'qy_sg_jp_a_commission_records',
  '/qy/admin/transfer-records': 'qy_sg_jp_a_transfer_records',
  '/qy/admin/transfer-group-rules': 'qy_sg_jp_a_transfer_group_rules',
  '/qy/admin/transfer-config': 'qy_sg_jp_a_transfer_config',
  '/qy/admin/withdrawals': 'qy_sg_jp_a_withdrawals',
  '/qy/admin/violation-rules': 'qy_sg_jp_a_violation_rules',
  '/qy/admin/violations': 'qy_sg_jp_a_violations',
  '/qy/admin/user-group': 'qy_sg_jp_a_user_group',
  '/qy/admin/group-pricing': 'qy_sg_jp_a_group_pricing',
  '/qy/admin/site-theme': 'qy_sg_jp_a_site_theme',
  '/qy/admin/fund-orders': 'qy_sg_jp_a_fund_orders',
  '/qy/admin/audit-logs': 'qy_sg_jp_a_audit_logs',
  '/qy/admin/health': 'qy_sg_jp_a_health',
}

export type QyPageMeta = {
  /** 已补零的两位序号，直接拼进 `LAB MEMO — {no}`。 */
  no: string
  /** 日文副标的 i18n key；未登记的路径为 `null`，此时区段头只显示序号与标题。 */
  jpKey: string | null
}

/**
 * 把 pathname 归一化到导航里登记的页面 URL。
 *
 * 取**最长前缀**而不是精确相等：详情页/子标签页（`/qy/admin/violations/123`）
 * 应当继承所属功能页的编号，而不是掉进"未登记"分支。尾部斜杠先去掉，
 * 否则 `/qy/withdraw/` 会匹配不上。
 */
export function qyPageMeta(pathname: string): QyPageMeta {
  const path = pathname.length > 1 ? pathname.replace(/\/+$/, '') : pathname

  let best = ''
  for (const url of Object.keys(JP_SUBTITLE_KEY)) {
    if (path !== url && !path.startsWith(`${url}/`)) continue
    if (url.length > best.length) best = url
  }
  if (best === '') return { no: '00', jpKey: null }

  const index = QY_PAGE_URL_ORDER.indexOf(best)
  return {
    // 首页（不在页面列表里）与未登记页面统一记 00。
    no: String(index + 1).padStart(2, '0'),
    jpKey: JP_SUBTITLE_KEY[best] ?? null,
  }
}
