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
import { Pause, Play, Plus, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import { QyConfirmDialog } from '../../../components/qy-confirm-dialog'
import { QyPageBoundary } from '../../../components/qy-page-boundary'
import { qyErrorMessage } from '../../../lib/api'
import { qyKeys } from '../../../lib/query-keys'
import { QyPager } from '../../components/qy-pager'
import { formatQyDuration, formatQyTs, QY_EMPTY_TEXT } from '../../ops/format'
import { QyFilterBar, QyFilterField } from '../../ops/qy-ops-ui'
import {
  qyDeleteRiskWatchTask,
  qyRiskWatchTasksQuery,
  qyStartRiskWatchTask,
  qyStopRiskWatchTask,
} from '../api'
import {
  qyRwPercentText,
  qyRwProgressText,
  qyRwRemainingSeconds,
  qyRwRetentionKind,
} from '../lib/format'
import type { QyRwStatus, QyRwTask } from '../types'
import { QyRwTaskDialog } from './task-dialog'

const PAGE_SIZE = 20
const ALL = '__all__'

const STATUSES: readonly QyRwStatus[] = [
  'running',
  'stopped',
  'finished',
  'expired',
]

/**
 * 监听任务列表。
 *
 * ## 这一页最重要的一列是「已抓 / 上限」
 *
 * 它同时回答两个问题:这个任务还在不在跑、这个账号的行为有多密集。一个设了
 * 500 条上限、两小时就抽满的任务,与一个跑了三天只抓到 37 条的任务,指向的
 * 结论完全不同 —— 前者往往说明请求量远超预期。
 *
 * ## 停止是立即的,状态可能晚几秒
 *
 * 点「停止」之后后端会立刻重载各节点的内存快照,所以抓取当场就停。但一个
 * 时间窗自然到期的任务,状态那一格要等后台巡检(一分钟一轮)才翻成「已到期」——
 * 那一分钟里它显示「运行中」而实际早就不抓了。这条差异是刻意的:抓不抓由
 * 每个节点自己的时钟说了算(立即),状态是一份共享的展示(要有人去改)。
 */
export function QyRwTaskCard(props: {
  /** 点击某一行的「查看记录」时,把记录卡的筛选切到这个任务上。 */
  onInspect: (taskId: number) => void
}) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const [page, setPage] = useState(1)
  const [status, setStatus] = useState<string>(ALL)
  const [editing, setEditing] = useState<QyRwTask | null>(null)
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<QyRwTask | null>(null)

  const query = useQuery(
    qyRiskWatchTasksQuery({
      p: page,
      page_size: PAGE_SIZE,
      status: status === ALL ? undefined : status,
    })
  )
  const rows = query.data?.items ?? []

  // 三个写动作共用同一条收尾:失效任务列表与总览。不共用的话,停一个任务之后
  // 顶部那个「运行中 N」还是旧的 —— 而那个数字正是 max_active_tasks 的分子。
  const settle = async () => {
    await client.invalidateQueries({ queryKey: qyKeys.adminRiskWatchTasks({}) })
    await client.invalidateQueries({ queryKey: qyKeys.adminRiskWatchStats() })
  }

  const stop = useMutation({
    mutationFn: (task: QyRwTask) => qyStopRiskWatchTask(task.id, task.version),
    onSuccess: async () => {
      toast.success(t('qy_rw_toast_stopped'))
      await settle()
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })
  const start = useMutation({
    mutationFn: (task: QyRwTask) => qyStartRiskWatchTask(task.id, task.version),
    onSuccess: async () => {
      toast.success(t('qy_rw_toast_started'))
      await settle()
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })
  const remove = useMutation({
    mutationFn: (task: QyRwTask) => qyDeleteRiskWatchTask(task.id),
    onSuccess: async () => {
      toast.success(t('qy_rw_toast_deleted'))
      setDeleting(null)
      // 删任务会连带删掉它的全部记录,所以记录列表也要一起失效。
      await client.invalidateQueries({
        queryKey: qyKeys.adminRiskWatchCaptures({}),
      })
      await settle()
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const atCapacity =
    (query.data?.max_active_tasks ?? 0) > 0 &&
    rows.filter((row) => row.status === 'running').length >=
      (query.data?.max_active_tasks ?? 0)

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('qy_rw_task_card_title')}</CardTitle>
        <CardDescription>{t('qy_rw_task_card_desc')}</CardDescription>
      </CardHeader>
      <CardContent className='flex flex-col gap-3'>
        <QyFilterBar>
          <QyFilterField label={t('qy_rw_col_status')}>
            <Select
              value={status}
              onValueChange={(v) => {
                setStatus(v ?? ALL)
                setPage(1)
              }}
            >
              <SelectTrigger className='w-40'>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={ALL}>{t('qy_common_all')}</SelectItem>
                {STATUSES.map((s) => (
                  <SelectItem key={s} value={s}>
                    {t(`qy_rw_status_${s}` as never)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </QyFilterField>
          <div className='ml-auto flex items-end'>
            <Button size='sm' onClick={() => setCreating(true)}>
              <Plus className='size-4' />
              {t('qy_rw_new_task')}
            </Button>
          </div>
        </QyFilterBar>

        {/* 上限提示只在真的到顶时出现。常驻一行"最多 N 个"只会变成背景噪声,
            而到顶那一刻管理员需要知道的是「先停掉一些」而不是「建不了」。 */}
        {atCapacity ? (
          <p className='text-muted-foreground text-xs'>
            {t('qy_rw_at_capacity', {
              max: query.data?.max_active_tasks ?? 0,
            })}
          </p>
        ) : null}

        <QyPageBoundary query={query}>
          <div className='overflow-x-auto'>
            <table className='w-full text-sm'>
              <thead>
                <tr className='text-muted-foreground text-left'>
                  <th className='py-1'>{t('qy_rw_col_name')}</th>
                  <th className='py-1'>{t('qy_rw_col_target')}</th>
                  <th className='py-1'>{t('qy_rw_col_rate')}</th>
                  <th className='py-1'>{t('qy_rw_col_progress')}</th>
                  <th className='py-1'>{t('qy_rw_col_window')}</th>
                  <th className='py-1'>{t('qy_rw_col_retention')}</th>
                  <th className='py-1'>{t('qy_rw_col_status')}</th>
                  <th className='py-1'>{t('qy_common_actions')}</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => (
                  <tr key={row.id} className='border-t align-top'>
                    <td className='py-1'>
                      <div>{row.name}</div>
                      {row.note ? (
                        <div className='text-muted-foreground text-xs'>
                          {row.note}
                        </div>
                      ) : null}
                    </td>
                    <td className='py-1'>
                      <QyRwTargetCell task={row} />
                    </td>
                    <td className='py-1 whitespace-nowrap'>
                      {qyRwPercentText(row.sample_bps)}
                    </td>
                    <td className='py-1 whitespace-nowrap'>
                      {qyRwProgressText(row)}
                    </td>
                    <td className='py-1 whitespace-nowrap'>
                      <QyRwWindowCell task={row} />
                    </td>
                    <td className='py-1 whitespace-nowrap'>
                      <QyRwRetentionCell task={row} />
                    </td>
                    <td className='py-1'>
                      <Badge
                        variant={
                          row.status === 'running' ? 'default' : 'outline'
                        }
                      >
                        {t(`qy_rw_status_${row.status}` as never)}
                      </Badge>
                      {row.status !== 'running' && row.stopped_reason ? (
                        <div className='text-muted-foreground text-xs'>
                          {t(
                            `qy_rw_reason_${row.stopped_reason}` as never,
                            row.stopped_reason
                          )}
                        </div>
                      ) : null}
                    </td>
                    <td className='py-1'>
                      <div className='flex flex-wrap gap-1'>
                        <Button
                          size='sm'
                          variant='outline'
                          onClick={() => props.onInspect(row.id)}
                        >
                          {t('qy_rw_view_captures')}
                        </Button>
                        <Button
                          size='sm'
                          variant='outline'
                          onClick={() => setEditing(row)}
                        >
                          {t('qy_common_edit')}
                        </Button>
                        {row.status === 'running' ? (
                          <Button
                            size='sm'
                            variant='outline'
                            disabled={stop.isPending}
                            onClick={() => stop.mutate(row)}
                          >
                            <Pause className='size-4' />
                            {t('qy_rw_stop')}
                          </Button>
                        ) : (
                          <Button
                            size='sm'
                            variant='outline'
                            disabled={start.isPending}
                            onClick={() => start.mutate(row)}
                          >
                            <Play className='size-4' />
                            {t('qy_rw_start')}
                          </Button>
                        )}
                        <Button
                          size='sm'
                          variant='outline'
                          onClick={() => setDeleting(row)}
                        >
                          <Trash2 className='size-4' />
                          {t('qy_common_delete')}
                        </Button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          <QyPager
            page={page}
            pageSize={PAGE_SIZE}
            total={query.data?.total ?? 0}
            onPageChange={setPage}
            disabled={query.isFetching}
          />
        </QyPageBoundary>
      </CardContent>

      <QyRwTaskDialog
        open={creating || editing != null}
        task={editing}
        maxRetentionDays={query.data?.max_retention_days ?? 0}
        defaultRetention={query.data?.default_retention ?? 0}
        onClose={() => {
          setCreating(false)
          setEditing(null)
        }}
        onSaved={settle}
      />

      {/* 删除是这一页唯一不可逆的动作,而且销毁的是取证材料 —— 必须走强制勾选
          的那一档,并且把会一起消失的记录条数复述出来。 */}
      <QyConfirmDialog
        open={deleting != null}
        onOpenChange={(open) => {
          if (!open) setDeleting(null)
        }}
        title={t('qy_rw_delete_title')}
        description={t('qy_rw_delete_desc', { name: deleting?.name ?? '' })}
        details={
          deleting ? (
            <p className='text-sm'>
              {t('qy_rw_delete_detail', { count: deleting.captured })}
            </p>
          ) : null
        }
        irreversible
        irreversibleDesc={t('qy_rw_delete_irreversible')}
        isLoading={remove.isPending}
        onConfirm={() => {
          if (deleting) remove.mutate(deleting)
        }}
      />
    </Card>
  )
}

/** 作用域三格的展示。空格 = 不限,一个都不显示会让人以为它盯的是全站。 */
function QyRwTargetCell(props: { task: QyRwTask }) {
  const { t } = useTranslation()
  const parts: string[] = []
  if (props.task.target_user_id > 0) {
    parts.push(
      `${props.task.target_username || QY_EMPTY_TEXT} #${props.task.target_user_id}`
    )
  }
  if (props.task.target_group) {
    parts.push(t('qy_rw_target_group', { group: props.task.target_group }))
  }
  if (props.task.target_model) {
    parts.push(t('qy_rw_target_model', { model: props.task.target_model }))
  }
  return (
    <div className='flex flex-col'>
      {parts.map((part) => (
        <span key={part}>{part}</span>
      ))}
    </div>
  )
}

/**
 * 时间窗那一列。
 *
 * 永久监听显示一句话,有终点的显示"还剩多久 / 已结束" —— 一个绝对时间戳
 * (`2026-10-01 18:00`)读者还要自己减一次,而这一列存在的意义就是免掉那一次减法。
 */
function QyRwWindowCell(props: { task: QyRwTask }) {
  const { t } = useTranslation()
  const remaining = qyRwRemainingSeconds(
    props.task,
    Math.floor(Date.now() / 1000)
  )
  if (remaining == null) return <span>{t('qy_rw_window_forever')}</span>
  if (remaining <= 0) {
    return (
      <span className='text-muted-foreground'>
        {t('qy_rw_window_ended', { at: formatQyTs(props.task.ends_at) })}
      </span>
    )
  }
  return <span>{t('qy_rw_window_left', { d: formatQyDuration(remaining) })}</span>
}

/** 保留期那一列。三个语义值各说各的话,见 `qyRwRetentionKind`。 */
function QyRwRetentionCell(props: { task: QyRwTask }) {
  const { t } = useTranslation()
  const kind = qyRwRetentionKind(props.task)
  if (kind === 'forever') return <span>{t('qy_rw_retention_forever')}</span>
  if (kind === 'default') return <span>{t('qy_rw_retention_default')}</span>
  return <span>{t('qy_rw_retention_days', { d: props.task.retention_days })}</span>
}
