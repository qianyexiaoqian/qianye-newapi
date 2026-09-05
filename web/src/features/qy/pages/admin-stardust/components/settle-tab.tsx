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
import { RefreshCw, TriangleAlert } from 'lucide-react'
import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
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

import { QyConfirmDialog } from '../../../components/qy-confirm-dialog'
import { QyPageBoundary } from '../../../components/qy-page-boundary'
import { useStardustName } from '../../../hooks/use-stardust-name'
import { formatSdWithUnit } from '../../../lib/format-sd'
import { qyKeys } from '../../../lib/query-keys'
import { qySdAdminErrorMessage } from '../../admin-stardust-config/lib/errors'
import { formatQyTs } from '../../ops/format'
import { QyKeyValue } from '../../ops/qy-ops-ui'
import { qySdFormatDay } from '../../stardust/lib/display'
import {
  qyAdminStardustLedgerCheckQuery,
  qyAdminStardustSettleStatusQuery,
  rerunQyStardustSettle,
} from '../api'
import type { QyStardustSettleStatus } from '../types'

/**
 * 「结算」—— 调度状态 / 重跑某一天 / 账本体检。
 *
 * 三块各答一个问题：昨天那一跑跑完了没有（状态卡）、卡住的那一天怎么补
 * （重跑表单）、账本还自洽吗（体检卡）。它们是运营在"有人说星屑没到账"时
 * 的排查顺序，所以排在同一屏上。
 */
export function QySdAdminSettleTab() {
  return (
    <div className='grid gap-4 lg:grid-cols-2 lg:items-start'>
      <div className='space-y-4'>
        <SettleStatusCard />
        <RerunCard />
      </div>
      <LedgerCheckCard />
    </div>
  )
}

/**
 * 调度状态。`next_settle_at` 只由后端下发（D-03）：今天这一跑已 done 就指向明天，
 * 否则指向今天的门槛 —— 门槛已过而仍未 done 时它在过去，界面据此说"进行中 / 待重试"。
 */
function SettleStatusCard() {
  const { t } = useTranslation()
  const unit = useStardustName()
  const query = useQuery(qyAdminStardustSettleStatusQuery())
  const status = query.data

  return (
    <Card data-card-hover='false'>
      <CardHeader>
        <CardTitle>{t('qy_sdadm_settle_title')}</CardTitle>
        <CardDescription>{t('qy_sdadm_settle_desc')}</CardDescription>
      </CardHeader>
      <CardContent>
        <QyPageBoundary query={query}>
          {status != null && (
            <div className='space-y-3'>
              {status.error != null && status.error !== '' && (
                <Alert variant='destructive'>
                  <TriangleAlert />
                  <AlertTitle>{t('qy_sdadm_settle_error_title')}</AlertTitle>
                  <AlertDescription>{status.error}</AlertDescription>
                </Alert>
              )}
              <div>
                <QyKeyValue label={t('qy_sdadm_settle_target_day')}>
                  {qySdFormatDay(status.target_day)}
                </QyKeyValue>
                <QyKeyValue label={t('qy_sdadm_settle_next_at')}>
                  <NextSettle status={status} />
                </QyKeyValue>
                <QyKeyValue label={t('qy_sdadm_settle_max_attempts')}>
                  {status.max_attempts}
                </QyKeyValue>
              </div>
              <LastRun status={status} unit={unit} />
            </div>
          )}
        </QyPageBoundary>
      </CardContent>
    </Card>
  )
}

/** 下次结算时刻 + 它此刻处在哪一档（未到 / 已就绪等心跳 / 已过门槛但没跑完）。 */
function NextSettle(props: { status: QyStardustSettleStatus }) {
  const { t } = useTranslation()
  const status = props.status
  const doneToday =
    status.last_run != null &&
    status.last_run.run_date === status.run_date &&
    status.last_run.status === 'done'
  let badge: { key: string; variant: 'secondary' | 'destructive' | 'outline' }
  if (doneToday) {
    badge = { key: 'qy_sdadm_settle_state_done', variant: 'secondary' }
  } else if (status.ready) {
    // 门槛已过、今天还没跑完：要么在跑、要么上一次 partial 在等重试。
    badge = { key: 'qy_sdadm_settle_state_due', variant: 'destructive' }
  } else {
    badge = { key: 'qy_sdadm_settle_state_waiting', variant: 'outline' }
  }
  return (
    <span className='inline-flex flex-wrap items-center justify-end gap-1.5'>
      <span>{formatQyTs(status.next_settle_at)}</span>
      <Badge variant={badge.variant}>{t(badge.key)}</Badge>
    </span>
  )
}

function LastRun(props: { status: QyStardustSettleStatus; unit: string }) {
  const { t } = useTranslation()
  const run = props.status.last_run
  if (run == null) {
    return (
      <p className='text-muted-foreground text-xs'>
        {t('qy_sdadm_settle_no_run')}
      </p>
    )
  }
  return (
    <div>
      <p className='mb-1 text-sm font-medium'>
        {t('qy_sdadm_settle_last_run')}
      </p>
      <QyKeyValue label={t('qy_sdadm_settle_run_date')}>
        {qySdFormatDay(run.run_date)}
        {run.target_date !== '' && ` → ${qySdFormatDay(run.target_date)}`}
      </QyKeyValue>
      <QyKeyValue label={t('qy_common_status')}>
        <span className='inline-flex items-center gap-1.5'>
          <Badge variant={run.status === 'done' ? 'secondary' : 'destructive'}>
            {run.status}
          </Badge>
          <span className='text-muted-foreground text-xs'>
            {t('qy_sdadm_settle_attempts', { n: run.attempts })}
          </span>
        </span>
      </QyKeyValue>
      <QyKeyValue label={t('qy_sdadm_settle_counts')}>
        {t('qy_sdadm_settle_counts_value', {
          processed: run.processed,
          held: run.held,
          failed: run.failed,
        })}
      </QyKeyValue>
      <QyKeyValue label={t('qy_sdadm_settle_granted')}>
        {formatSdWithUnit(run.granted, props.unit)}
      </QyKeyValue>
      <QyKeyValue label={t('qy_sdadm_settle_started_at')}>
        {formatQyTs(run.started_at)}
      </QyKeyValue>
      <QyKeyValue label={t('qy_sdadm_settle_finished_at')}>
        {formatQyTs(run.finished_at)}
      </QyKeyValue>
      {run.remark !== '' && (
        <QyKeyValue label={t('qy_common_remark')}>{run.remark}</QyKeyValue>
      )}
    </div>
  )
}

/**
 * 重跑某一天。只对 computed / held 桶生效、已 settled 的一律不动，所以它是
 * "补发"而不是"再发一遍"；但它真的会发星屑，所以过二次确认并强制勾选。
 * 桶日默认填状态卡给的目标日 —— 那正是最常需要重跑的一天。
 */
function RerunCard() {
  const { t } = useTranslation()
  const unit = useStardustName()
  const queryClient = useQueryClient()
  const dayId = useId()
  const status = useQuery(qyAdminStardustSettleStatusQuery()).data
  const [day, setDay] = useState('')
  const [confirmOpen, setConfirmOpen] = useState(false)

  // 目标日到达时预填一次；运营已经改过的不覆盖。
  useEffect(() => {
    if (status != null && day === '') setDay(status.target_day)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [status?.target_day])

  const rerun = useMutation({
    mutationFn: rerunQyStardustSettle,
    onSuccess: async (result) => {
      setConfirmOpen(false)
      toast.success(
        t('qy_sdadm_rerun_done', {
          day: qySdFormatDay(result.day),
          settled: result.settled,
          held: result.held,
          skipped: result.skipped,
          failed: result.failed,
          granted: formatSdWithUnit(result.granted, unit),
        })
      )
      // 重跑真的发了星屑：余额、流水、日桶、体检、调度状态全部要重取。
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    onError: (error) => toast.error(qySdAdminErrorMessage(error, t)),
  })

  const dayValid = /^\d{8}$/.test(day)

  return (
    <Card data-card-hover='false'>
      <CardHeader>
        <CardTitle>{t('qy_sdadm_rerun_title')}</CardTitle>
        <CardDescription>{t('qy_sdadm_rerun_desc')}</CardDescription>
      </CardHeader>
      <CardContent className='space-y-3'>
        <div className='space-y-1.5'>
          <Label htmlFor={dayId}>{t('qy_sdadm_rerun_day')}</Label>
          <Input
            id={dayId}
            inputMode='numeric'
            maxLength={8}
            value={day}
            aria-invalid={day !== '' && !dayValid}
            placeholder={t('qy_sdadm_day_ph')}
            onChange={(event) =>
              setDay(event.target.value.replaceAll(/\D/g, ''))
            }
          />
          <p className='text-muted-foreground text-xs'>
            {t('qy_sdadm_rerun_day_hint')}
          </p>
        </div>
        <Button
          variant='outline'
          disabled={!dayValid || rerun.isPending}
          onClick={() => setConfirmOpen(true)}
        >
          {t('qy_sdadm_rerun_action')}
        </Button>
      </CardContent>

      <QyConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={t('qy_sdadm_rerun_confirm_title')}
        description={t('qy_sdadm_rerun_confirm_desc', {
          day: qySdFormatDay(day),
          unit,
        })}
        irreversible
        irreversibleDesc={t('qy_sdadm_rerun_irreversible', { unit })}
        isLoading={rerun.isPending}
        onConfirm={() => rerun.mutate(day)}
      />
    </Card>
  )
}

/**
 * 账本体检。三条恒等式（I0 / I1 / I2）与暂缓桶积龄。
 *
 * `ok=false` 时展示 `error` 而不是那几个零值：读失败时的 0 个漂移与"真的没有
 * 漂移"看起来一模一样，而那正是最需要看清的时刻。`held_alert` 是红点：
 * 最老的 held 桶已经积压超过 `held_alert_days`，再不处理这笔返还就会一直欠着。
 */
function LedgerCheckCard() {
  const { t } = useTranslation()
  const query = useQuery(qyAdminStardustLedgerCheckQuery())
  const report = query.data

  return (
    <Card data-card-hover='false'>
      <CardHeader>
        <CardTitle className='inline-flex items-center gap-2'>
          {t('qy_sdadm_check_title')}
          {report?.held_alert === true && (
            <span
              className='bg-destructive inline-block size-2 rounded-full'
              aria-label={t('qy_sdadm_check_held_alert')}
              title={t('qy_sdadm_check_held_alert')}
            />
          )}
        </CardTitle>
        <CardDescription>{t('qy_sdadm_check_desc')}</CardDescription>
      </CardHeader>
      <CardContent className='space-y-3'>
        <QyPageBoundary query={query}>
          {report != null && !report.ok && (
            <Alert variant='destructive'>
              <TriangleAlert />
              <AlertTitle>{t('qy_sdadm_check_failed_title')}</AlertTitle>
              <AlertDescription>
                {report.error === ''
                  ? t('qy_sdadm_check_failed_desc')
                  : report.error}
              </AlertDescription>
            </Alert>
          )}
          {report != null && report.ok && (
            <div>
              <QyKeyValue label={t('qy_sdadm_check_checked_users')}>
                {report.checked_users}
              </QyKeyValue>
              <QyKeyValue label={t('qy_sdadm_check_drifted_users')}>
                {report.drifted_users === 0 ? (
                  <Badge variant='secondary'>{t('qy_sdadm_check_ok')}</Badge>
                ) : (
                  <Badge variant='destructive'>
                    {t('qy_sdadm_check_drifted', { n: report.drifted_users })}
                  </Badge>
                )}
              </QyKeyValue>
              {report.drifted_users > 0 && (
                <QyKeyValue label={t('qy_sdadm_check_worst')}>
                  {t('qy_sdadm_check_worst_value', {
                    id: report.worst_user_id,
                    drift: report.worst_drift,
                  })}
                </QyKeyValue>
              )}
              <QyKeyValue label={t('qy_sdadm_check_held_rows')}>
                <span className='inline-flex items-center gap-1.5'>
                  <span>{report.held_rows}</span>
                  {report.held_alert && (
                    <Badge variant='destructive'>
                      {t('qy_sdadm_check_held_alert')}
                    </Badge>
                  )}
                </span>
              </QyKeyValue>
              <QyKeyValue label={t('qy_sdadm_check_oldest_held')}>
                {report.oldest_held_day === ''
                  ? '-'
                  : qySdFormatDay(report.oldest_held_day)}
              </QyKeyValue>
            </div>
          )}
        </QyPageBoundary>
        <Button
          size='sm'
          variant='outline'
          disabled={query.isFetching}
          onClick={() => {
            void query.refetch()
          }}
        >
          <RefreshCw aria-hidden='true' />
          {t('qy_common_refresh')}
        </Button>
      </CardContent>
    </Card>
  )
}
