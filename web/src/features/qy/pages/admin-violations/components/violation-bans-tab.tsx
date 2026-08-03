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
import { UserCheck, UserX } from 'lucide-react'
import { useEffect, useId, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { StaticDataTable } from '@/components/data-table'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'

import { QyPageBoundary } from '../../../components/qy-page-boundary'
import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { qyKeys } from '../../../lib/query-keys'
import { QyPager } from '../../components/qy-pager'
import { qyOpsErrorMessage } from '../../ops/errors'
import { formatQyTs, QY_EMPTY_TEXT } from '../../ops/format'
import { QyFilterBar, QyFilterField, QyKeyValue } from '../../ops/qy-ops-ui'
import { listQyViolationBans, unbanQyViolationUser } from '../api'
import type { QyViolationBan } from '../types'

const PAGE_SIZE = 20
const ALL = 'all'

const BAN_STATUS_VARIANT: Record<
  string,
  'danger' | 'neutral' | 'success' | 'warning'
> = {
  pending: 'warning',
  banned: 'danger',
  failed: 'danger',
  skipped: 'neutral',
  unbanned: 'success',
}

/** 只有这三种状态还「挂着」，需要人工解除。 */
const UNBANNABLE = new Set(['banned', 'pending', 'failed'])

export function QyViolationBansTab() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [page, setPage] = useState(1)
  const [status, setStatus] = useState(ALL)
  const [userId, setUserId] = useState('')
  const [target, setTarget] = useState<QyViolationBan | null>(null)

  const params = useMemo(
    () => ({
      p: page,
      page_size: PAGE_SIZE,
      status: status === ALL ? undefined : status,
      user_id: userId.trim() === '' ? undefined : Number(userId),
    }),
    [page, status, userId]
  )

  const query = useQuery({
    queryKey: qyKeys.adminViolationBans(params),
    queryFn: () => listQyViolationBans(params),
    staleTime: 15_000,
  })

  const bans = query.data?.items ?? []

  return (
    <div className='space-y-3'>
      <QyFilterBar>
        <QyFilterField label={t('qy_common_status')}>
          <Select
            value={status}
            onValueChange={(value) => {
              setStatus(value ?? ALL)
              setPage(1)
            }}
          >
            <SelectTrigger className='w-36'>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>{t('qy_common_all')}</SelectItem>
              <SelectItem value='banned'>{t('qy_vio_ban_banned')}</SelectItem>
              <SelectItem value='pending'>{t('qy_vio_ban_pending')}</SelectItem>
              <SelectItem value='failed'>{t('qy_vio_ban_failed')}</SelectItem>
              <SelectItem value='unbanned'>
                {t('qy_vio_ban_unbanned')}
              </SelectItem>
              <SelectItem value='skipped'>{t('qy_vio_ban_skipped')}</SelectItem>
            </SelectContent>
          </Select>
        </QyFilterField>
        <QyFilterField label={t('qy_vio_filter_user_id')}>
          <Input
            className='w-28'
            inputMode='numeric'
            value={userId}
            onChange={(event) => {
              setUserId(event.target.value.replaceAll(/\D/g, ''))
              setPage(1)
            }}
          />
        </QyFilterField>
      </QyFilterBar>

      <QyPageBoundary
        query={query}
        isEmpty={query.data != null && bans.length === 0}
        emptyIcon={UserX}
        emptyTitle={t('qy_vio_bans_empty')}
      >
        <div className='space-y-3'>
          <StaticDataTable
            data={bans}
            getRowKey={(row) => row.id}
            columns={[
              {
                id: 'created_at',
                header: t('qy_common_time'),
                cell: (row: QyViolationBan) => formatQyTs(row.created_at),
              },
              {
                id: 'user',
                header: t('qy_common_user'),
                cell: (row: QyViolationBan) => `#${row.user_id}`,
              },
              {
                id: 'hits',
                header: t('qy_vio_ban_hits'),
                cellClassName: 'tabular-nums',
                cell: (row: QyViolationBan) =>
                  `${row.hit_count_at} / ${row.threshold}`,
              },
              {
                id: 'cycle',
                header: t('qy_vio_ban_cycle'),
                cellClassName: 'tabular-nums',
                cell: (row: QyViolationBan) => row.ban_cycle,
              },
              {
                id: 'status',
                header: t('qy_common_status'),
                cell: (row: QyViolationBan) => (
                  <StatusBadge
                    label={t(`qy_vio_ban_${row.status}`, {
                      defaultValue: row.status,
                    })}
                    variant={BAN_STATUS_VARIANT[row.status] ?? 'neutral'}
                    copyable={false}
                  />
                ),
              },
              {
                id: 'banned_at',
                header: t('qy_vio_ban_banned_at'),
                cell: (row: QyViolationBan) =>
                  row.banned_at === 0
                    ? QY_EMPTY_TEXT
                    : formatQyTs(row.banned_at),
              },
              {
                id: 'last_error',
                header: t('qy_vio_ban_last_error'),
                cell: (row: QyViolationBan) =>
                  row.last_error === '' ? QY_EMPTY_TEXT : row.last_error,
              },
              {
                id: 'actions',
                header: t('qy_common_actions'),
                cell: (row: QyViolationBan) => (
                  <Button
                    type='button'
                    variant='ghost'
                    size='icon-sm'
                    aria-label={t('qy_vio_unban_title')}
                    disabled={!UNBANNABLE.has(row.status)}
                    onClick={() => setTarget(row)}
                  >
                    <UserCheck aria-hidden='true' />
                  </Button>
                ),
              },
            ]}
          />

          <QyPager
            page={page}
            pageSize={PAGE_SIZE}
            total={query.data?.total ?? 0}
            onPageChange={setPage}
          />
        </div>
      </QyPageBoundary>

      <QyUnbanDialog
        ban={target}
        onClose={() => setTarget(null)}
        onDone={() => {
          void queryClient.invalidateQueries({ queryKey: qyKeys.all })
        }}
      />
    </div>
  )
}

/**
 * 解封。
 *
 * `reset_counter` 决定是否把滚动窗口的违规计数清零。不清零的话，用户解封后
 * 只要再命中一次就会立刻二次封号 —— 对确认误判的场景必须勾上。
 */
function QyUnbanDialog(props: {
  ban: QyViolationBan | null
  onClose: () => void
  onDone: () => void
}) {
  const { t } = useTranslation()
  const resetId = useId()
  const [note, setNote] = useState('')
  const [resetCounter, setResetCounter] = useState(false)

  useEffect(() => {
    setNote('')
    setResetCounter(false)
  }, [props.ban])

  const mutation = useMutation({
    mutationFn: () =>
      unbanQyViolationUser(props.ban?.user_id ?? 0, {
        note,
        reset_counter: resetCounter,
      }),
    onSuccess: () => {
      toast.success(t('qy_vio_unban_done'))
      props.onDone()
      props.onClose()
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  return (
    <QyResponsiveDialog
      open={props.ban != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('qy_vio_unban_title')}
      description={t('qy_vio_unban_desc')}
      footer={
        <>
          <Button type='button' variant='outline' onClick={props.onClose}>
            {t('qy_common_cancel')}
          </Button>
          <Button
            type='button'
            disabled={mutation.isPending}
            onClick={() => mutation.mutate()}
          >
            {t('qy_vio_unban_confirm')}
          </Button>
        </>
      }
    >
      <div className='space-y-3'>
        <div>
          <QyKeyValue label={t('qy_common_user')}>
            {`#${props.ban?.user_id ?? 0}`}
          </QyKeyValue>
          <QyKeyValue label={t('qy_vio_ban_hits')}>
            {`${props.ban?.hit_count_at ?? 0} / ${props.ban?.threshold ?? 0}`}
          </QyKeyValue>
        </div>

        <div className='space-y-1'>
          <Label htmlFor={`${resetId}-note`}>{t('qy_common_remark')}</Label>
          <Textarea
            id={`${resetId}-note`}
            rows={3}
            value={note}
            onChange={(event) => setNote(event.target.value)}
          />
        </div>

        <div className='flex items-start justify-between gap-4 rounded-lg border p-3'>
          <div className='min-w-0'>
            <Label htmlFor={resetId}>{t('qy_vio_unban_reset')}</Label>
            <p className='text-muted-foreground text-xs'>
              {t('qy_vio_unban_reset_desc')}
            </p>
          </div>
          <Switch
            id={resetId}
            checked={resetCounter}
            onCheckedChange={setResetCounter}
          />
        </div>
      </div>
    </QyResponsiveDialog>
  )
}
