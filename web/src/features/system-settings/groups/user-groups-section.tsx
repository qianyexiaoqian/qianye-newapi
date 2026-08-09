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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { Info, Plus, SlidersHorizontal, Trash2 } from 'lucide-react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { StaticDataTable } from '@/components/data-table/static/static-data-table'
import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { qyKeys } from '@/features/qy/lib/query-keys'
import { qyGmMatrixQuery } from '@/features/qy/pages/admin-group-matrix/api'
import { QyGmStatusBanners } from '@/features/qy/pages/admin-group-matrix/components/status-banners'
import type { QyGmUserGroup } from '@/features/qy/pages/admin-group-matrix/types'
import { QyUgGroupDialog } from '@/features/qy/pages/admin-user-groups/components/user-group-dialog'
import { QyNewUserDefaultGroupCard } from '@/features/qy/pages/admin-user-groups/default-group'
import { qyUgSplitUsable } from '@/features/qy/pages/admin-user-groups/lib/merged-rows'
import {
  qyUgrCreate,
  qyUgrDelete,
  qyUgrImpactQuery,
} from '@/features/qy/pages/admin-user-groups/roster/api'
import { QyUgrActionBlockNote } from '@/features/qy/pages/admin-user-groups/roster/components/action-block-note'
import { QyUgrCreateDialog } from '@/features/qy/pages/admin-user-groups/roster/components/create-dialog'
import { QyUgrDeleteDialog } from '@/features/qy/pages/admin-user-groups/roster/components/delete-dialog'
import { qyUgrDeleteEntry } from '@/features/qy/pages/admin-user-groups/roster/lib/gates'
import type { QyUgrCreateRequest } from '@/features/qy/pages/admin-user-groups/roster/types'
import { qyOpsErrorMessage } from '@/features/qy/pages/ops/errors'

import { SettingsSwitchField } from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { GroupOptionsJsonDrawer } from './components/group-options-json-drawer'
import { GroupPricingGuideButton } from './components/group-pricing-guide'
import { parseAutoGroups } from './lib/group-options'
import { useGroupOptionSave } from './lib/use-group-option-save'

/** 取数未回来时的清单。**模块级常量**：`?? []` 每次渲染都是一个新数组。 */
const QY_UG_NO_ROWS: readonly QyGmUserGroup[] = []

/** 单元格里直接列出几个名字，超出的折进 `+N`。理由见 `usable` 那一列。 */
const QY_UG_USABLE_INLINE_LIMIT = 3

const QY_UGR_EMPTY_DRAFT: QyUgrCreateRequest = {
  display_name: '',
  name: '',
  note: '',
}

export type UserGroupsSectionValues = {
  TopupGroupRatio: string
  DefaultUseAutoGroup: boolean
  /** 只读：这一页只拿它的**外层键**当用户分组的证据，一个字节都不写回。 */
  GroupGroupRatio: string
  /** 只读：用来判断「auto 清单是不是空的」。编辑在「模型分组」页。 */
  AutoGroups: string
}

/**
 * 「用户分组」页 —— **一张表**。
 *
 * ── 项目方原话 ──
 *
 * 「你现在前端怎么感觉搞得一团糟……比如，用户分组这一页，这两个可以合并成一个。
 * 明明一个很简单的问题为什么这么搞这么复杂？」
 * 「简单一点：用户分组：注册用户数，充值倍率，可用模型分组，用户分组备注。
 * 编辑、删除。一个列表框即可。」
 *
 * ── 合并掉的是什么 ──
 *
 * 这一页此前并排着两张表：「用户分组」（从 `users.group` 观测出来的）与
 * 「用户分组登记」（运营在 `qy_user_groups` 里登记出来的）。两个库的分工是真实
 * 存在的（一个刚建出来、还没有人的分组只存在于登记表；一个历史遗留的
 * `users.group` 值可能不在登记表里），但那是**内部数据模型的事** —— 它出现在
 * 界面上，就是把实现细节当成了功能。
 *
 * 现在是并集（{@link qyUgBuildRows}），差异降级成行上的一个「未登记」徽章，
 * 补登记在弹窗里。
 *
 * ── 表只负责看，弹窗负责改 ──
 *
 * 这张表**一个输入框都没有**。行内输入框看起来更方便，代价是这一页要同时持有
 * 一份跨行草稿与一个页级保存键，而运营改完一行就走人时那份草稿会静默丢失。
 * 一行 = 一档人，点「编辑」把这一档的全部配置（备注、充值倍率、可用模型分组、
 * 每一格的倍率与备注）摆在同一屏里，各段有各自的保存与闸门。
 *
 * 「删除」仍然走登记表那套带影响面与迁移的闸门 —— 删掉一档人同时是一次批量
 * 改价与一次批量权限变更，判据全在服务端。
 */
export function UserGroupsSection(props: {
  defaultValues: UserGroupsSectionValues
}) {
  const { t } = useTranslation()
  const { defaultValues } = props
  const queryClient = useQueryClient()

  /**
   * 权威用户分组清单 —— 服务端按 `users.group` 现算，带在册人数与范围状态。
   *
   * `retry:false`：扩展未启用 / 后端 guard 关掉时这个端点直接 404，重试三次只会
   * 让「加载中」赖着不走。拉不到时整张表是空的，并在顶部说明这一点 ——
   * 编空一张表比按半份数据画一张看起来正常的表安全。
   */
  const matrixQuery = useQuery({ ...qyGmMatrixQuery(), retry: false })
  const rows = matrixQuery.data?.user_groups ?? QY_UG_NO_ROWS

  const [defaultUseAutoGroup, setDefaultUseAutoGroup] = useState(
    defaultValues.DefaultUseAutoGroup
  )
  /** 正在编辑的那一档人。非空 = 弹窗开着。 */
  const [editing, setEditing] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)
  const [createDraft, setCreateDraft] = useState(QY_UGR_EMPTY_DRAFT)
  const [deleting, setDeleting] = useState<QyGmUserGroup | null>(null)
  const [migrateTo, setMigrateTo] = useState('')
  const [ack, setAck] = useState(false)

  /*
    保存的键域**由归属清单直接给出**，不是在这里重新抄一遍。
    往下面的保存载荷里多加一个 `GroupRatio`，TypeScript 当场报「对象字面量只能
    指定已知属性」——「同一份数据只有一个编辑器」这条约束因此在编译期成立。

    `TopupGroupRatio` 不在这里：本轮它由 `PUT /user-groups/:name` **按单个键**
    写回（后端把 `topup_ratio` 与 `clear_topup_ratio` 分成两个字段，正是为了
    区分"这次没打算改"与"删掉这个键"）。在这一页再拼一份整表 JSON 写回去，
    就会把另一个管理员刚改的另一档静默覆盖成本页打开那一刻的旧值。
  */
  const { save, resetBaseline, isSaving } =
    useGroupOptionSave<'DefaultUseAutoGroup'>({
      DefaultUseAutoGroup: defaultValues.DefaultUseAutoGroup,
    })

  /*
    服务端回读到达时，用它替换本地一切（草稿 + 基线）。

    本地状态是「我请求过什么」，回读是「服务端现在是什么」，把前者当成后者渲染，
    一次部分失败就会画出一个从未存在过的成功画面。另一个管理员在别的标签页改了
    同一份 option 时同理 —— 服务端赢。

    依赖刻意列**原始值**而不是 `defaultValues` 这个对象：上层 `build(settings)`
    每次渲染都新造一个对象，按对象比会让这个 effect 在每一次父级重渲染时把正在
    编辑的内容清掉。
  */
  useEffect(() => {
    setDefaultUseAutoGroup(defaultValues.DefaultUseAutoGroup)
    resetBaseline({ DefaultUseAutoGroup: defaultValues.DefaultUseAutoGroup })
  }, [defaultValues.DefaultUseAutoGroup, resetBaseline])

  const rowsByName = useMemo(
    () => new Map(rows.map((row) => [row.name, row])),
    [rows]
  )

  const autoGroupsEmpty = useMemo(
    () => parseAutoGroups(defaultValues.AutoGroups).length === 0,
    [defaultValues.AutoGroups]
  )

  /*
    ── 状态横幅：这一页现在是这些状态的**主入口** ──────────────────────

    合并之前它们只挂在矩阵整页与配置弹窗上。合并之后运营的日常入口是这一页，
    而横幅回答的恰恰是「下面这张表现在到底算不算数」：

      · `snapshot.loaded === false` —— 权威清单一条都没生效，全站按上游白名单
        放行。这一页照旧把清单画得像在生效，不说出来就是一次没有任何信号的故障。
      · `warnings` —— 后端现算的待办，其中一条以「【需要处理】」开头，明说某一档的
        清单里有已从倍率表消失的模型分组（那正是「可用模型分组」那一列看起来
        不对劲时的真实原因）。
      · `shadow_write_denies` —— 影子期唯一可归因的证据。

    早先它们只能在点开某一档的编辑弹窗之后才看得到，而运营没有理由去点。
  */
  const banner = useMemo(() => {
    const selfExcluded: string[] = []
    const emptyScopeGroups: string[] = []
    for (const row of rows) {
      if (row.self_excluded && row.scope_state !== 'unset') {
        selfExcluded.push(row.name)
      }
      if (row.scope_state === 'empty') emptyScopeGroups.push(row.name)
    }
    return { emptyScopeGroups, selfExcluded }
  }, [rows])

  const refreshRoster = useCallback(async () => {
    await queryClient.invalidateQueries({
      queryKey: qyKeys.adminUserGroupRoster(),
    })
    // 观测清单里的在册人数与范围状态也会跟着变。不一起失效的表现是：删完之后
    // 同一张表上还列着那个已经不存在的分组。
    await queryClient.invalidateQueries({ queryKey: qyKeys.adminGroupMatrix() })
  }, [queryClient])

  const impactQuery = useQuery({
    ...qyUgrImpactQuery(deleting?.name ?? null, migrateTo),
    retry: false,
  })

  const createMutation = useMutation({
    mutationFn: () => qyUgrCreate(createDraft),
    onSuccess: async (result) => {
      toast.success(t('qy_ugr_created', { name: result.name }))
      // 「建好了但还不能用」是服务端现算的。逐条常驻地弹出来：折进一句「请注意
      // 配置」的话，运营下一步照样会把人挪进来，然后拿到一批 403。
      for (const warning of result.warnings) {
        toast.warning(warning, { duration: Number.POSITIVE_INFINITY })
      }
      setCreating(false)
      setCreateDraft(QY_UGR_EMPTY_DRAFT)
      await refreshRoster()
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const deleteMutation = useMutation({
    mutationFn: () =>
      qyUgrDelete(deleting?.name ?? '', {
        ack_loses_everything: ack,
        expect_users: impactQuery.data?.users ?? 0,
        migrate_to: migrateTo,
      }),
    onSuccess: async (result) => {
      // 半成状态必须原样弹出来并留在屏幕上：报一句绿色的「已完成」然后走人，
      // 是这套跨库设计最坏的失败方式 —— 线上停在中间态，而运营以为一切正常。
      if (result.partial != null) {
        toast.error(result.partial.message, {
          duration: Number.POSITIVE_INFINITY,
        })
      } else if (result.stragglers > 0) {
        toast.warning(t('qy_ugr_stragglers', { count: result.stragglers }), {
          duration: Number.POSITIVE_INFINITY,
        })
      } else {
        toast.success(
          result.to === ''
            ? t('qy_ugr_deleted', { name: result.from })
            : t('qy_ugr_deleted_migrated', {
                name: result.from,
                target: result.to,
                users: result.users,
              })
        )
      }
      closeDelete()
      await refreshRoster()
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const closeDelete = useCallback(() => {
    setDeleting(null)
    // 迁移目标与强制勾选**必须**跟着弹窗一起清掉：留着的话下一次删除会带着
    // 上一次的选择打开，而那个勾选正是「我知道这会让一批人的令牌全部挂掉」的
    // 全部凭据 —— 它不该被上一次操作代签。
    setMigrateTo('')
    setAck(false)
  }, [])

  const editingRow = editing == null ? null : rowsByName.get(editing)

  return (
    <SettingsSection title={t('qy_gs_user_groups_title')}>
      <SettingsPageFormActions
        onSave={() => void save({ DefaultUseAutoGroup: defaultUseAutoGroup })}
        isSaving={isSaving}
      />

      <div className='flex flex-wrap items-center justify-between gap-2'>
        <p className='text-muted-foreground min-w-0 text-sm'>
          {t('qy_gs_user_groups_desc')}
        </p>
        <div className='flex shrink-0 flex-wrap gap-2'>
          <GroupPricingGuideButton />
          <GroupOptionsJsonDrawer
            fields={[
              {
                // 只读：本轮充值倍率改由 `PUT /user-groups/:name` 按单个键写回。
                // 抽屉是整份覆盖语义，留成可写就等于给这一页留了一条"用打开
                // 那一刻的旧快照整表覆盖"的路，而它与逐键写回互相看不见。
                key: 'TopupGroupRatio',
                label: t('Top-up group ratios'),
                value: defaultValues.TopupGroupRatio,
                readOnly: true,
                description: t('qy_ug_topup_readonly_hint'),
              },
              {
                key: 'GroupGroupRatio',
                label: t('qy_gs_cross_ratio_label'),
                value: defaultValues.GroupGroupRatio,
                readOnly: true,
                description: t('qy_gs_where_cross_ratio'),
              },
            ]}
            // 两个字段都是只读的，这里没有可写字段能被应用回来。
            onApply={() => undefined}
          />
        </div>
      </div>

      {matrixQuery.isError && (
        <Alert variant='destructive'>
          <Info className='h-4 w-4' />
          <AlertDescription>
            {t('qy_gs_user_groups_no_roster')}
          </AlertDescription>
        </Alert>
      )}

      {matrixQuery.data != null && (
        <QyGmStatusBanners
          snapshot={matrixQuery.data.snapshot}
          // 这一页不做保存，也不持有 base_ratio_hash：那两条状态属于配置弹窗。
          partial={null}
          ratioDrift={false}
          selfExcluded={banner.selfExcluded}
          // 大小写近似项由后端的 warnings 逐条给出（含"为什么不折叠"），
          // 这里再画一遍只会把同一件事说两次。
          caseNearMiss={[]}
          warnings={matrixQuery.data.warnings}
          shadowWriteDenies={matrixQuery.data.shadow_write_denies}
          emptyScopeGroups={banner.emptyScopeGroups}
          onReload={() => void matrixQuery.refetch()}
        />
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
        「新注册用户落进哪一档」—— 站级默认值，与上面那个开关并列：一个管新注册的
        人落哪一档、一个管新建的令牌起手用哪个模型分组。它自带保存按钮。
      */}
      <QyNewUserDefaultGroupCard />

      <div className='flex flex-wrap items-center justify-between gap-2'>
        <p className='text-muted-foreground min-w-0 text-xs leading-5'>
          {t('qy_ug_table_hint')}
        </p>
        <Button
          size='sm'
          onClick={() => {
            setCreateDraft(QY_UGR_EMPTY_DRAFT)
            setCreating(true)
          }}
        >
          <Plus className='h-4 w-4' />
          {t('qy_ugr_create_title')}
        </Button>
      </div>

      <StaticDataTable
        data={[...rows]}
        getRowKey={(row) => row.name}
        emptyClassName='text-muted-foreground h-20 text-sm'
        emptyContent={t('qy_gs_user_groups_empty')}
        columns={[
          {
            id: 'name',
            header: t('Group name'),
            className: 'min-w-40',
            cell: (row) => (
              <div className='min-w-0'>
                <div className='text-sm font-medium break-words'>
                  {row.name}
                </div>
                <div className='mt-1 flex flex-wrap gap-1'>
                  {!row.registered && (
                    // 「未登记」不是错误，是一个可以补上的状态。文案与弹窗里的
                    // 补登记按钮说的是同一件事，运营点进去就知道该按哪里。
                    <StatusBadge variant='neutral' copyable={false}>
                      {t('qy_ug_badge_unregistered')}
                    </StatusBadge>
                  )}
                  {!row.enabled && (
                    <StatusBadge variant='neutral' copyable={false}>
                      {t('Disabled')}
                    </StatusBadge>
                  )}
                </div>
              </div>
            ),
          },
          {
            id: 'users',
            header: t('qy_gs_col_user_count'),
            className: 'w-24 text-right',
            cellClassName: 'text-right tabular-nums',
            // null = 观测不到（矩阵 404），**不是 0**。把「没查到」画成 0 会让
            // 运营以为这一档空了而放心删掉它。
            cell: (row) => row.user_count,
          },
          {
            id: 'topup',
            header: t('Top-up ratio'),
            className: 'w-28',
            cellClassName: 'tabular-nums',
            /*
              没配过时显示的是**兜底值 + 一句「没配过」**，不是一个空格子。

              后端下发的 `topup_ratio: null` 与 `topup_ratio_effective: "1"`
              合起来才说得清「按 1 收钱，但那个 1 不是任何人配出来的」。
              只画空的话，运营会以为这一档不参与充值折扣；只画 1 的话，
              他会以为有人做过这个决定。
            */
            cell: (row) =>
              row.topup_ratio == null ? (
                <span className='text-muted-foreground text-xs'>
                  {t('qy_ug_topup_fallback', {
                    ratio: row.topup_ratio_effective,
                  })}
                </span>
              ) : (
                <span>{row.topup_ratio}</span>
              ),
          },
          {
            /*
              ── 这一列列**名字**，不列个数 ──

              项目方原话：「前端这个列：【可用模型分组】直接把模型分组名称显示
              上去，如：免费の渠道、浅夜の梦专属号池」。一个数字回答不了运营在
              这张表上唯一想问的问题 ——「这一档人到底能用到哪几个池子」。

              超过 3 个折进 `+N`，整格 `title` 里给全名单：站里模型分组有十几个，
              全列出来会把这一行撑成一堵墙。折叠**只折显示**。

              「没设可用清单」那一档的名单是**上游此刻的实际可选集合**，看起来
              与"配好了"一模一样，所以必须带一句说明 —— 而那句说明是人话
              （「没设 = 按模型分组的『用户可选』来」），不是「未设定范围」这种
              只有读过数据模型的人才懂的内部术语。
            */
            id: 'usable',
            header: t('qy_gs_col_usable_model_groups'),
            className: 'min-w-56',
            cell: (row) => <QyUgUsableCell row={row} />,
          },
          {
            id: 'note',
            header: t('qy_ug_col_note'),
            className: 'min-w-40',
            cell: (row) =>
              row.note.trim() === '' ? (
                <span className='text-muted-foreground text-xs'>—</span>
              ) : (
                <span className='text-xs break-words'>{row.note}</span>
              ),
          },
          {
            id: 'actions',
            header: t('Actions'),
            className: 'text-right',
            cellClassName: 'text-right',
            cell: (row) => {
              /*
                ── 删除入口：短标签留在行上，完整理由留给弹窗 ────────────────

                此前理由塞在按钮的 `title` 上。禁用的按钮在多数浏览器上不派发
                指针事件，那句解释因此永远不出现 —— 项目方原话「我选择了其他
                分组仍然无法删除」，他点了、没反应、也没有任何解释。

                但把 `default` 这一档的按钮直接关掉又走过了头：删除弹窗是全站
                唯一渲染后端 `block_reason` 的地方，关掉按钮等于让后端那段写得
                很清楚的理由（以及按 `block_code` 给出的替代做法）永远到不了
                屏幕，运营读到的只是前端另写的一份副本。所以这里只印一句短的
                状态标签，按钮照常打开弹窗，由弹窗给全原因与下一步。

                真正关掉按钮的只有一种：删除端点的 `lookupUserGroup` 根本寻址
                不到这个名字（没有登记行、users.group 里也没有人挂着）。
              */
              const deleteEntry = qyUgrDeleteEntry(row)
              return (
                <div className='flex flex-col items-end gap-1'>
                  <div className='flex justify-end gap-1'>
                    <Button
                      type='button'
                      variant='outline'
                      size='sm'
                      onClick={() => setEditing(row.name)}
                    >
                      <SlidersHorizontal className='h-4 w-4' />
                      {t('Edit')}
                    </Button>
                    <Button
                      type='button'
                      variant='ghost'
                      size='sm'
                      aria-label={t('Delete')}
                      // 禁用而不是隐藏：藏起来会让运营以为这一行本来就没有
                      // 删除这个动作，而实际上它只是此刻寻址不到。
                      disabled={!deleteEntry.enabled}
                      onClick={() => {
                        setMigrateTo('')
                        setAck(false)
                        setDeleting(row)
                      }}
                    >
                      <Trash2 className='h-4 w-4' />
                    </Button>
                  </div>
                  <QyUgrActionBlockNote
                    noteKey={deleteEntry.noteKey}
                    className='max-w-64 text-right'
                  />
                </div>
              )
            },
          },
        ]}
      />

      <div className='text-muted-foreground flex flex-wrap items-center gap-x-2 gap-y-1 text-xs leading-5'>
        <span className='min-w-0'>{t('qy_gs_topup_hint')}</span>
        {/*
          ── 整列批量 / 跨档对比 / 孤儿令牌基线的入口 ────────────────────

          那三件事是**排查**动作而不是配置动作，所以它们不该在计费与支付里
          占一个菜单项（那正是项目方说的"一团糟"）。但它们整体只活在
          `/qy/admin/group-matrix` 这一条路由上，而这一页下线第三个菜单项之后
          全站没有任何一处链到它 —— 不看代码的人只能手敲 URL，实际表现是这三件
          能力等同于被删除。所以入口收在这里、以"高级"的名义留一条链接。
        */}
        <Link
          to='/qy/admin/group-matrix'
          className='underline underline-offset-2'
        >
          {t('qy_ug_open_advanced_matrix')}
        </Link>
      </div>

      <QyUgGroupDialog
        open={editing != null}
        onOpenChange={(open) => {
          if (!open) setEditing(null)
        }}
        userGroup={editing}
        row={editingRow ?? null}
        onSaved={refreshRoster}
      />

      <QyUgrCreateDialog
        open={creating}
        onOpenChange={setCreating}
        draft={createDraft}
        onDraftChange={(patch) =>
          setCreateDraft((current) => ({ ...current, ...patch }))
        }
        isSaving={createMutation.isPending}
        onConfirm={() => createMutation.mutate()}
      />

      <QyUgrDeleteDialog
        open={deleting != null}
        onOpenChange={(open) => {
          if (!open) closeDelete()
        }}
        impact={impactQuery.data ?? null}
        isLoading={impactQuery.isLoading}
        isRefreshing={impactQuery.isFetching && impactQuery.data != null}
        isDeleting={deleteMutation.isPending}
        target={migrateTo}
        onTargetChange={(value) => {
          setMigrateTo(value)
          // 换目标 = 换一份差异。上一个目标的「我知道他们会全部挂掉」不能顺延。
          setAck(false)
        }}
        ack={ack}
        onAckChange={setAck}
        onConfirm={() => deleteMutation.mutate()}
      />
    </SettingsSection>
  )
}

/**
 * 「可用模型分组」那一格。
 *
 * ── 「没设 = 全禁」是这一格唯一会造成误操作的读法 ──
 *
 * 合并之前这里显示的是「未设定范围」。那是数据模型的词（`qy_group_scopes` 有没有
 * 这一行），而运营读到的是「什么都没配」，于是最自然的下一步是去把它配上 ——
 * 而"配上"意味着从"全部可用"变成"只有勾选的可用"，那是一次实实在在的收权。
 *
 * 换成人话：**没设 = 按模型分组自己的「用户可选」来**。这句话同时说清了两件事：
 * 它现在是放行的，以及要改的话该去哪一页改。
 */
function QyUgUsableCell(props: { row: QyGmUserGroup }) {
  const { t } = useTranslation()
  const { row } = props

  const { shown, overflow } = qyUgSplitUsable(
    row.model_groups,
    QY_UG_USABLE_INLINE_LIMIT
  )

  return (
    <div
      className='flex flex-wrap items-center gap-1.5'
      title={
        row.model_groups.length === 0 ? undefined : row.model_groups.join('、')
      }
    >
      {row.model_groups.length === 0 && (
        <span className='text-muted-foreground text-sm'>
          {t('qy_gs_usable_none')}
        </span>
      )}
      {shown.map((name) => (
        <Badge key={name} variant='outline' className='max-w-40 truncate'>
          {name}
        </Badge>
      ))}
      {overflow > 0 && (
        <Badge variant='secondary' className='tabular-nums'>
          {t('qy_gs_usable_more', { count: overflow })}
        </Badge>
      )}
      {row.scope_state === 'unset' && (
        <StatusBadge variant='neutral' copyable={false}>
          {t('qy_ug_scope_follows_model_group')}
        </StatusBadge>
      )}
      {row.scope_state === 'empty' && (
        <StatusBadge variant='danger' copyable={false}>
          {t('qy_ug_scope_blocks_everything')}
        </StatusBadge>
      )}
      {/*
        设了清单、但它此刻是**影子模式** —— 清单一个字节都不生效，用户实际
        仍按上面那条规则放行。不说出来的话，界面显示「只能用这几个」而线上
        谁都拦不住，而那正是项目方那句「设了可用模型分组则用户只能选这些」
        唯一对不上的一种状态。

        判据是 `scope_state !== 'unset'`，**不是 `=== 'set'`**：空清单的
        `scope_state` 是 `'empty'`，而 shadow + 空清单是完全可达的一档
        （后端错误正文推荐的正是"先建一条 shadow scope"）。只判 `'set'` 时
        那一档只剩一枚红色的「一个都不能用」，运营据此以为这一档已经被锁死，
        下一步可能是紧急迁人 —— 而 shadow 期这批人此刻什么都没被拦。
      */}
      {row.scope_state !== 'unset' && !row.scope_enforced && (
        <StatusBadge variant='warning' copyable={false}>
          {t('qy_ug_scope_shadow')}
        </StatusBadge>
      )}
    </div>
  )
}
