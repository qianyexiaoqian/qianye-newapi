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
import { useMutation, useQuery } from '@tanstack/react-query'
import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

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
import { getUserGroupOptions } from '@/features/users/api'

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { qyErrorMessage } from '../../../lib/api'
import { formatQyDuration } from '../../ops/format'
import { qyCreateRiskWatchTask, qyUpdateRiskWatchTask } from '../api'
import {
  QY_RW_COUNTDOWNS,
  qyRwPercentText,
  qyRwPercentToBps,
} from '../lib/format'
import type { QyRwTask, QyRwWindowMode } from '../types'

/** 「跟随全局默认」在 Select 里的哨兵值:空串与 placeholder 语义纠缠。 */
const RETENTION_DEFAULT = '__default__'
/** 「永久保留」。它与上一个是两件事,见 `qyRwRetentionKind`。 */
const RETENTION_FOREVER = '__forever__'
const GROUP_ANY = '__any__'

/**
 * 新建 / 编辑监听任务。
 *
 * ## 校验只做「按钮点了什么都没发生」这一层
 *
 * 概率范围、条数上界、时间窗自洽、保留期上界的判据全部在后端
 * (`api_admin.go` 的 `applyTo` / `resolveWindow` / `checkRetention`),
 * 前端**刻意不复述**:复述一遍就是同一份规则的第二份拷贝,而漂移的方向必然是
 * 前端更松(后端加了新判据前端不会跟)或更严(前端挡住了后端本来接受的写法,
 * 用户完全无从申诉)。这里只挡住"三格作用域全空"与"名字为空"两条,
 * 因为它们的后端文案要靠一次失败的提交才能看到,而前者的代价是一个全站任务。
 *
 * ## 倒计时是「从按下保存那一刻起」
 *
 * 新建时后端按当前时刻起算;**编辑时不会重算** —— 只改一个备注不该把剩余时间
 * 悄悄重置成满格。要重新计时请用列表上的「启动」。
 */
export function QyRwTaskDialog(props: {
  open: boolean
  /** 为 null 表示新建。 */
  task: QyRwTask | null
  maxRetentionDays: number
  defaultRetention: number
  onClose: () => void
  onSaved: () => Promise<void> | void
}) {
  const { t } = useTranslation()
  const formId = useId()

  const [name, setName] = useState('')
  const [note, setNote] = useState('')
  const [userId, setUserId] = useState('')
  const [group, setGroup] = useState(GROUP_ANY)
  const [model, setModel] = useState('')
  const [percent, setPercent] = useState('10')
  const [maxRecords, setMaxRecords] = useState('200')
  const [windowMode, setWindowMode] = useState<QyRwWindowMode>('countdown')
  const [countdown, setCountdown] = useState(String(24 * 3600))
  const [endsAtLocal, setEndsAtLocal] = useState('')
  const [retention, setRetention] = useState(RETENTION_DEFAULT)

  const task = props.task
  // 每次打开都从入参重置:留着上一次的草稿会让「编辑 A → 关掉 → 新建」带出
  // A 的内容,而那看起来完全像是新建表单的默认值。
  useEffect(() => {
    if (!props.open) return
    setName(task?.name ?? '')
    setNote(task?.note ?? '')
    setUserId(task && task.target_user_id > 0 ? String(task.target_user_id) : '')
    setGroup(task?.target_group ? task.target_group : GROUP_ANY)
    setModel(task?.target_model ?? '')
    setPercent(task ? String((task.sample_bps / 100).toFixed(2)) : '10')
    setMaxRecords(task ? String(task.max_records) : '200')
    setWindowMode(task?.window_mode ?? 'countdown')
    setCountdown(String(task?.countdown_seconds || 24 * 3600))
    setEndsAtLocal(task?.ends_at ? toLocalInput(task.ends_at) : '')
    if (task == null || task.retention_days == null) {
      setRetention(RETENTION_DEFAULT)
    } else if (task.retention_days <= 0) {
      setRetention(RETENTION_FOREVER)
    } else {
      setRetention(String(task.retention_days))
    }
  }, [props.open, task])

  const groupsQuery = useQuery({
    queryKey: ['user-group-options'],
    queryFn: () => getUserGroupOptions(),
    staleTime: 5 * 60 * 1000,
    enabled: props.open,
  })
  // 存量任务盯的分组可能已经一个用户都没有了,于是不在候选清单里 —— 必须把
  // 当前值补进去,否则运营只想改个备注,一打开分组就自己变成"不限"了。
  const groupOptions = [
    ...new Set([
      ...(groupsQuery.data?.data ?? []),
      ...(task?.target_group ? [task.target_group] : []),
    ]),
  ]

  const targetUserId = Number.parseInt(userId, 10) || 0
  const targetGroup = group === GROUP_ANY ? '' : group
  const scopeEmpty =
    targetUserId <= 0 && targetGroup === '' && model.trim() === ''

  const save = useMutation({
    mutationFn: () => {
      const payload = {
        name: name.trim(),
        note: note.trim(),
        target_user_id: targetUserId,
        target_group: targetGroup,
        target_model: model.trim(),
        sample_bps: qyRwPercentToBps(percent),
        max_records: Number.parseInt(maxRecords, 10) || 0,
        window_mode: windowMode,
        starts_at: 0,
        ends_at: windowMode === 'range' ? fromLocalInput(endsAtLocal) : 0,
        countdown_seconds:
          windowMode === 'countdown' ? Number.parseInt(countdown, 10) || 0 : 0,
        retention_days: retentionValue(retention),
        version: task?.version ?? 0,
      }
      return task == null
        ? qyCreateRiskWatchTask(payload)
        : qyUpdateRiskWatchTask(task.id, payload)
    },
    onSuccess: async () => {
      toast.success(task == null ? t('qy_rw_toast_created') : t('qy_rw_toast_saved'))
      props.onClose()
      await props.onSaved()
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const canSubmit = name.trim() !== '' && !scopeEmpty && !save.isPending

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={task == null ? t('qy_rw_new_task') : t('qy_rw_edit_task')}
      description={t('qy_rw_form_desc')}
      dismissible={false}
      footer={
        <>
          <Button variant='outline' onClick={props.onClose}>
            {t('qy_common_cancel')}
          </Button>
          <Button form={formId} type='submit' disabled={!canSubmit}>
            {t('qy_common_save')}
          </Button>
        </>
      }
    >
      <form
        id={formId}
        className='space-y-4'
        onSubmit={(event) => {
          event.preventDefault()
          if (canSubmit) save.mutate()
        }}
      >
        <div className='space-y-1.5'>
          <Label htmlFor={`${formId}-name`}>{t('qy_rw_field_name')}</Label>
          <Input
            id={`${formId}-name`}
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={t('qy_rw_field_name_ph')}
          />
        </div>

        <div className='space-y-1.5'>
          <Label htmlFor={`${formId}-note`}>{t('qy_rw_field_note')}</Label>
          <Input
            id={`${formId}-note`}
            value={note}
            onChange={(e) => setNote(e.target.value)}
            placeholder={t('qy_rw_field_note_ph')}
          />
          {/* 立案理由是这张表上最该被写满的一格:一个三个月前建的监听任务,
              若没有理由,后来的人既不敢停也不敢留。 */}
          <p className='text-muted-foreground text-xs'>
            {t('qy_rw_field_note_hint')}
          </p>
        </div>

        <div className='grid gap-3 sm:grid-cols-3'>
          <div className='space-y-1.5'>
            <Label htmlFor={`${formId}-user`}>{t('qy_rw_field_user')}</Label>
            <Input
              id={`${formId}-user`}
              inputMode='numeric'
              value={userId}
              onChange={(e) => setUserId(e.target.value.replaceAll(/\D/gu, ''))}
              placeholder={t('qy_rw_field_any')}
            />
          </div>
          <div className='space-y-1.5'>
            <Label htmlFor={`${formId}-group`}>{t('qy_rw_field_group')}</Label>
            <Select value={group} onValueChange={(v) => setGroup(v ?? GROUP_ANY)}>
              <SelectTrigger id={`${formId}-group`}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={GROUP_ANY}>{t('qy_rw_field_any')}</SelectItem>
                {groupOptions.map((g) => (
                  <SelectItem key={g} value={g}>
                    {g}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className='space-y-1.5'>
            <Label htmlFor={`${formId}-model`}>{t('qy_rw_field_model')}</Label>
            <Input
              id={`${formId}-model`}
              value={model}
              onChange={(e) => setModel(e.target.value)}
              placeholder={t('qy_rw_field_any')}
            />
          </div>
        </div>
        <p className='text-muted-foreground text-xs'>
          {scopeEmpty ? t('qy_rw_scope_required') : t('qy_rw_scope_hint')}
        </p>

        <div className='grid gap-3 sm:grid-cols-2'>
          <div className='space-y-1.5'>
            <Label htmlFor={`${formId}-rate`}>{t('qy_rw_field_rate')}</Label>
            <Input
              id={`${formId}-rate`}
              inputMode='decimal'
              value={percent}
              onChange={(e) => setPercent(e.target.value)}
            />
            {/* 把换算后的值当场回显:这一格是唯一的成本闸门,而"我填的 10 到底
                是 10% 还是 0.1%"必须在按下保存之前就有答案。 */}
            <p className='text-muted-foreground text-xs'>
              {t('qy_rw_field_rate_hint', {
                pct: qyRwPercentText(qyRwPercentToBps(percent)),
              })}
            </p>
          </div>
          <div className='space-y-1.5'>
            <Label htmlFor={`${formId}-max`}>{t('qy_rw_field_max')}</Label>
            <Input
              id={`${formId}-max`}
              inputMode='numeric'
              value={maxRecords}
              onChange={(e) =>
                setMaxRecords(e.target.value.replaceAll(/\D/gu, ''))
              }
            />
            <p className='text-muted-foreground text-xs'>
              {t('qy_rw_field_max_hint')}
            </p>
          </div>
        </div>

        <div className='space-y-1.5'>
          <Label htmlFor={`${formId}-window`}>{t('qy_rw_field_window')}</Label>
          <Select
            value={windowMode}
            onValueChange={(v) => setWindowMode((v ?? 'countdown') as QyRwWindowMode)}
          >
            <SelectTrigger id={`${formId}-window`}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value='countdown'>
                {t('qy_rw_window_countdown')}
              </SelectItem>
              <SelectItem value='range'>{t('qy_rw_window_range')}</SelectItem>
              <SelectItem value='forever'>
                {t('qy_rw_window_forever')}
              </SelectItem>
            </SelectContent>
          </Select>
        </div>

        {windowMode === 'countdown' ? (
          <div className='space-y-1.5'>
            <Label htmlFor={`${formId}-countdown`}>
              {t('qy_rw_field_countdown')}
            </Label>
            <Select value={countdown} onValueChange={(v) => setCountdown(v ?? '')}>
              <SelectTrigger id={`${formId}-countdown`}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {QY_RW_COUNTDOWNS.map((secs) => (
                  <SelectItem key={secs} value={String(secs)}>
                    {formatQyDuration(secs)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {task != null ? (
              <p className='text-muted-foreground text-xs'>
                {t('qy_rw_countdown_edit_hint')}
              </p>
            ) : null}
          </div>
        ) : null}

        {windowMode === 'range' ? (
          <div className='space-y-1.5'>
            <Label htmlFor={`${formId}-ends`}>{t('qy_rw_field_ends_at')}</Label>
            <Input
              id={`${formId}-ends`}
              type='datetime-local'
              value={endsAtLocal}
              onChange={(e) => setEndsAtLocal(e.target.value)}
            />
          </div>
        ) : null}

        <div className='space-y-1.5'>
          <Label htmlFor={`${formId}-retention`}>
            {t('qy_rw_field_retention')}
          </Label>
          <Select
            value={retention}
            onValueChange={(v) => setRetention(v ?? RETENTION_DEFAULT)}
          >
            <SelectTrigger id={`${formId}-retention`}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={RETENTION_DEFAULT}>
                {t('qy_rw_retention_default_n', { d: props.defaultRetention })}
              </SelectItem>
              {retentionChoices(props.maxRetentionDays).map((days) => (
                <SelectItem key={days} value={String(days)}>
                  {t('qy_rw_retention_days', { d: days })}
                </SelectItem>
              ))}
              <SelectItem value={RETENTION_FOREVER}>
                {t('qy_rw_retention_forever')}
              </SelectItem>
            </SelectContent>
          </Select>
          <p className='text-muted-foreground text-xs'>
            {t('qy_rw_field_retention_hint')}
          </p>
        </div>
      </form>
    </QyResponsiveDialog>
  )
}

/** 保留期下拉的候选天数,超过站点上界的一律不列出来。 */
function retentionChoices(maxDays: number): number[] {
  const all = [3, 7, 30, 90, 180, 365]
  if (maxDays <= 0) return all
  return all.filter((days) => days <= maxDays)
}

/** 把下拉的哨兵值翻回接口要的三种取值。 */
function retentionValue(raw: string): number | null {
  if (raw === RETENTION_DEFAULT) return null
  if (raw === RETENTION_FOREVER) return 0
  return Number.parseInt(raw, 10) || null
}

/**
 * unix 秒 → `datetime-local` 输入框要的本地时间字符串。
 *
 * 手动拼而不是 `toISOString().slice(0,16)`:后者给的是 UTC,输入框会把它当成
 * 本地时间显示 —— 东八区的管理员会看到一个早了八小时的结束时刻,然后把它改对,
 * 于是真正存进去的又晚了八小时。
 */
function toLocalInput(unixSeconds: number): string {
  const d = new Date(unixSeconds * 1000)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/** `datetime-local` 字符串 → unix 秒。空串给 0(后端按"没填"拒绝)。 */
function fromLocalInput(local: string): number {
  if (!local) return 0
  const ms = new Date(local).getTime()
  return Number.isFinite(ms) ? Math.floor(ms / 1000) : 0
}
