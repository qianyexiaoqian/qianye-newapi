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
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { Info, SlidersHorizontal } from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table/static/static-data-table'
import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Input } from '@/components/ui/input'
import { qyGmMatrixQuery } from '@/features/qy/pages/admin-group-matrix/api'

import { SettingsSwitchField } from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { GroupOptionsJsonDrawer } from './components/group-options-json-drawer'
import { GroupPricingGuideButton } from './components/group-pricing-guide'
import {
  buildUserGroupRows,
  nextRowId,
  parseAutoGroups,
  serializeTopupRatios,
  type UserGroupRow,
  type USER_GROUP_PAGE_KEYS,
} from './lib/group-options'
import { useGroupOptionSave } from './lib/use-group-option-save'

export type UserGroupsSectionValues = {
  TopupGroupRatio: string
  DefaultUseAutoGroup: boolean
  /** 只读：这一页只拿它的**外层键**当用户分组的证据，一个字节都不写回。 */
  GroupGroupRatio: string
  /** 只读：用来判断「auto 清单是不是空的」。编辑在「模型分组」页。 */
  AutoGroups: string
}

/**
 * A「用户分组」页。
 *
 * ── 这一页的主语是「一档人」──
 *
 * 上游把 8 个配置项摊在同一张表上，其中 `TopupGroupRatio` 的键是**用户分组**、
 * 其余几乎全是**模型分组**，两种键混在同一行里。项目方点名的就是这处错位：
 * 「充值折扣」摆在一张模型分组表上，运营会以为给某个渠道池配了折扣。
 *
 * 所以这一页只回答关于「一档人」的问题：
 *   · 站上有哪些用户分组、每一档多少人；
 *   · 这一档充值按几折（`TopupGroupRatio`）；
 *   · 这一档能用哪些模型分组（只读摘要，编辑在另一页）。
 *
 * ── 为什么这里不能新建 / 改名 / 删除用户分组 ──
 *
 * 用户分组不是一张实体表，它是 `users.group` 上的字符串。「新建」在数据层
 * 无处可写（真正的写入发生在给某个用户改分组的时候），「改名」要同时重写
 * `users.group` / `GroupGroupRatio` 外层键 / `TopupGroupRatio` 键 / 范围表 /
 * 套餐升降级分组五处，任何一处漏掉的表现都是「一批账号挂在一个不存在的分组上，
 * 按 1.0 兜底扣费」且静默。在这一页上放一个「新建」按钮，只会造出第三种
 * 「看起来能做、其实没落地」的操作。清单因此是**观测出来的**，不是维护出来的。
 */
export function UserGroupsSection(props: {
  defaultValues: UserGroupsSectionValues
}) {
  const { t } = useTranslation()
  const { defaultValues } = props

  /**
   * 权威用户分组清单 —— 服务端按 `users.group` 现算，带在册人数与范围状态。
   *
   * `retry:false`：扩展未启用 / 后端 guard 关掉时这个端点直接 404，
   * 重试三次只会让页面上的「加载中」赖着不走。拉不到时下面会退化成
   * 「从两份 option 的键推出来的清单」，并在顶部说明这一点 ——
   * 退化后仍然能改充值折扣，那才是这一页的本体。
   */
  const matrixQuery = useQuery({ ...qyGmMatrixQuery(), retry: false })
  const matrix = matrixQuery.data

  const authoritativeNames = useMemo(
    () => (matrix?.user_groups ?? []).map((group) => group.name),
    [matrix?.user_groups]
  )

  const [rows, setRows] = useState<UserGroupRow[]>(() =>
    buildUserGroupRows(
      [],
      defaultValues.TopupGroupRatio,
      defaultValues.GroupGroupRatio
    )
  )

  /*
    权威清单的最新值，供「服务端 option 回读」那个 effect 读取。

    用 ref 而不是把 `authoritativeNames` 列进那个 effect 的依赖：依赖它的话，
    查询落地会把正在编辑的草稿整份重建一次。两个 effect 各管一件事，互不触发。
  */
  const authoritativeRef = useRef<readonly string[]>([])
  authoritativeRef.current = authoritativeNames
  const [defaultUseAutoGroup, setDefaultUseAutoGroup] = useState(
    defaultValues.DefaultUseAutoGroup
  )

  /*
    保存的键域**由归属清单直接给出**，不是在这里重新抄一遍。

    `Record<(typeof USER_GROUP_PAGE_KEYS)[number], …>` 是精确的：往下面的
    保存载荷里多加一个 `GroupRatio`，TypeScript 当场报「对象字面量只能指定
    已知属性」。「同一份数据只有一个编辑器」这条约束因此在编译期成立，
    而不是只写在注释里等人守规矩。
  */
  const { save, resetBaseline, isSaving } = useGroupOptionSave<
    (typeof USER_GROUP_PAGE_KEYS)[number]
  >({
    TopupGroupRatio: defaultValues.TopupGroupRatio,
    DefaultUseAutoGroup: defaultValues.DefaultUseAutoGroup,
  })

  /*
    服务端回读到达时，用它替换本地一切（草稿 + 基线）。

    ── 为什么不做乐观合并 ──

    保存成功后 `updateOption` 会 invalidate `system-options`，回读到来时这里
    必须整份换成服务端真实值：本地草稿是「我请求过什么」，回读是「服务端现在
    是什么」，把前者当成后者渲染，一次部分失败就会画出一个从未存在过的成功
    画面。另一个管理员在别的标签页改了同一份 option 时同理 —— 服务端赢，
    而不是让本地草稿在下一次保存里把对方的改动整段覆盖掉。

    依赖列表刻意逐个列**原始值**而不是 `defaultValues` 这个对象：上层
    `build(settings)` 每次渲染都新造一个对象，按对象比会让这个 effect 在每一次
    父级重渲染时把正在编辑的内容清掉。
  */
  useEffect(() => {
    setRows(
      buildUserGroupRows(
        authoritativeRef.current,
        defaultValues.TopupGroupRatio,
        defaultValues.GroupGroupRatio
      )
    )
    setDefaultUseAutoGroup(defaultValues.DefaultUseAutoGroup)
    resetBaseline({
      TopupGroupRatio: defaultValues.TopupGroupRatio,
      DefaultUseAutoGroup: defaultValues.DefaultUseAutoGroup,
    })
  }, [
    defaultValues.TopupGroupRatio,
    defaultValues.DefaultUseAutoGroup,
    defaultValues.GroupGroupRatio,
    resetBaseline,
  ])

  /*
    权威清单到达之后，把缺的那几档**物化进 state**，而不是在渲染时拼一段
    「额外行」。

    ── 为什么不能只在 displayRows 里拼 ──

    拼出来的行此前用 `ug_auth_${name}` 作 id，而第一次编辑它时 `updateTopup` 会用
    `ug_new_${name}` 把它追加进 `rows` —— 敲下第一个字符的瞬间行身份就变了，
    `StaticDataTable` 以 `getRowKey` 作 React key，该行整体卸载重挂：**输入框失焦**，
    而且它从 extra 段跳到 rows 段末尾、在表里换了位置。这一页唯一的编辑控件就是它，
    于是每敲一个字符都要重新点一次输入框。

    物化之后 id 与顺序在整个编辑过程中不变。**合并而不是替换**：`TopupGroupRatio`
    里可能有一档人已经清空了（分组被改名、最后一个用户被移走），那一条折扣仍然
    实实在在躺在 options 里，替换掉就再也没有任何界面能看到它。
  */
  useEffect(() => {
    if (authoritativeNames.length === 0) return
    setRows((current) => {
      const known = new Set(current.map((row) => row.name))
      const extra = authoritativeNames
        .filter((name) => !known.has(name))
        .map((name) => ({ id: nextRowId('ug'), name, topupRatio: '' }))
      return extra.length === 0 ? current : [...current, ...extra]
    })
  }, [authoritativeNames])

  const displayRows = rows

  const statsByGroup = useMemo(() => {
    const map = new Map<
      string,
      { userCount: number; granted: number; scopeState: string }
    >()
    if (matrix == null) return map
    for (const group of matrix.user_groups) {
      const granted = matrix.cells.filter(
        (cell) => cell.user_group === group.name && cell.granted
      ).length
      map.set(group.name, {
        userCount: group.user_count,
        granted,
        scopeState: group.scope_state,
      })
    }
    return map
  }, [matrix])

  // 按 **id** 定位而不是按 name：name 是显示值，而行身份是 id。按 name 定位的话，
  // 两行同名（`TopupGroupRatio` 与权威清单在大小写上写岔时会出现）会被一起改掉。
  const updateTopup = useCallback((id: string, value: string) => {
    setRows((current) =>
      current.map((row) =>
        row.id === id ? { ...row, topupRatio: value } : row
      )
    )
  }, [])

  const autoGroupsEmpty = useMemo(
    () => parseAutoGroups(defaultValues.AutoGroups).length === 0,
    [defaultValues.AutoGroups]
  )

  const handleSave = useCallback(() => {
    void save({
      TopupGroupRatio: serializeTopupRatios(displayRows),
      DefaultUseAutoGroup: defaultUseAutoGroup,
    })
  }, [save, displayRows, defaultUseAutoGroup])

  return (
    <SettingsSection title={t('qy_gs_user_groups_title')}>
      <SettingsPageFormActions onSave={handleSave} isSaving={isSaving} />

      <div className='flex flex-wrap items-center justify-between gap-2'>
        <p className='text-muted-foreground min-w-0 text-sm'>
          {t('qy_gs_user_groups_desc')}
        </p>
        <div className='flex shrink-0 flex-wrap gap-2'>
          <GroupPricingGuideButton />
          <GroupOptionsJsonDrawer
            fields={[
              {
                key: 'TopupGroupRatio',
                label: t('Top-up group ratios'),
                value: serializeTopupRatios(displayRows),
                description: t('qy_gs_topup_hint'),
              },
              {
                key: 'GroupGroupRatio',
                label: t('qy_gs_cross_ratio_label'),
                value: defaultValues.GroupGroupRatio,
                readOnly: true,
                description: t('qy_gs_where_cross_ratio'),
              },
            ]}
            onApply={(next) => {
              const raw = next.TopupGroupRatio
              if (raw === undefined) return
              setRows(
                buildUserGroupRows(
                  authoritativeNames,
                  raw,
                  defaultValues.GroupGroupRatio
                )
              )
            }}
          />
        </div>
      </div>

      {matrixQuery.isError && (
        <Alert>
          <Info className='h-4 w-4' />
          <AlertDescription>
            {t('qy_gs_user_groups_no_roster')}
          </AlertDescription>
        </Alert>
      )}

      {/*
        打开「新令牌默认使用 auto」而 auto 清单是空的，是一次没有任何信号的故障：
        每个新注册用户的初始令牌 group='auto'，而 `GetUserAutoGroup` 逐个过
        `IsUserSelectableGroup` 之后返回空列表 —— 该令牌没有任何候选模型分组，
        第一次调用就失败。auto 顺序在「模型分组」页，拆页之后两者不再同屏，
        所以这条提示必须留在开关旁边。
      */}
      {defaultUseAutoGroup && autoGroupsEmpty && (
        <Alert variant='destructive'>
          <Info className='h-4 w-4' />
          <AlertDescription>{t('qy_gs_auto_list_empty_warn')}</AlertDescription>
        </Alert>
      )}

      <SettingsSwitchField
        checked={defaultUseAutoGroup}
        onCheckedChange={setDefaultUseAutoGroup}
        label={t('Default to auto groups')}
        description={t(
          'When enabled, newly created tokens start in the first auto group.'
        )}
      />

      <StaticDataTable
        data={displayRows}
        getRowKey={(row) => row.id}
        emptyClassName='text-muted-foreground h-20 text-sm'
        emptyContent={t('qy_gs_user_groups_empty')}
        columns={[
          {
            id: 'name',
            header: t('qy_gs_user_groups_title'),
            className: 'min-w-40',
            cell: (row) => (
              <span className='text-sm font-medium'>{row.name}</span>
            ),
          },
          {
            id: 'users',
            header: t('qy_gs_col_user_count'),
            className: 'w-24 text-right',
            cellClassName: 'text-right tabular-nums',
            cell: (row) => {
              const stats = statsByGroup.get(row.name)
              return stats == null ? '—' : stats.userCount
            },
          },
          {
            id: 'topup',
            header: t('Top-up ratio'),
            className: 'w-32',
            cell: (row) => (
              <Input
                type='number'
                min={0}
                step={0.1}
                value={row.topupRatio}
                placeholder={t('Not set')}
                aria-label={t('Top-up ratio')}
                onChange={(event) => updateTopup(row.id, event.target.value)}
              />
            ),
          },
          {
            id: 'usable',
            header: t('qy_gs_col_usable_model_groups'),
            className: 'min-w-44',
            cell: (row) => {
              const stats = statsByGroup.get(row.name)
              if (stats == null) return <span>—</span>
              return (
                <div className='flex flex-wrap items-center gap-1.5'>
                  <span className='text-sm tabular-nums'>{stats.granted}</span>
                  {stats.scopeState === 'unset' && (
                    <StatusBadge variant='neutral' copyable={false}>
                      {t('qy_gs_scope_unset')}
                    </StatusBadge>
                  )}
                  {stats.scopeState === 'empty' && (
                    <StatusBadge variant='danger' copyable={false}>
                      {t('qy_gs_scope_empty')}
                    </StatusBadge>
                  )}
                </div>
              )
            },
          },
          {
            id: 'actions',
            header: t('Actions'),
            className: 'text-right',
            cellClassName: 'text-right',
            cell: () => (
              <Link
                to='/system-settings/billing/$section'
                params={{ section: 'group-matrix' }}
                className='text-primary inline-flex items-center gap-1.5 text-sm underline underline-offset-2'
              >
                <SlidersHorizontal className='h-4 w-4' />
                {t('qy_gs_open_matrix')}
              </Link>
            ),
          },
        ]}
      />

      <p className='text-muted-foreground text-xs leading-5'>
        {t('qy_gs_topup_hint')}
      </p>
    </SettingsSection>
  )
}
