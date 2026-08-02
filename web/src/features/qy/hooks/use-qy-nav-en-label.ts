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
import { useTranslation } from 'react-i18next'

import { QY_PAGES } from '../lib/pages'
import { useQyIsSteinsGate } from './use-qy-theme-preset'

/**
 * 侧栏项的英文副标（design-11 §1.3）。
 *
 * 图里最强的识别特征是「中文大字 + 下方英文小号大写宽字距」成对出现。
 * 它是**装饰性排版元素而不是翻译**，所以：
 *   - `src/i18n/qy/{en,zh}.json` 两份都填同样的英文（与已有的
 *     `qy_sg_jp_*` 日文副标同一套路，见 `lib/page-meta.ts`）；
 *   - 渲染时 `aria-hidden`，读屏软件只念中文主标题，不会把同一项念两遍。
 *
 * 文案取各页真实功能的英文名（`qy_nav_*` 在 en.json 里的既有译名的短式），
 * 不生造词：提现 → WITHDRAWAL、可用率 → AVAILABILITY、划转 → TRANSFER。
 *
 * ── 为什么按 url 索引而不是按标题 ──
 * 渲染侧栏的 `components/layout/components/nav-group.tsx` 是通用组件，
 * 拿到的 `item.title` 已经是**翻译后的字符串**（中文环境下是中文），
 * 反查 i18n key 不可能；`item.url` 才是稳定标识。这与 `lib/page-meta.ts`
 * 给区段头取日文副标的做法一致。
 *
 * ── 映射表从 `lib/pages.ts` 派生 ──
 * 曾经是本文件里的第三份 url→文案硬编码表，与 `nav.ts`、`page-meta.ts` 的
 * 两份并列，加页面时必然漏掉其中一份。
 *
 * ── 已知边界：折叠项内的二级页面拿不到副标 ──
 * `nav-group.tsx` 只在 `SidebarMenuLink`（一级链接）里调用本 hook，
 * `SidebarMenuSubItem` 与 `SidebarMenuCollapsedDropdown` 没有接。所以
 * 收进折叠项的 6 个页面（划转/划转记录、提现申请/提现记录、划转门槛配置/
 * 划转分组限制）在 Steins Gate 下只显示中文 —— 与上游 system-settings 的
 * 二级菜单一致（那里同样没有副标），视觉上不突兀。它们的 `enKey` 仍然登记
 * 在表里而不是删掉：一旦这几页改回一级项，副标自动回来，不需要再补数据。
 * `__tests__/pages-table.test.ts` 里 `renders an en subtitle only for
 * top-level pages` 一条把"当前谁有副标"钉死，改动折叠结构时会红。
 */
const EN_LABEL_KEY: Readonly<Record<string, string>> = Object.fromEntries(
  QY_PAGES.map((page) => [page.url, page.enKey])
)

/**
 * 返回该导航项的英文副标；非 Steins Gate 预设、或该 url 未登记时返回 `null`
 * （调用方据此**完全不渲染**那个节点，其它预设下 DOM 与改造前逐字节一致）。
 *
 * 预设的判定复用 {@link useQyIsSteinsGate}：它读 `<body[data-theme-preset]>`，
 * 与 CSS 认的是同一个东西，站点主题管理页的实时预览也能同步。
 */
export function useQyNavEnLabel(url: string | undefined): string | null {
  const isSteinsGate = useQyIsSteinsGate()
  const { t } = useTranslation()

  if (!isSteinsGate || !url) return null
  const key = EN_LABEL_KEY[url]
  return key ? t(key) : null
}
