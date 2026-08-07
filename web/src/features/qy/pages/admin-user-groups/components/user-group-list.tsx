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
import { Search, Users } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Input } from '@/components/ui/input'
import { cn } from '@/lib/utils'

import { QyGmScopeStateBadges } from '../../admin-group-matrix/components/scope-state-badges'
import type { QyGmUserGroup } from '../../admin-group-matrix/types'
import { formatQyCount } from '../../ops/format'

type QyUgUserGroupListProps = {
  /** 已按搜索词筛过的清单。筛选本身在 `lib/rows.ts`，与后端同一套归一口径。 */
  groups: readonly QyGmUserGroup[]
  /** 未筛选前的总数 —— 「搜不到」与「一个分组都没有」必须能分开。 */
  totalCount: number
  selected: string | null
  onSelect: (userGroup: string) => void
  search: string
  onSearchChange: (value: string) => void
  /** 分组名 → 草稿合并后在范围内的模型分组数。徽章与右侧共用同一个数。 */
  grantedCounts: ReadonlyMap<string, number>
  /** 该分组在本次草稿里被动过（勾选或改价）。列表上必须看得见。 */
  dirtyGroups: ReadonlySet<string>
}

/**
 * 左列表：站点上有哪些**用户分组**。
 *
 * ── 为什么这一列不提供「新建 / 重命名 / 删除」──
 *
 * 用户分组不是一张实体表，它是 `options.GroupRatio` 的键集合，而**同一个键集合
 * 同时也是模型分组清单**（方案 3 的已知代价）。于是：
 *
 *   · 「新建用户分组」= 往那张表加一个键 = 同时凭空造出一个模型分组。在一个
 *     专门用来区分这两个概念的界面上提供这个按钮，会让本页说的每一句话当场失效；
 *     而那个动作在「模型分组定价」里已经有唯一入口，再开第二个写入口，两处对
 *     同一份 `options` 的并发写会互相覆盖且无人察觉。
 *   · 「重命名」在数据层根本不存在：`users.group` / `tokens.group` /
 *     `channels.group` / `qy_group_grants` / `GroupGroupRatio` 各存一份字符串，
 *     改名要同时重写五处，任何一处漏掉的表现都是「一批账号挂在一个不存在的
 *     分组上，请求按 1.0 兜底扣费」，且静默。
 *   · 「删除」同理 —— 删掉键之后仍挂在这一档的用户不会消失，只会失去倍率。
 *
 * 所以这里给的是一条**指路链接**，把这三件事留在它们真正的归属地，并在那里
 * 承担它们的全部语义。这与 `admin-group-matrix/components/axis-legend.tsx` 是
 * 同一条决定，两处措辞刻意一致。
 */
export function QyUgUserGroupList(props: QyUgUserGroupListProps) {
  const { t } = useTranslation()

  return (
    <div className='flex min-h-0 flex-col gap-2 rounded-lg border p-2'>
      <div className='space-y-1 px-1'>
        <div className='flex items-center gap-1.5 text-xs font-medium'>
          <Users aria-hidden='true' className='size-3.5 shrink-0' />
          {t('qy_group_matrix_row_header')}
        </div>
        <p className='text-muted-foreground text-xs'>
          {t('qy_group_matrix_axis_row_hint')}
        </p>
      </div>

      <div className='relative'>
        <Search
          aria-hidden='true'
          className='text-muted-foreground pointer-events-none absolute start-2 top-1/2 size-3.5 -translate-y-1/2'
        />
        <Input
          value={props.search}
          onChange={(event) => props.onSearchChange(event.target.value)}
          aria-label={t('qy_common_search')}
          placeholder={t('qy_common_search')}
          className='h-8 ps-7 text-xs'
        />
      </div>

      {/*
        `max-h` + `overflow-y-auto`：站里分组数还小，但这一列与右侧详情是并排的，
        列表无限长会把详情面板顶到屏幕外，运营要滚很久才回得到正在编辑的格子。
      */}
      <ul className='max-h-[52vh] min-h-0 space-y-1 overflow-y-auto'>
        {props.groups.map((group) => {
          const active = group.name === props.selected
          return (
            <li key={group.name}>
              <button
                type='button'
                onClick={() => props.onSelect(group.name)}
                aria-current={active ? 'true' : undefined}
                title={t('qy_group_matrix_row_label', {
                  userGroup: group.name,
                })}
                className={cn(
                  'focus-visible:ring-ring w-full rounded-md border p-2 text-start focus-visible:ring-2 focus-visible:outline-none',
                  active
                    ? 'border-primary bg-primary/5'
                    : 'hover:bg-muted/50 border-transparent',
                  // 草稿标记与矩阵格子同一个 `border-warning`：同一件事在两个
                  // 视图里必须长同一个样，否则切一次标签就得重新学一遍配色。
                  props.dirtyGroups.has(group.name) && 'border-warning'
                )}
              >
                <div className='flex flex-wrap items-center gap-1.5'>
                  <span className='min-w-0 truncate text-xs font-medium'>
                    {group.name}
                  </span>
                  <QyGmScopeStateBadges
                    userGroup={group}
                    grantedCount={props.grantedCounts.get(group.name) ?? 0}
                  />
                </div>
                <div className='text-muted-foreground text-[10px] tabular-nums'>
                  {t('qy_group_matrix_row_stats', {
                    users: formatQyCount(group.user_count),
                    tokens: formatQyCount(group.active_token_count),
                  })}
                </div>
              </button>
            </li>
          )
        })}
      </ul>

      {/*
        这里的空态**只可能是搜索词造成的** —— 「站点一个用户分组都没有」由页面
        级的空态承担（`QyPageBoundary`），那一档要指路去创建。两者措辞必须不同：
        把「搜不到」也画成「去创建分组」，运营会真的去建一个本来就存在、只是他
        打错了一个字母的分组，而新建一个分组同时也会造出一个模型分组。
      */}
      {props.groups.length > 0 || props.totalCount === 0 ? null : (
        <p className='text-muted-foreground px-1 text-xs'>
          {t('qy_tr_a_empty_desc')}
        </p>
      )}

      <p className='text-muted-foreground border-t px-1 pt-2 text-xs'>
        {t('qy_group_matrix_axis_create_hint')}{' '}
        <Link
          to='/system-settings/billing/$section'
          params={{ section: 'group-pricing' }}
          className='text-primary underline underline-offset-2'
        >
          {t('qy_group_matrix_axis_create_link')}
        </Link>
      </p>
    </div>
  )
}
