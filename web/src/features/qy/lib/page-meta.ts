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
import { QY_PAGE_URL_ORDER } from './page-order'
import { QY_INDEX_PAGES, QY_PAGES } from './pages'

/**
 * Midnight Signal 区段头需要的两样每页文案（口径见
 * `qianye/docs/design-14-midnight-signal.md` §6）：
 *   - 序号 `GATE NN`：取自 {@link QY_PAGE_URL_ORDER}，那是一张**冻结的
 *     字面量表**，与侧边栏怎么排列完全无关（见 `lib/page-order.ts` 开头对
 *     "为什么解耦"的说明）。加页面时往那张表末尾追加一行即可；
 *   - 页面代号：参考稿登机牌语言里的大写英文戳记，装饰性字形，但必须**贴合该页
 *     的实际功能**（提现 →「WITHDRAW · REQUEST」），所以只能一页一条地写。
 *     走 i18n（`src/i18n/qy/{en,zh}.json`）而不是硬编码，以便站点自行调整；
 *     en/zh 两份内容相同——它本来就是拉丁装饰字形，不是被翻译的对象。
 *
 * 映射表由 `lib/pages.ts` 派生：页面代号不再单独维护一份 url→key 表，
 * 避免"同一份页面清单散在多个文件里各自漂移"。
 *
 * 两个工作区索引页不在编号表里（它们是分组入口而不是功能页），给 `00`。
 */
const PAGE_CODE_KEY: Readonly<Record<string, string>> = Object.fromEntries(
  [...QY_INDEX_PAGES, ...QY_PAGES].map((page) => [page.url, page.codeKey])
)

export type QyPageMeta = {
  /** 已补零的两位序号，直接拼进 `GATE {no}`。 */
  no: string
  /** 页面代号的 i18n key；未登记的路径为 `null`，此时区段头只显示序号与标题。 */
  codeKey: string | null
}

/**
 * 把 pathname 归一化到登记过的页面 URL。
 *
 * 取**最长前缀**而不是精确相等：详情页/子标签页（`/qy/admin/violations/123`）
 * 应当继承所属功能页的编号，而不是掉进"未登记"分支。尾部斜杠先去掉，
 * 否则 `/qy/withdraw/` 会匹配不上。
 */
export function qyPageMeta(pathname: string): QyPageMeta {
  const path = pathname.length > 1 ? pathname.replace(/\/+$/, '') : pathname

  let best = ''
  for (const url of Object.keys(PAGE_CODE_KEY)) {
    if (path !== url && !path.startsWith(`${url}/`)) continue
    if (url.length > best.length) best = url
  }
  if (best === '') return { no: '00', codeKey: null }

  const index = QY_PAGE_URL_ORDER.indexOf(best)
  return {
    // 索引页（不在编号表里）与未登记页面统一记 00。
    no: String(index + 1).padStart(2, '0'),
    codeKey: PAGE_CODE_KEY[best] ?? null,
  }
}
