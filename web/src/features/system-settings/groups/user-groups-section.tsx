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
import { Info, SlidersHorizontal } from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table/static/static-data-table'
import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { qyGmMatrixQuery } from '@/features/qy/pages/admin-group-matrix/api'
import {
  qyGmIndexCells,
  type QyGmDraft,
} from '@/features/qy/pages/admin-group-matrix/lib/draft'
import { QyUgScopeDialog } from '@/features/qy/pages/admin-user-groups/components/user-group-scope-dialog'
import { QyNewUserDefaultGroupCard } from '@/features/qy/pages/admin-user-groups/default-group'
import { qyUgGrantedModelGroups } from '@/features/qy/pages/admin-user-groups/lib/rows'
import { QyUserGroupRoster } from '@/features/qy/pages/admin-user-groups/roster'

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

/**
 * 这张表上没有草稿：可选性与倍率的编辑全在配置弹窗里。
 *
 * **必须是模块级常量**，不能在渲染里写 `new Map()`：那样每次渲染都是一个新引用，
 * 下面那个 `useMemo` 的依赖恒变，整张名单在每一次按键（充值折扣输入框）上重算。
 */
const QY_UG_NO_DRAFT: QyGmDraft = new Map()

/** 单元格里直接列出几个名字，超出的折进 `+N`。理由见 `usable` 那一列。 */
const QY_UG_USABLE_INLINE_LIMIT = 3

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
 * ── 新建 / 改名 / 删除在下面那张「用户分组登记」卡片上 ──
 *
 * 这一张表的清单是**观测出来的**（`users.group` 的现值），所以它天然表达不了
 * 一个**还没有任何人**的分组 —— 而「新建」造出来的正是那个。写入的落点是登记表
 * `qy_user_groups`，那是 {@link QyUserGroupRoster} 的主语。
 *
 * 改名与删除更不能放在这里：它们要横跨两个数据库改六处（`users.group`、已售订阅
 * 的三列快照、套餐升降级分组、`GroupGroupRatio` 外层键、`TopupGroupRatio` 键，
 * 以及各模块声明的范围/授权/费率/划转规则），任何一处漏掉的表现都是「一批账号
 * 挂在一个不存在的分组上、按 1.0 兜底扣费」且静默。这套顺序与补偿全在后端的
 * 一个端点里，界面只负责在按下按钮之前把影响面摆出来。
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
  /** 正在配置可用模型分组的那一档人。非空 = 弹窗开着。 */
  const [scopeTarget, setScopeTarget] = useState<string | null>(null)

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

  /**
   * 每一档人的在册人数、**可用模型分组名单**、以及范围三态。
   *
   * 名单走 {@link qyUgGrantedModelGroups}（与矩阵页 / 配置弹窗上的徽章同一个
   * 取值），而不是在这里自己 filter 一遍 `cells`：两处各算一份必然漂移成
   * 「这张表列了 3 个、点开弹窗只有 2 个」，而运营对着矛盾的数字最可能的动作
   * 是重配一遍 —— 重配的动作恰好是撤销与改价。这里没有草稿，传空 Map。
   */
  const statsByGroup = useMemo(() => {
    const map = new Map<
      string,
      { userCount: number; usable: string[]; scopeState: string }
    >()
    if (matrix == null) return map
    const serverCells = qyGmIndexCells(matrix.cells)
    for (const group of matrix.user_groups) {
      map.set(group.name, {
        userCount: group.user_count,
        usable: qyUgGrantedModelGroups(
          matrix,
          serverCells,
          QY_UG_NO_DRAFT,
          group.name
        ),
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

      {/*
        「新注册用户落进哪一档」—— 原来是侧栏上一个整页(`/qy/admin/user-group`)，
        整页只有这一个下拉。项目方原话：「当前为何要有 2 个用户分组？只保留一个
        新的即可，旧的这个移除掉。」

        它紧挨着上面那个开关：两者都是**站级的初始分组**默认值，一个管新注册的
        人落哪一档、一个管新建的令牌起手用哪个模型分组。它自带保存按钮，理由与
        它为什么不是行内标记一起写在组件注释里。
      */}
      <QyNewUserDefaultGroupCard />

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
            /*
              ── 这一列列**名字**，不列个数（需求 4）──

              项目方原话：「前端这个列：【可用模型分组】直接把模型分组名称显示
              上去，如：免费の渠道、浅夜の梦专属号池」。一个数字回答不了运营在
              这张表上唯一想问的问题 ——「这一档人到底能用到哪几个池子」，而要
              知道答案就得点进另一页，那正是需求 4 要消掉的那次跳转。

              超过 3 个折进 `+N`，整格 `title` 里给全名单：站里模型分组有 13 个，
              全列出来会把这一行撑成一堵墙，而这张表还要同时显示充值折扣输入框。
              折叠**只折显示**，`title` 与弹窗里都是完整的。

              「未设定范围」那一档必须仍然带徽章：它的名单是**上游此刻的实际
              可选集合**（后端按 `GetUserUsableGroups` 现算），看起来与"配好了"
              一模一样，但它不受这里的范围约束，改动方式完全不同。
            */
            id: 'usable',
            header: t('qy_gs_col_usable_model_groups'),
            className: 'min-w-56',
            cell: (row) => {
              const stats = statsByGroup.get(row.name)
              if (stats == null) return <span>—</span>
              const overflow = stats.usable.length - QY_UG_USABLE_INLINE_LIMIT
              return (
                <div
                  className='flex flex-wrap items-center gap-1.5'
                  title={
                    stats.usable.length === 0
                      ? undefined
                      : stats.usable.join('、')
                  }
                >
                  {stats.usable.length === 0 && (
                    <span className='text-muted-foreground text-sm'>
                      {t('qy_gs_usable_none')}
                    </span>
                  )}
                  {stats.usable
                    .slice(0, QY_UG_USABLE_INLINE_LIMIT)
                    .map((name) => (
                      <Badge
                        key={name}
                        variant='outline'
                        className='max-w-40 truncate'
                      >
                        {name}
                      </Badge>
                    ))}
                  {overflow > 0 && (
                    <Badge variant='secondary' className='tabular-nums'>
                      {t('qy_gs_usable_more', { count: overflow })}
                    </Badge>
                  )}
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
            /*
              ── 弹窗，不跳转（需求 4）──

              原来这里是一条通往「用户分组可用的模型分组配置」那一页的链接。
              项目方点名「不要再跳转」：跳走之后运营正在编辑的充值折扣草稿全部
              丢失（那一页是另一个 section，本 section 会被卸载），回来还要重新
              找到那一行。弹窗与那一页共用同一份状态机与同一道闸门
              （见 `useQyGmEditor`），能力上没有任何删减。
            */
            id: 'actions',
            header: t('Actions'),
            className: 'text-right',
            cellClassName: 'text-right',
            cell: (row) => (
              <Button
                type='button'
                variant='outline'
                size='sm'
                onClick={() => setScopeTarget(row.name)}
              >
                <SlidersHorizontal className='h-4 w-4' />
                {t('qy_gs_open_matrix')}
              </Button>
            ),
          },
        ]}
      />

      <p className='text-muted-foreground text-xs leading-5'>
        {t('qy_gs_topup_hint')}
      </p>

      {/*
        用户分组**登记表**：新建 / 改名 / 带迁移的删除。

        它与上面那张表的分工写在 `QyUserGroupRoster` 的组件注释里 —— 一句话：
        上面那张的清单是从 `users.group` 观测出来的，这一张是运营维护出来的，
        而一个刚建出来、还没有任何人的分组只存在于后者。
      */}
      <QyUserGroupRoster />

      <QyUgScopeDialog
        open={scopeTarget != null}
        onOpenChange={(open) => {
          if (!open) setScopeTarget(null)
        }}
        userGroup={scopeTarget}
      />
    </SettingsSection>
  )
}
