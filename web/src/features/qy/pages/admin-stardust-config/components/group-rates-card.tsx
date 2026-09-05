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
import { Layers } from 'lucide-react'
import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import {
  StaticDataTable,
  staticDataTableClassNames,
  type StaticDataTableColumn,
} from '@/components/data-table'
import { Badge } from '@/components/ui/badge'
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
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Switch } from '@/components/ui/switch'

import { QyConfirmDialog } from '../../../components/qy-confirm-dialog'
import { QyPageBoundary } from '../../../components/qy-page-boundary'
import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { qyArray } from '../../../lib/array'
import { qyKeys } from '../../../lib/query-keys'
import { formatQyTs } from '../../ops/format'
import { qySdBpsPercent } from '../../stardust/lib/display'
import {
  deleteQyStardustGroupRate,
  putQyStardustGroupRate,
  qyAdminStardustGroupRatesQuery,
} from '../api'
import { qySdBoundContains, qySdParseDraftText } from '../lib/draft'
import { qySdAdminErrorMessage } from '../lib/errors'
import type {
  QyStardustBound,
  QyStardustGroupRate,
  QyStardustGroupRateInput,
} from '../types'

/** 四个可覆盖的比例列，顺序 = 表格列顺序 = 弹窗字段顺序。 */
const RATE_KEYS = [
  'consume_bps',
  'invite_consume_bps',
  'invite_topup_bps',
  'invite_redeem_bps',
] as const
type RateKey = (typeof RATE_KEYS)[number]

/**
 * 「分组比例」—— 按用户分组覆盖四档比例（`qy_sd_group_rate`）。
 *
 * 每一项都是三态：`null` = 本档不覆盖、沿用全站默认；`0` = 显式不返；正数 = 覆盖。
 * 界面上空输入框就是 `null`，所以"清空"与"填 0"必须是两个看得出来的动作 ——
 * 运营最常犯的错正是把"想关掉"填成了"想回落"。
 *
 * 分组名只能从后端下发的 `groups`（在册 ∪ 登记表）里选，不给自由文本：
 * 打错一个字母的比例行永远不会命中，而界面上看起来"已配置"。
 */
export function QySdGroupRatesCard(props: {
  bpsBound: QyStardustBound | null
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const query = useQuery(qyAdminStardustGroupRatesQuery())
  const items = qyArray(query.data?.items)
  const groups = qyArray(query.data?.groups)

  const [editing, setEditing] = useState<QyStardustGroupRate | 'new' | null>(
    null
  )
  const [deleteTarget, setDeleteTarget] = useState<string | null>(null)

  const remove = useMutation({
    mutationFn: deleteQyStardustGroupRate,
    onSuccess: async (result) => {
      setDeleteTarget(null)
      toast.success(
        result.deleted
          ? t('qy_sdadm_gr_deleted', { group: result.user_group })
          : t('qy_sdadm_gr_delete_noop', { group: result.user_group })
      )
      await queryClient.invalidateQueries({
        queryKey: qyKeys.adminStardustGroupRates(),
      })
    },
    onError: (error) => toast.error(qySdAdminErrorMessage(error, t)),
  })

  const configured = new Set(items.map((row) => row.user_group))
  const freeGroups = groups.filter((name) => !configured.has(name))

  const columns: StaticDataTableColumn<QyStardustGroupRate>[] = [
    {
      id: 'group',
      header: t('qy_sdadm_gr_col_group'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => (
        <span className='inline-flex items-center gap-1.5'>
          <span className='font-mono'>{row.user_group}</span>
          {!row.enabled && (
            <Badge variant='outline'>{t('qy_sdadm_gr_disabled')}</Badge>
          )}
        </span>
      ),
    },
    ...RATE_KEYS.map(
      (key): StaticDataTableColumn<QyStardustGroupRate> => ({
        id: key,
        header: t(`qy_sdadm_cfg_k_${key}`, { defaultValue: key }),
        className: staticDataTableClassNames.compactHeaderCellRight,
        cellClassName: staticDataTableClassNames.compactNumericCell,
        cell: (row) => <RateCell value={row[key]} />,
      })
    ),
    {
      id: 'updated_at',
      header: t('qy_common_updated_at'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCell,
      cell: (row) => formatQyTs(row.updated_at),
    },
    {
      id: 'actions',
      header: t('qy_common_actions'),
      className: staticDataTableClassNames.actionHeaderCell,
      cellClassName: staticDataTableClassNames.actionCell,
      cell: (row) => (
        <div className='flex justify-end gap-1'>
          <Button variant='ghost' size='sm' onClick={() => setEditing(row)}>
            {t('qy_common_edit')}
          </Button>
          <Button
            variant='ghost'
            size='sm'
            onClick={() => setDeleteTarget(row.user_group)}
          >
            {t('qy_common_delete')}
          </Button>
        </div>
      ),
    },
  ]

  return (
    <Card data-card-hover='false'>
      <CardHeader>
        <CardTitle>{t('qy_sdadm_gr_title')}</CardTitle>
        <CardDescription>{t('qy_sdadm_gr_desc')}</CardDescription>
      </CardHeader>
      <CardContent className='space-y-3'>
        <div className='flex flex-wrap items-center justify-between gap-2'>
          <p className='text-muted-foreground text-xs'>
            {t('qy_sdadm_gr_null_note')}
          </p>
          <Button
            size='sm'
            variant='outline'
            disabled={query.data == null || freeGroups.length === 0}
            onClick={() => setEditing('new')}
          >
            {t('qy_sdadm_gr_add')}
          </Button>
        </div>

        <QyPageBoundary
          query={query}
          isEmpty={query.data != null && items.length === 0}
          emptyIcon={Layers}
          emptyTitle={t('qy_sdadm_gr_empty_title')}
          emptyDescription={t('qy_sdadm_gr_empty_desc')}
        >
          <div className='w-full overflow-x-auto'>
            <StaticDataTable
              columns={columns}
              data={items}
              getRowKey={(row) => row.user_group}
              tableClassName='min-w-[760px]'
            />
          </div>
        </QyPageBoundary>
      </CardContent>

      <GroupRateDialog
        target={editing}
        freeGroups={freeGroups}
        bpsBound={props.bpsBound}
        onClose={() => setEditing(null)}
      />

      <QyConfirmDialog
        open={deleteTarget != null}
        onOpenChange={(open) => {
          if (!open) setDeleteTarget(null)
        }}
        title={t('qy_sdadm_gr_delete_title')}
        description={t('qy_sdadm_gr_delete_desc', {
          group: deleteTarget ?? '',
        })}
        isLoading={remove.isPending}
        onConfirm={() => {
          if (deleteTarget != null) remove.mutate(deleteTarget)
        }}
      />
    </Card>
  )
}

/** 一格比例：`null` 明说「沿用全站」，数字带百分数。 */
function RateCell(props: { value: number | null }) {
  const { t } = useTranslation()
  if (props.value == null) {
    return (
      <span className='text-muted-foreground'>{t('qy_sdadm_gr_inherit')}</span>
    )
  }
  return (
    <span>
      {t('qy_sdadm_cfg_bps_value', {
        bps: props.value,
        percent: qySdBpsPercent(props.value),
      })}
    </span>
  )
}

type GroupRateDialogProps = {
  /** `'new'` = 新增一行（分组从下拉选）；一行 = 编辑它（分组固定）。 */
  target: QyStardustGroupRate | 'new' | null
  freeGroups: string[]
  bpsBound: QyStardustBound | null
  onClose: () => void
}

/**
 * 新增 / 编辑一个分组的比例档。四个比例是文本草稿：空串 = `null`（沿用全站）。
 * PUT 是整行 upsert，所以编辑时四个格子都要按当前行回填，少填一格就是把它清成 null。
 */
function GroupRateDialog(props: GroupRateDialogProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const groupId = useId()
  const enabledId = useId()

  const [group, setGroup] = useState('')
  const [enabled, setEnabled] = useState(true)
  const [draft, setDraft] = useState<Record<RateKey, string>>({
    consume_bps: '',
    invite_consume_bps: '',
    invite_topup_bps: '',
    invite_redeem_bps: '',
  })

  // 每次打开都按目标行回填：编辑 A 之后接着新增，留着 A 的三个数字会被当成
  // 新分组的默认值写进去。
  useEffect(() => {
    if (props.target == null) return
    if (props.target === 'new') {
      setGroup(props.freeGroups[0] ?? '')
      setEnabled(true)
      setDraft({
        consume_bps: '',
        invite_consume_bps: '',
        invite_topup_bps: '',
        invite_redeem_bps: '',
      })
      return
    }
    const row = props.target
    setGroup(row.user_group)
    setEnabled(row.enabled)
    setDraft({
      consume_bps: row.consume_bps == null ? '' : String(row.consume_bps),
      invite_consume_bps:
        row.invite_consume_bps == null ? '' : String(row.invite_consume_bps),
      invite_topup_bps:
        row.invite_topup_bps == null ? '' : String(row.invite_topup_bps),
      invite_redeem_bps:
        row.invite_redeem_bps == null ? '' : String(row.invite_redeem_bps),
    })
    // freeGroups 只在新增那一支用作默认选项，随列表刷新变化时不该重置正在编辑的草稿。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [props.target])

  const save = useMutation({
    mutationFn: (input: { group: string; body: QyStardustGroupRateInput }) =>
      putQyStardustGroupRate(input.group, input.body),
    onSuccess: async (row) => {
      toast.success(t('qy_sdadm_gr_saved', { group: row.user_group }))
      await queryClient.invalidateQueries({
        queryKey: qyKeys.adminStardustGroupRates(),
      })
      props.onClose()
    },
    onError: (error) => toast.error(qySdAdminErrorMessage(error, t)),
  })

  /** 一格草稿 → `null` / 整数；非法（非数字或越界）→ `undefined`。 */
  const parsed = (key: RateKey): number | null | undefined => {
    const text = draft[key].trim()
    if (text === '') return null
    const value = qySdParseDraftText(text)
    if (value == null) return undefined
    if (props.bpsBound != null && !qySdBoundContains(props.bpsBound, value)) {
      return undefined
    }
    return value
  }
  const invalid = RATE_KEYS.some((key) => parsed(key) === undefined)
  const isNew = props.target === 'new'

  return (
    <QyResponsiveDialog
      open={props.target != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={isNew ? t('qy_sdadm_gr_add') : t('qy_sdadm_gr_edit_title')}
      description={t('qy_sdadm_gr_dialog_desc')}
      contentClassName='sm:max-w-lg'
      footer={
        <Button
          disabled={group === '' || invalid || save.isPending}
          onClick={() =>
            save.mutate({
              group,
              body: {
                consume_bps: parsed('consume_bps') ?? null,
                invite_consume_bps: parsed('invite_consume_bps') ?? null,
                invite_topup_bps: parsed('invite_topup_bps') ?? null,
                invite_redeem_bps: parsed('invite_redeem_bps') ?? null,
                enabled,
              },
            })
          }
        >
          {t('qy_sdadm_cfg_save')}
        </Button>
      }
    >
      <div className='space-y-4'>
        <div className='space-y-1.5'>
          <Label htmlFor={groupId}>{t('qy_sdadm_gr_col_group')}</Label>
          {isNew ? (
            <NativeSelect
              id={groupId}
              className='w-full'
              value={group}
              onChange={(event) => setGroup(event.target.value)}
            >
              {props.freeGroups.map((name) => (
                <NativeSelectOption key={name} value={name}>
                  {name}
                </NativeSelectOption>
              ))}
            </NativeSelect>
          ) : (
            <Input id={groupId} value={group} readOnly />
          )}
        </div>

        {RATE_KEYS.map((key) => {
          const value = parsed(key)
          return (
            <div key={key} className='space-y-1.5'>
              <Label htmlFor={`${groupId}-${key}`}>
                {t(`qy_sdadm_cfg_k_${key}`, { defaultValue: key })}
              </Label>
              <Input
                id={`${groupId}-${key}`}
                inputMode='numeric'
                value={draft[key]}
                placeholder={t('qy_sdadm_gr_inherit')}
                aria-invalid={value === undefined}
                onChange={(event) =>
                  setDraft((prev) => ({
                    ...prev,
                    [key]: event.target.value.replaceAll(/\D/g, ''),
                  }))
                }
              />
              <p className='text-muted-foreground text-xs'>
                {value == null
                  ? t('qy_sdadm_gr_field_inherit_hint')
                  : t('qy_sdadm_cfg_bps_hint', {
                      percent: qySdBpsPercent(value),
                    })}
                {props.bpsBound != null && (
                  <span className='block'>
                    {t('qy_common_amount_range', {
                      min: props.bpsBound.lo,
                      max: props.bpsBound.hi,
                    })}
                  </span>
                )}
              </p>
            </div>
          )
        })}

        <div className='flex items-start justify-between gap-4 rounded-lg border p-3'>
          <div className='min-w-0'>
            <Label htmlFor={enabledId}>{t('qy_sdadm_gr_enabled')}</Label>
            <p className='text-muted-foreground text-xs'>
              {t('qy_sdadm_gr_enabled_hint')}
            </p>
          </div>
          <Switch
            id={enabledId}
            checked={enabled}
            onCheckedChange={(checked) => setEnabled(checked)}
          />
        </div>
      </div>
    </QyResponsiveDialog>
  )
}
