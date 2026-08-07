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
import { Link } from '@tanstack/react-router'
import { ArrowRight } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'

import { useQyConfig } from '../hooks/use-qy-config'

/**
 * 上游「计费与支付 → 模型分组」页上的一块指路牌。
 *
 * ── 它替代了什么 ──
 *
 * 拆页之前那一页（旧 id `group-pricing`）有两段编辑 UI，现在都拆掉了：
 *
 *  - `GroupGroupRatio`（分组间倍率覆盖：可视化的「特殊倍率规则」卡片 + 生 JSON 框）
 *  - `GroupSpecialUsableGroup`（特殊可用分组规则：可视化编辑器 + 生 JSON 框）
 *
 * 两者的去向**不一样**，这块指路牌必须把它们分开说：
 *
 *  - 倍率搬家了：现在是「计费与支付 → 用户分组可用的模型分组配置」里那张
 *    「用户分组 × 模型分组」表格子里的值，**数据仍在原处、仍在实时参与计费**。
 *  - 特殊可用分组规则**整套下线了**：它从来没有真正生效过（上游在差分算完之后
 *    无条件把用户分组自己补回去，把唯一有意义的 `-:自己` 恒抵消掉）。
 *    「哪一档人能选哪些模型分组」现在由用户分组的**范围**独家回答。
 *
 * 把"下线"说成"搬家"会让运营去新页面找一批已经不存在的规则，那与找不到入口
 * 一样糟 —— 只是浪费的时间更长。
 *
 * ── 为什么必须留这一块，而不是删干净了事 ──
 *
 * 一个配置项从界面上消失、而它管的事情还在生效，正是这个仓库反复栽的那个形状：
 * 运营找不到入口就会认定"功能没了"，然后要么绕路、要么把整套方案判死。所以
 * 删掉编辑框的同一次改动里，必须在原地留下**去哪儿改**。
 *
 * ── 判据与「用户分组可用的模型分组配置」那一项保持同源 ──
 *
 * 链接指向的 section 由 `withQyBillingSectionNavItems` 按「扩展是否启用」这一个
 * 条件决定去留，所以这里用同一个判据（而不是再叠一层 `group_matrix` 模块开关）：
 * 两处判据不同的直接后果，是指路牌指向一个已经从抽屉里摘掉的菜单项。
 * `unknown`（取数中 / 503）时不渲染 —— 宁可少说一句，也不要在扩展其实没开的
 * 站点上指一条空路。
 *
 * 代价说清楚：扩展关掉时这一页既没有那两段编辑框、也没有这块指路牌。本 fork
 * 始终启用扩展，所以这是纸面上的组合而不是线上状态；真要支持它，正确做法是把
 * 两段编辑 UI 按同一个判据条件渲染回来，而不是在这里挂一条指向空页的链接。
 */
export function QyGroupMatrixHint() {
  const { t } = useTranslation()
  const config = useQyConfig()

  if (config.status !== 'enabled') return null

  // 文案写成 `t('…')` 字面量而不是查表：`lib/__tests__/i18n-key-coverage.test.ts`
  // 只扫这一种形式，查表得到的键它看不见，会被当成「零引用」删掉，
  // 而删掉的直接后果是上游那一页上出现一行裸键名。
  //
  // 链接文案刻意复用 `qy_gs_group_matrix_title`（「用户分组可用的模型分组配置」）
  // —— 与 `billing/section-registry.tsx` 里那一项的 titleKey 是同一个键，指路牌上
  // 的名字与侧栏上要点的那一项因此不可能对不上。拆页之后这里**不能**再用
  // `qy_group_matrix_row_header`（「用户分组」）：那已经是另一页的名字了。
  //
  // 正文用 `qy_group_pricing_moved_desc` 而不是矩阵页那句 `qy_group_scope_matrix_desc`：
  // 后者里的「本页不做限制」说的是矩阵所在的那一页，渲染在这一页上恰好指着这一页
  // 自己摆着的那份全局「用户可选分组」清单，把「谁限制谁」说反了；而且它通篇没有
  // 出现被拆掉的那两个控件的名字，运营按名字找东西时一个都对不上。这一句必须
  // **指名道姓**点出「分组间覆盖」与「特殊可用分组规则」各自去了哪里。
  return (
    <Alert>
      <ArrowRight className='h-4 w-4' />
      <AlertTitle>
        <Link
          to='/system-settings/billing/$section'
          params={{ section: 'group-matrix' }}
          className='text-primary underline underline-offset-2'
        >
          {t('qy_gs_group_matrix_title')}
        </Link>
      </AlertTitle>
      <AlertDescription>{t('qy_group_pricing_moved_desc')}</AlertDescription>
    </Alert>
  )
}
