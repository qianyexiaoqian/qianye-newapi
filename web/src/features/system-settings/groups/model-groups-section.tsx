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
import { AlertTriangle, GripVertical, Plus, Trash2 } from 'lucide-react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { StaticDataTable } from '@/components/data-table/static/static-data-table'
import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { qyKeys } from '@/features/qy/lib/query-keys'
import {
  qyMgDelete,
  qyMgImpactQuery,
  qyMgListQuery,
  qyMgUpdate,
} from '@/features/qy/pages/admin-model-groups/api'
import { QyMgDeleteDialog } from '@/features/qy/pages/admin-model-groups/components/delete-dialog'
import {
  qyMgBuildRows,
  qyMgFreeNames,
  qyMgInvalidRatioNames,
  qyMgSerializeRatios,
  qyMgSerializeUsableGroups,
  qyMgSilentlyBilledNames,
  type QyMgMergedRow,
} from '@/features/qy/pages/admin-model-groups/lib/merged-rows'
import { qyOpsErrorMessage } from '@/features/qy/pages/ops/errors'

import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { GroupOptionsJsonDrawer } from './components/group-options-json-drawer'
import { GroupPricingGuideButton } from './components/group-pricing-guide'
import {
  duplicateRowNames,
  moveAutoGroup,
  nextRowId,
  parseAutoGroups,
  parseGroupDescriptionMap,
  parseGroupRatioMap,
  serializeAutoGroups,
  type MODEL_GROUP_PAGE_KEYS,
} from './lib/group-options'
import {
  changedGroupOptionKeys,
  useGroupOptionSave,
} from './lib/use-group-option-save'

export type ModelGroupsSectionValues = {
  GroupRatio: string
  AutoGroups: string
  MaxTokenAutoGroups: number
  /** 「用户可选」那一列就是它的**键**。见 `qyMgSerializeUsableGroups` 的口径说明。 */
  UserUsableGroups: string
}

/**
 * 「模型分组」页 —— **一张表**。
 *
 * ── 项目方点名的四列 ──
 *
 * 「模型分组：分组名称，兜底倍率，用户可选，分组备注。」
 *
 * 这一页此前也是两张表（上面「分组倍率」、下面「模型分组登记」），同一批名字
 * 出现两次、各带一半的列。合并的判据与用户分组页逐字相同：运营在这一页上要
 * 回答的是一个问题（这个池子按几倍收、用户看不看得见、备注写什么），那就该是
 * 一行。合并逻辑与序列化在
 * `features/qy/pages/admin-model-groups/lib/merged-rows.ts`，有测试。
 *
 * ── 「渠道数」为什么是实时反查而不是一个可配置字段 ──
 *
 * 「这个模型分组底下还有没有池子」是**事实**不是配置，而它正是 503 的直接原因：
 * 一个有倍率、没渠道的模型分组在令牌下拉里长得和正常的一模一样，选中之后每一次
 * 请求都 503。落成一个字段就一定会与现实不同步 —— 而不同步的方向恰好是
 * 「界面说有、线上没有」。拉不到时显示 `—` 且**不出告警**：把「不确定」画成
 * 「没有渠道」，整张表会挂满假警报，而假警报比没有警报更糟。
 */
export function ModelGroupsSection(props: {
  defaultValues: ModelGroupsSectionValues
}) {
  const { t } = useTranslation()
  const { defaultValues } = props
  const queryClient = useQueryClient()

  const registryQuery = useQuery({ ...qyMgListQuery(), retry: false })

  const [rows, setRows] = useState<QyMgMergedRow[]>([])
  const [autoGroups, setAutoGroups] = useState<string[]>(() =>
    parseAutoGroups(defaultValues.AutoGroups)
  )
  const [maxTokenAutoGroups, setMaxTokenAutoGroups] = useState(
    String(defaultValues.MaxTokenAutoGroups)
  )
  const [deleting, setDeleting] = useState<string | null>(null)
  const [forceHasRoute, setForceHasRoute] = useState(false)
  const [forceOrphanTokens, setForceOrphanTokens] = useState(false)

  // 键域由归属清单给出，多写一个键即编译失败。理由见 `user-groups-section`。
  const { save, resetBaseline, isSaving } = useGroupOptionSave<
    (typeof MODEL_GROUP_PAGE_KEYS)[number]
  >({
    GroupRatio: defaultValues.GroupRatio,
    AutoGroups: defaultValues.AutoGroups,
    MaxTokenAutoGroups: defaultValues.MaxTokenAutoGroups,
    UserUsableGroups: defaultValues.UserUsableGroups,
  })

  const registryItems = registryQuery.data?.items

  /*
    服务端回读到达时，用它替换本地一切（草稿 + 基线）。

    本地草稿是「我请求过什么」，回读是「服务端现在是什么」，把前者当成后者渲染，
    一次部分失败就会画出一个从未存在过的成功画面。另一个管理员在别的标签页改了
    同一份 option 时同理 —— 服务端赢。

    依赖刻意逐个列**原始值**：上层 `build(settings)` 每次渲染都新造一个对象，
    按对象比会让这个 effect 在每一次父级重渲染时把正在编辑的内容清掉。
  */
  useEffect(() => {
    setRows(
      qyMgBuildRows({
        registry: registryItems ?? [],
        groupRatios: parseGroupRatioMap(defaultValues.GroupRatio),
        usableGroups: parseGroupDescriptionMap(defaultValues.UserUsableGroups),
        autoGroups: parseAutoGroups(defaultValues.AutoGroups),
      })
    )
    setAutoGroups(parseAutoGroups(defaultValues.AutoGroups))
    setMaxTokenAutoGroups(String(defaultValues.MaxTokenAutoGroups))
    resetBaseline({
      GroupRatio: defaultValues.GroupRatio,
      AutoGroups: defaultValues.AutoGroups,
      MaxTokenAutoGroups: defaultValues.MaxTokenAutoGroups,
      UserUsableGroups: defaultValues.UserUsableGroups,
    })
  }, [
    defaultValues.GroupRatio,
    defaultValues.AutoGroups,
    defaultValues.MaxTokenAutoGroups,
    defaultValues.UserUsableGroups,
    registryItems,
    resetBaseline,
  ])

  const duplicates = useMemo(() => duplicateRowNames(rows), [rows])
  const invalidRatios = useMemo(() => qyMgInvalidRatioNames(rows), [rows])
  const silentlyBilled = useMemo(() => qyMgSilentlyBilledNames(rows), [rows])
  const freeGroups = useMemo(() => qyMgFreeNames(rows), [rows])
  const emptyPools = useMemo(
    () =>
      rows
        .filter((row) => row.selectable && row.hasRoute === false)
        .map((row) => row.name),
    [rows]
  )

  const parsedMax = Number(maxTokenAutoGroups)
  const maxInvalid =
    !Number.isInteger(parsedMax) || parsedMax < 1 || maxTokenAutoGroups === ''

  const updateRow = useCallback((id: string, patch: Partial<QyMgMergedRow>) => {
    setRows((current) =>
      current.map((row) => (row.id === id ? { ...row, ...patch } : row))
    )
  }, [])

  const addRow = useCallback(() => {
    setRows((current) => {
      const taken = new Set(current.map((row) => row.name.trim()))
      let index = 1
      let name = `group_${index}`
      while (taken.has(name)) {
        index += 1
        name = `group_${index}`
      }
      return [
        ...current,
        {
          // id 走单调自增序列，**不从名字派生**：这一行的名字正在被敲，
          // 从名字派生就是每敲一个字换一次 React key —— 输入框每个字符失焦一次。
          id: nextRowId('mg'),
          name,
          ratio: '1',
          selectable: false,
          note: '',
          usableDescription: '',
          sources: [],
          hasRoute: null,
          channelCount: null,
          legacyDual: false,
          autoPosition: 0,
          registered: false,
          isNew: true,
        },
      ]
    })
  }, [])

  const handleSave = useCallback(() => {
    void save({
      GroupRatio: qyMgSerializeRatios(rows),
      UserUsableGroups: qyMgSerializeUsableGroups(rows),
      AutoGroups: serializeAutoGroups(autoGroups),
      MaxTokenAutoGroups: parsedMax,
    })
  }, [save, rows, autoGroups, parsedMax])

  const refreshRegistry = useCallback(async () => {
    await queryClient.invalidateQueries({ queryKey: qyKeys.adminModelGroups() })
  }, [queryClient])

  /*
    备注保存成功后**刻意不刷新登记表**。

    刷新会让 `registryItems` 换一个引用，上面那个 effect 随即用服务端数据整个
    `setRows()` + `resetBaseline()`——于是同一张表上别的行里还没保存的兜底倍率与
    「用户可选」改动被静默复原，而屏幕上只有一句绿色的「备注已保存」。运营接着
    点顶部「保存」，写回去的是旧值，他以为改价已经落地。

    这次写入的唯一产物就是这一行的 `note`，而本地行里已经是它了 —— 没有任何
    需要从服务端拿回来的东西。删除是另一回事：那会改变行的集合，必须重建。
  */
  const noteMutation = useMutation({
    mutationFn: (input: { name: string; note: string }) =>
      qyMgUpdate(input.name, { note: input.note }),
    onSuccess: () => toast.success(t('qy_mg_note_saved')),
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const impactQuery = useQuery({ ...qyMgImpactQuery(deleting), retry: false })

  const deleteMutation = useMutation({
    mutationFn: (name: string) =>
      qyMgDelete(name, {
        force_has_route: forceHasRoute,
        force_orphan_tokens: forceOrphanTokens,
      }),
    onSuccess: async (result) => {
      // 半成状态必须原样弹出来并留在屏幕上：两库不原子的最坏失败方式是运营看到
      // 一句绿色的「已删除」然后走人，而线上停在中间态。
      if (result.partial == null) {
        toast.success(
          t('qy_mg_deleted', { keys: result.removed_from.join('、') })
        )
      } else {
        toast.error(result.partial.message, {
          duration: Number.POSITIVE_INFINITY,
        })
      }
      closeDelete()
      await refreshRegistry()
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const closeDelete = useCallback(() => {
    setDeleting(null)
    // 两个覆盖勾选**必须**跟着弹窗一起清掉：留着的话下一次删除会带着上一次的
    // 勾选状态打开，而那两个勾选正是「我知道这会让 200 个令牌挂掉」的全部凭据。
    setForceHasRoute(false)
    setForceOrphanTokens(false)
  }, [])

  const autoCandidates = useMemo(
    () =>
      rows
        .map((row) => row.name.trim())
        .filter((name) => name !== '' && !autoGroups.includes(name)),
    [rows, autoGroups]
  )

  const saveBlocked =
    duplicates.length > 0 || invalidRatios.length > 0 || maxInvalid

  /*
    这一页此刻有没有**还没按保存**的改动。

    删除是唯一一个必须让服务端重建整张表的动作（它改的正是 `options.GroupRatio`
    的键集合），所以它会连带丢掉同屏未保存的兜底倍率 /「用户可选」草稿。
    拦不住也不该拦 —— 但必须说出来：一句绿色的「已删除」加上一批悄悄复原的数字，
    正是这一页最贵的失败方式。判据复用保存路径那份逐键差分，不另写一套。
  */
  const hasUnsavedEdits =
    changedGroupOptionKeys(
      {
        AutoGroups: serializeAutoGroups(autoGroups),
        GroupRatio: qyMgSerializeRatios(rows),
        MaxTokenAutoGroups: parsedMax,
        UserUsableGroups: qyMgSerializeUsableGroups(rows),
      },
      {
        AutoGroups: defaultValues.AutoGroups,
        GroupRatio: defaultValues.GroupRatio,
        MaxTokenAutoGroups: defaultValues.MaxTokenAutoGroups,
        UserUsableGroups: defaultValues.UserUsableGroups,
      }
    ).length > 0

  return (
    <SettingsSection title={t('qy_gs_model_groups_title')}>
      <SettingsPageFormActions
        onSave={handleSave}
        isSaving={isSaving}
        isSaveDisabled={saveBlocked}
      />

      <div className='flex flex-wrap items-center justify-between gap-2'>
        <p className='text-muted-foreground min-w-0 text-sm'>
          {t('qy_gs_model_groups_desc')}
        </p>
        <div className='flex shrink-0 flex-wrap gap-2'>
          <GroupPricingGuideButton />
          <GroupOptionsJsonDrawer
            fields={[
              {
                key: 'GroupRatio',
                label: t('Group ratios'),
                value: qyMgSerializeRatios(rows),
              },
              {
                key: 'UserUsableGroups',
                label: t('Selectable groups'),
                value: qyMgSerializeUsableGroups(rows),
                description: t('qy_mg_usable_value_is_legacy'),
              },
              {
                key: 'AutoGroups',
                label: t('Auto assignment order'),
                value: serializeAutoGroups(autoGroups),
              },
            ]}
            onApply={(next) => {
              // 三份 option 一起重建行：只应用其中一份的话，另外两份仍是旧值，
              // 而它们共用同一批行 —— 表现是勾选状态与倍率对不上号。
              setRows(
                qyMgBuildRows({
                  registry: registryItems ?? [],
                  groupRatios: parseGroupRatioMap(
                    next.GroupRatio ?? qyMgSerializeRatios(rows)
                  ),
                  usableGroups: parseGroupDescriptionMap(
                    next.UserUsableGroups ?? qyMgSerializeUsableGroups(rows)
                  ),
                  autoGroups: parseAutoGroups(
                    next.AutoGroups ?? serializeAutoGroups(autoGroups)
                  ),
                })
              )
              if (next.AutoGroups !== undefined) {
                setAutoGroups(parseAutoGroups(next.AutoGroups))
              }
            }}
          />
        </div>
      </div>

      {duplicates.length > 0 && (
        <Alert variant='destructive'>
          <AlertTriangle className='h-4 w-4' />
          <AlertDescription>
            {t('Duplicate group names: {{names}}', {
              names: duplicates.join(', '),
            })}
          </AlertDescription>
        </Alert>
      )}

      {invalidRatios.length > 0 && (
        <Alert variant='destructive'>
          <AlertTriangle className='h-4 w-4' />
          <AlertDescription>
            {t('qy_gs_invalid_ratio_warn', { names: invalidRatios.join(', ') })}
          </AlertDescription>
        </Alert>
      )}

      {silentlyBilled.length > 0 && (
        <Alert>
          <AlertTriangle className='h-4 w-4' />
          <AlertTitle>{t('qy_gs_missing_ratio_title')}</AlertTitle>
          <AlertDescription>
            {t('qy_gs_missing_ratio_warn', {
              names: silentlyBilled.join(', '),
            })}
          </AlertDescription>
        </Alert>
      )}

      {emptyPools.length > 0 && (
        <Alert>
          <AlertTriangle className='h-4 w-4' />
          <AlertTitle>{t('qy_gs_no_channels_title')}</AlertTitle>
          <AlertDescription>
            {t('qy_gs_no_channels_warn', { names: emptyPools.join(', ') })}
          </AlertDescription>
        </Alert>
      )}

      {freeGroups.length > 0 && (
        <Alert>
          <AlertTriangle className='h-4 w-4' />
          <AlertDescription>
            {t('qy_gs_free_ratio_warn', { names: freeGroups.join(', ') })}
          </AlertDescription>
        </Alert>
      )}

      <Card>
        <CardHeader className='border-b'>
          <div className='flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between'>
            <div>
              <CardTitle>{t('qy_gs_model_groups_title')}</CardTitle>
              <CardDescription>{t('qy_mg_table_desc')}</CardDescription>
            </div>
            <Button onClick={addRow} size='sm' className='sm:self-start'>
              <Plus className='mr-2 h-4 w-4' />
              {t('Add group')}
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          <StaticDataTable
            data={rows}
            getRowKey={(row) => row.id}
            emptyClassName='text-muted-foreground h-20 text-sm'
            emptyContent={t('No groups yet. Add a group to get started.')}
            columns={[
              {
                id: 'name',
                header: t('Group name'),
                className: 'min-w-48',
                cell: (row) =>
                  row.isNew ? (
                    // 还没保存过的一行才可以改名字。已存在的名字改在这里只会改掉
                    // `options.GroupRatio` 的键，而路由表、令牌、授权、套餐解锁
                    // 一个都不动 —— 详见 `QyMgMergedRow.isNew` 的说明。
                    <Input
                      value={row.name}
                      aria-label={t('Group name')}
                      aria-invalid={duplicates.includes(row.name.trim())}
                      onChange={(event) =>
                        updateRow(row.id, { name: event.target.value })
                      }
                    />
                  ) : (
                    <div className='min-w-0'>
                      <div className='text-sm font-medium break-words'>
                        {row.name}
                      </div>
                      <div className='mt-1 flex flex-wrap gap-1'>
                        {row.sources.map((source) => (
                          <StatusBadge
                            key={source}
                            copyable={false}
                            variant={
                              source === 'registry_only' ? 'neutral' : 'info'
                            }
                          >
                            {t(`qy_mg_source_${source}`)}
                          </StatusBadge>
                        ))}
                        {row.hasRoute === false && (
                          <StatusBadge variant='danger' copyable={false}>
                            {t('qy_gs_no_channels')}
                          </StatusBadge>
                        )}
                        {row.legacyDual && (
                          <StatusBadge variant='warning' copyable={false}>
                            {t('qy_mg_legacy_dual')}
                          </StatusBadge>
                        )}
                        {row.autoPosition > 0 && (
                          <StatusBadge variant='neutral' copyable={false}>
                            {t('Position {{position}}', {
                              position: row.autoPosition,
                            })}
                          </StatusBadge>
                        )}
                      </div>
                    </div>
                  ),
              },
              {
                id: 'ratio',
                header: t('qy_gs_col_base_ratio'),
                className: 'w-32',
                cell: (row) =>
                  row.ratio === null ? (
                    // 「不在 GroupRatio 里」不是 1，也不是空 —— 它是一个正在
                    // 静默按 1.0 扣费的状态。给一个显式的「补上倍率」动作，
                    // 而不是一个看起来已经填好的输入框。
                    <Button
                      variant='outline'
                      size='sm'
                      onClick={() => updateRow(row.id, { ratio: '1' })}
                    >
                      {t('qy_gs_add_missing_ratio')}
                    </Button>
                  ) : (
                    <Input
                      type='number'
                      min={0}
                      step={0.1}
                      value={row.ratio}
                      aria-label={t('qy_gs_col_base_ratio')}
                      aria-invalid={invalidRatios.includes(row.name.trim())}
                      onChange={(event) =>
                        updateRow(row.id, { ratio: event.target.value })
                      }
                    />
                  ),
              },
              {
                /*
                  「用户可选」= 这个名字在 `options.UserUsableGroups` 里有没有键。

                  它是用户分组**没设**可用清单时的判据（项目方原话：「若没有配置
                  模型分组，则用户可选选中的…全部都可以选择」），所以它必须与
                  用户分组页那一列的「按模型分组的用户可选来」是同一份数据。
                */
                id: 'selectable',
                header: t('User selectable'),
                className: 'w-28 text-center',
                cellClassName: 'text-center',
                cell: (row) => (
                  <Switch
                    checked={row.selectable}
                    aria-label={t('User selectable')}
                    onCheckedChange={(checked) =>
                      updateRow(row.id, { selectable: checked })
                    }
                  />
                ),
              },
              {
                id: 'note',
                header: t('qy_mg_col_note'),
                className: 'min-w-64',
                cell: (row) => (
                  <QyMgNoteCell
                    row={row}
                    isSaving={
                      noteMutation.isPending &&
                      noteMutation.variables?.name === row.name
                    }
                    onDraftChange={(value) =>
                      updateRow(row.id, { note: value })
                    }
                    onSave={(value) =>
                      noteMutation.mutate({ name: row.name, note: value })
                    }
                  />
                ),
              },
              {
                id: 'actions',
                header: t('Actions'),
                className: 'text-right',
                cellClassName: 'text-right',
                cell: (row) =>
                  row.isNew ? (
                    // 没保存过的行只需要从本地列表里去掉，没有任何服务端引用。
                    <Button
                      variant='ghost'
                      size='sm'
                      aria-label={t('Delete')}
                      onClick={() =>
                        setRows((current) =>
                          current.filter((item) => item.id !== row.id)
                        )
                      }
                    >
                      <Trash2 className='h-4 w-4' />
                    </Button>
                  ) : (
                    /*
                      已存在的行走**联动删除**，删之前强制看一次影响面。

                      合并之前这里有两个垃圾桶：倍率表那个只把一行从
                      `options.GroupRatio` 里去掉（其余十一处引用原样留着，
                      而那个分组从此按凭空的 1.0 计费），登记表那个才是真的删。
                      两个长得一样、后果差一个量级 —— 留后者。
                    */
                    <Button
                      variant='ghost'
                      size='sm'
                      aria-label={t('Delete')}
                      disabled={!row.registered}
                      title={
                        row.registered
                          ? undefined
                          : t('qy_mg_delete_no_registry')
                      }
                      onClick={() => {
                        // 删除会让服务端重建整张表。同屏未保存的改动因此会被
                        // 复原，而屏幕上只有一句「已删除」——先把这件事说出来。
                        if (hasUnsavedEdits) {
                          toast.warning(t('qy_mg_delete_discards_draft'), {
                            duration: Number.POSITIVE_INFINITY,
                          })
                        }
                        setDeleting(row.name)
                      }}
                    >
                      <Trash2 className='h-4 w-4' />
                    </Button>
                  ),
              },
            ]}
          />
        </CardContent>
      </Card>

      <Card>
        <CardHeader className='border-b'>
          <CardTitle>{t('Auto assignment order')}</CardTitle>
          <CardDescription>
            {t(
              'Priority order for tokens in the auto group. The system tries groups from top to bottom.'
            )}
          </CardDescription>
        </CardHeader>
        <CardContent className='space-y-4'>
          <div className='space-y-1.5'>
            <Label htmlFor='max-token-auto-groups'>
              {t('Maximum custom groups per token')}
            </Label>
            <Input
              id='max-token-auto-groups'
              type='number'
              min={1}
              step={1}
              value={maxTokenAutoGroups}
              aria-invalid={maxInvalid}
              onChange={(event) => setMaxTokenAutoGroups(event.target.value)}
            />
            <p className='text-muted-foreground text-xs leading-5'>
              {maxInvalid
                ? t('Enter a positive integer')
                : t(
                    'Limits only token-specific Auto snapshots. Global Auto inheritance remains unlimited.'
                  )}
            </p>
          </div>

          <Select
            value={null}
            onValueChange={(value) => {
              if (typeof value !== 'string' || value === '') return
              setAutoGroups((current) =>
                current.includes(value) ? current : [...current, value]
              )
            }}
          >
            <SelectTrigger className='w-56'>
              <SelectValue placeholder={t('Add group')} />
            </SelectTrigger>
            <SelectContent alignItemWithTrigger={false}>
              <SelectGroup>
                {autoCandidates.map((name) => (
                  <SelectItem key={name} value={name}>
                    {name}
                  </SelectItem>
                ))}
              </SelectGroup>
            </SelectContent>
          </Select>

          {autoGroups.length > 0 && (
            <div className='space-y-2'>
              {autoGroups.map((group, index) => (
                <div
                  key={group}
                  className='flex items-center gap-2 rounded-md border p-3'
                >
                  <GripVertical className='text-muted-foreground h-4 w-4' />
                  <span className='font-medium'>{group}</span>
                  {!rows.some((row) => row.name.trim() === group) && (
                    <StatusBadge variant='danger' copyable={false}>
                      <AlertTriangle className='mr-1 h-3 w-3' />
                      {t('Not in pricing table')}
                    </StatusBadge>
                  )}
                  <div className='ml-auto flex gap-1'>
                    <Button
                      variant='ghost'
                      size='sm'
                      disabled={index === 0}
                      onClick={() =>
                        setAutoGroups((current) =>
                          moveAutoGroup(current, index, 'up')
                        )
                      }
                    >
                      ↑
                    </Button>
                    <Button
                      variant='ghost'
                      size='sm'
                      disabled={index === autoGroups.length - 1}
                      onClick={() =>
                        setAutoGroups((current) =>
                          moveAutoGroup(current, index, 'down')
                        )
                      }
                    >
                      ↓
                    </Button>
                    <Button
                      variant='ghost'
                      size='sm'
                      aria-label={t('Delete')}
                      onClick={() =>
                        setAutoGroups((current) =>
                          current.filter((_, position) => position !== index)
                        )
                      }
                    >
                      <Trash2 className='h-4 w-4' />
                    </Button>
                  </div>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>

      <QyMgDeleteDialog
        open={deleting != null}
        onOpenChange={(open) => {
          if (!open) closeDelete()
        }}
        impact={impactQuery.data ?? null}
        isLoading={impactQuery.isFetching}
        isDeleting={deleteMutation.isPending}
        forceHasRoute={forceHasRoute}
        forceOrphanTokens={forceOrphanTokens}
        onForceHasRouteChange={setForceHasRoute}
        onForceOrphanTokensChange={setForceOrphanTokens}
        onConfirm={() => {
          if (deleting == null) return
          deleteMutation.mutate(deleting)
        }}
      />
    </SettingsSection>
  )
}

/**
 * 分组备注单元格。
 *
 * ── 它是这句文案**唯一**的编辑面 ──
 *
 * 站上此前有两份用户侧文案：这里的 `note`，以及 `options.UserUsableGroups` 的
 * value（上游本来就把它当说明用，本站里有真实内容，如「浅夜自有分组用户数据，
 * 本站均不会留存…」）。两份并存时，无论谁覆盖谁，界面上都要多解释一条覆盖规则，
 * 而"一份文案两个来源"正是这次要消掉的复杂度。
 *
 * 口径拍定为：**`note` 唯一**。理由是它能表达项目方要的那条优先级链的第二层，
 * 而 `UserUsableGroups` 的 value 是 per-模型分组 的，结构上表达不了第一层
 * （用户分组 × 模型分组 的按格备注）—— 留着它只会多一条永远排第三的路。
 *
 * 历史数据不靠迁移脚本：`note` 为空而旧文案非空的行上给一句灰字与一个
 * 「采用为分组备注」的按钮，运营点一次就把它搬进唯一的那个来源。搬完之前，
 * 用户看到的仍然是旧文案（后端在 `note` 为空时回落它），所以这里必须**说出来**
 * 它此刻正在生效，而不是让运营对着一个空输入框以为没人配过。
 *
 * 备注**单独一个保存键**，不跟着页面顶部那个走：它落的是登记表
 * （`qy_model_groups`），页面顶部那个写的是上游 `options`。两者失败方式完全不同，
 * 合成一次点击的话，一半成功一半失败时界面上只有一句「已保存」。
 */
function QyMgNoteCell(props: {
  row: QyMgMergedRow
  isSaving: boolean
  onDraftChange: (value: string) => void
  onSave: (value: string) => void
}) {
  const { t } = useTranslation()
  const { row } = props
  const legacy = row.usableDescription.trim()
  const showLegacy = legacy !== '' && row.note.trim() === ''

  return (
    <div className='min-w-0 space-y-1'>
      <div className='flex items-center gap-2'>
        {/*
          未登记的行（刚用「新建分组」加出来的、以及只存在于 options 里的历史
          名字）在 `qy_model_groups` 里没有行，备注端点无从下手。禁用而不是隐藏，
          但**必须带上 title**：新建流程必然命中这一档，而一个灰掉且不作任何解释
          的输入框会让运营以为这一列坏了，而不是"先按顶部保存"。
        */}
        <Input
          value={row.note}
          disabled={!row.registered}
          title={row.registered ? undefined : t('qy_mg_note_needs_registry')}
          aria-label={t('qy_mg_col_note')}
          placeholder={t('qy_mg_note_placeholder')}
          onChange={(event) => props.onDraftChange(event.target.value)}
        />
        <Button
          size='sm'
          variant='outline'
          disabled={!row.registered || props.isSaving}
          title={row.registered ? undefined : t('qy_mg_note_needs_registry')}
          onClick={() => props.onSave(row.note)}
        >
          {props.isSaving ? t('Saving...') : t('Save')}
        </Button>
      </div>
      {!row.registered && (
        <p className='text-muted-foreground text-[11px] leading-4'>
          {t('qy_mg_note_needs_registry')}
        </p>
      )}
      {showLegacy && (
        <p className='text-muted-foreground flex flex-wrap items-center gap-1 text-xs leading-5'>
          <span>{t('qy_mg_note_legacy_active', { text: legacy })}</span>
          <Button
            size='sm'
            variant='ghost'
            className='h-6 px-1.5 text-xs'
            disabled={!row.registered}
            onClick={() => props.onDraftChange(legacy)}
          >
            {t('qy_mg_note_adopt_legacy')}
          </Button>
        </p>
      )}
    </div>
  )
}
