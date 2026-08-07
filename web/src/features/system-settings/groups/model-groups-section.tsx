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
import { AlertTriangle, GripVertical, Plus, Trash2 } from 'lucide-react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

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
import { QyGroupMatrixHint } from '@/features/qy/components/qy-group-matrix-hint'
import { qyGroupOptionsQuery } from '@/features/qy/lib/group-options'
import { qyGmMatrixQuery } from '@/features/qy/pages/admin-group-matrix/api'

import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { GroupOptionsJsonDrawer } from './components/group-options-json-drawer'
import { GroupPricingGuideButton } from './components/group-pricing-guide'
import {
  buildModelGroupRows,
  duplicateRowNames,
  freeModelGroups,
  invalidRatioRowNames,
  modelGroupsMissingRatio,
  moveAutoGroup,
  nextRowId,
  parseAutoGroups,
  serializeAutoGroups,
  serializeModelGroupRows,
  type ModelGroupRow,
  type MODEL_GROUP_PAGE_KEYS,
} from './lib/group-options'
import { useGroupOptionSave } from './lib/use-group-option-save'

export type ModelGroupsSectionValues = {
  GroupRatio: string
  AutoGroups: string
  MaxTokenAutoGroups: number
  /** 只读：全局可选清单归 B 页编辑，这里只用它建行并显示一个徽标。 */
  UserUsableGroups: string
}

/**
 * C「模型分组」页。
 *
 * ── 这一页的主语是「一批渠道」──
 *
 * 模型分组决定一次请求**路由到哪批渠道**、按**哪个兜底倍率**结账。
 * 它与「哪一档人」无关 —— 后者在用户分组页，两者的交叉在矩阵页。
 *
 * ── 为什么把「渠道数」这一列做成实时反查而不是一个可配置字段 ──
 *
 * 「这个模型分组底下还有没有池子」是**事实**不是配置，而它正是 503 的直接
 * 原因：一个有倍率、没渠道的模型分组在令牌下拉里长得和正常的一模一样，
 * 选中之后每一次请求都 503。这一列只能来自实时探测，落成一个字段就一定会
 * 与现实不同步 —— 而不同步的方向恰好是「界面说有、线上没有」。
 *
 * 探测走扩展的分组候选端点（与划转分组规则页同源）。拉不到时这一列显示 `—`
 * 并且**不出任何告警**：把「不确定」画成「没有渠道」，整张表会挂满假警报，
 * 而假警报比没有警报更糟。
 */
export function ModelGroupsSection(props: {
  defaultValues: ModelGroupsSectionValues
}) {
  const { t } = useTranslation()
  const { defaultValues } = props

  const [rows, setRows] = useState<ModelGroupRow[]>(() =>
    buildModelGroupRows(
      defaultValues.GroupRatio,
      defaultValues.UserUsableGroups
    )
  )
  const [autoGroups, setAutoGroups] = useState<string[]>(() =>
    parseAutoGroups(defaultValues.AutoGroups)
  )
  const [maxTokenAutoGroups, setMaxTokenAutoGroups] = useState(
    String(defaultValues.MaxTokenAutoGroups)
  )

  const probeQuery = useQuery({ ...qyGroupOptionsQuery(), retry: false })
  const matrixQuery = useQuery({ ...qyGmMatrixQuery(), retry: false })

  const channelsByGroup = useMemo(() => {
    const map = new Map<string, boolean>()
    for (const option of probeQuery.data?.options ?? []) {
      map.set(option.name, option.has_channels)
    }
    return map
  }, [probeQuery.data])
  const probeOk = probeQuery.data?.probe_ok === true

  /** 模型分组 → 有哪几档人被授予了它。反查矩阵格子，只读。 */
  const reachableBy = useMemo(() => {
    const map = new Map<string, number>()
    for (const cell of matrixQuery.data?.cells ?? []) {
      if (!cell.granted) continue
      map.set(cell.model_group, (map.get(cell.model_group) ?? 0) + 1)
    }
    return map
  }, [matrixQuery.data])

  const usableNames = useMemo(() => {
    try {
      const parsed: unknown = JSON.parse(defaultValues.UserUsableGroups || '{}')
      if (typeof parsed !== 'object' || parsed === null) {
        return new Set<string>()
      }
      return new Set(Object.keys(parsed as Record<string, unknown>))
    } catch {
      return new Set<string>()
    }
  }, [defaultValues.UserUsableGroups])

  const duplicates = useMemo(() => duplicateRowNames(rows), [rows])
  const invalidRatios = useMemo(() => invalidRatioRowNames(rows), [rows])
  const missingRatio = useMemo(() => modelGroupsMissingRatio(rows), [rows])
  const freeGroups = useMemo(() => freeModelGroups(rows), [rows])
  const emptyPools = useMemo(() => {
    if (!probeOk) return []
    return rows
      .filter((row) => row.ratio !== null)
      .map((row) => row.name.trim())
      .filter((name) => name !== '' && channelsByGroup.get(name) === false)
  }, [rows, channelsByGroup, probeOk])

  const parsedMax = Number(maxTokenAutoGroups)
  const maxInvalid =
    !Number.isInteger(parsedMax) || parsedMax < 1 || maxTokenAutoGroups === ''

  // 键域由归属清单给出，多写一个键即编译失败。理由见 `user-groups-section`。
  const { save, resetBaseline, isSaving } = useGroupOptionSave<
    (typeof MODEL_GROUP_PAGE_KEYS)[number]
  >({
    GroupRatio: defaultValues.GroupRatio,
    AutoGroups: defaultValues.AutoGroups,
    MaxTokenAutoGroups: defaultValues.MaxTokenAutoGroups,
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
      buildModelGroupRows(
        defaultValues.GroupRatio,
        defaultValues.UserUsableGroups
      )
    )
    setAutoGroups(parseAutoGroups(defaultValues.AutoGroups))
    setMaxTokenAutoGroups(String(defaultValues.MaxTokenAutoGroups))
    resetBaseline({
      GroupRatio: defaultValues.GroupRatio,
      AutoGroups: defaultValues.AutoGroups,
      MaxTokenAutoGroups: defaultValues.MaxTokenAutoGroups,
    })
  }, [
    defaultValues.GroupRatio,
    defaultValues.AutoGroups,
    defaultValues.MaxTokenAutoGroups,
    defaultValues.UserUsableGroups,
    resetBaseline,
  ])

  const updateRow = useCallback((id: string, patch: Partial<ModelGroupRow>) => {
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
      // id 走单调自增序列，**不从名字派生**：名字随后可以被改，而重名检测只看
      // name 不看 id，两行拿到同一个 id 之后 updateRow / 删除会同时命中两行 ——
      // 给第二行填 0.5 会把第一行的兜底倍率也一起改成 0.5，那是直接改钱。
      return [...current, { id: nextRowId('mg'), name, ratio: '1' }]
    })
  }, [])

  const handleSave = useCallback(() => {
    void save({
      GroupRatio: serializeModelGroupRows(rows),
      AutoGroups: serializeAutoGroups(autoGroups),
      MaxTokenAutoGroups: parsedMax,
    })
  }, [save, rows, autoGroups, parsedMax])

  const autoCandidates = useMemo(
    () =>
      rows
        .map((row) => row.name.trim())
        .filter((name) => name !== '' && !autoGroups.includes(name)),
    [rows, autoGroups]
  )

  const saveBlocked =
    duplicates.length > 0 || invalidRatios.length > 0 || maxInvalid

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
                value: serializeModelGroupRows(rows),
              },
              {
                key: 'AutoGroups',
                label: t('Auto assignment order'),
                value: serializeAutoGroups(autoGroups),
              },
              {
                key: 'UserUsableGroups',
                label: t('Selectable groups'),
                value: defaultValues.UserUsableGroups,
                readOnly: true,
                description: t('qy_gs_where_usable_list'),
              },
            ]}
            onApply={(next) => {
              if (next.GroupRatio !== undefined) {
                setRows(
                  buildModelGroupRows(
                    next.GroupRatio,
                    defaultValues.UserUsableGroups
                  )
                )
              }
              if (next.AutoGroups !== undefined) {
                setAutoGroups(parseAutoGroups(next.AutoGroups))
              }
            }}
          />
        </div>
      </div>

      {/*
        拆页之前这一页上还有「分组间倍率覆盖」与「特殊可用分组规则」两段编辑
        框。它们搬去了矩阵页，而数据仍在原处实时参与计费 —— 原地留一块指路牌，
        否则运营找不到入口就会认定「功能没了」。
      */}
      <QyGroupMatrixHint />

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

      {missingRatio.length > 0 && (
        <Alert>
          <AlertTriangle className='h-4 w-4' />
          <AlertTitle>{t('qy_gs_missing_ratio_title')}</AlertTitle>
          <AlertDescription>
            {t('qy_gs_missing_ratio_warn', { names: missingRatio.join(', ') })}
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
              <CardDescription>{t('qy_gs_model_table_desc')}</CardDescription>
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
                className: 'min-w-40',
                cell: (row) => (
                  <Input
                    value={row.name}
                    aria-label={t('Group name')}
                    aria-invalid={duplicates.includes(row.name.trim())}
                    onChange={(event) =>
                      updateRow(row.id, { name: event.target.value })
                    }
                  />
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
                id: 'channels',
                header: t('qy_gs_col_channels'),
                className: 'w-28 text-center',
                cellClassName: 'text-center',
                cell: (row) => {
                  if (!probeOk) return <span>—</span>
                  const has = channelsByGroup.get(row.name.trim())
                  if (has === undefined) return <span>—</span>
                  return has ? (
                    <StatusBadge variant='success' copyable={false}>
                      {t('qy_gs_has_channels')}
                    </StatusBadge>
                  ) : (
                    <StatusBadge variant='danger' copyable={false}>
                      {t('qy_gs_no_channels')}
                    </StatusBadge>
                  )
                },
              },
              {
                id: 'selectable',
                header: t('User selectable'),
                className: 'w-28 text-center',
                cellClassName: 'text-center',
                cell: (row) =>
                  usableNames.has(row.name.trim()) ? t('Yes') : t('No'),
              },
              {
                id: 'auto',
                header: t('Auto assignment order'),
                className: 'w-28 text-center',
                cellClassName: 'text-center',
                cell: (row) => {
                  const index = autoGroups.indexOf(row.name.trim())
                  return index < 0
                    ? t('Not included')
                    : t('Position {{position}}', { position: index + 1 })
                },
              },
              {
                id: 'reachable',
                header: t('qy_gs_col_used_by'),
                className: 'w-32 text-center',
                cellClassName: 'text-center tabular-nums',
                cell: (row) => {
                  if (matrixQuery.data == null) return <span>—</span>
                  return reachableBy.get(row.name.trim()) ?? 0
                },
              },
              {
                id: 'actions',
                header: t('Actions'),
                className: 'text-right',
                cellClassName: 'text-right',
                cell: (row) => (
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
                ),
              },
            ]}
          />
          <p className='text-muted-foreground mt-3 text-xs leading-5'>
            {t('qy_gs_delete_row_hint')}{' '}
            <Link
              to='/system-settings/billing/$section'
              params={{ section: 'group-matrix' }}
              className='text-primary underline underline-offset-2'
            >
              {t('qy_gs_group_matrix_title')}
            </Link>
          </p>
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
    </SettingsSection>
  )
}
