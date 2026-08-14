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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

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

import { SettingsSection } from '../components/settings-section'
import {
  getEmailSendLogs,
  getSmtpAccountStats,
  type EmailSendLogItem,
} from './smtp-accounts-api'

const PAGE_SIZE = 20
const ALL = '__all__'

function formatTime(ts: number): string {
  if (!ts) return '-'
  return new Date(ts * 1000).toLocaleString()
}

/**
 * SMTP 发件日志。
 *
 * 它回答两个问题，而两个都是旧实现里完全看不见的：
 *
 *   「这个人为什么没收到验证码」 → 按收件人过滤，失败行带原始 SMTP 错误
 *   「哪个账号快到上限了」       → 顶部按账号的统计，含最近一小时与配置上限
 *
 * 旧实现只在 `client.Quit()` 失败那一种情况下往系统日志里写过一行，
 * 认证失败、连接被拒、收件人被拒全都静默消失。
 */
export function SmtpSendLogSection() {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  const [accountId, setAccountId] = useState(ALL)
  const [status, setStatus] = useState<'' | 'success' | 'failed'>('')
  const [receiver, setReceiver] = useState('')
  // 已提交的搜索词。与输入框状态分开，否则每敲一个字符都会发一次请求。
  const [receiverQuery, setReceiverQuery] = useState('')

  const statsQuery = useQuery({
    queryKey: ['smtp-account-stats'],
    queryFn: () => getSmtpAccountStats(),
    retry: false,
  })

  const logsQuery = useQuery({
    queryKey: ['email-send-logs', page, accountId, status, receiverQuery],
    queryFn: () =>
      getEmailSendLogs({
        p: page,
        page_size: PAGE_SIZE,
        account_id: accountId === ALL ? undefined : accountId,
        status: status || undefined,
        receiver: receiverQuery || undefined,
      }),
    retry: false,
  })

  const stats = statsQuery.data?.data ?? []
  const items: EmailSendLogItem[] = logsQuery.data?.data?.items ?? []
  const total = logsQuery.data?.data?.total ?? 0
  const maxPage = Math.max(1, Math.ceil(total / PAGE_SIZE))

  const search = () => {
    setReceiverQuery(receiver.trim())
    setPage(1)
  }

  return (
    <SettingsSection title={t('qy_smtp_log_title')}>
      {stats.length > 0 && (
        <div className='mb-6 grid gap-3 sm:grid-cols-2 lg:grid-cols-3'>
          {stats.map((s) => (
            <div key={s.account_id} className='rounded-lg border p-3'>
              <div className='flex items-center justify-between gap-2'>
                <span className='truncate text-sm font-medium'>
                  {s.account_name || s.account_id}
                </span>
                {!s.configured && (
                  <span className='text-muted-foreground shrink-0 text-xs'>
                    {t('qy_smtp_stat_deleted')}
                  </span>
                )}
              </div>
              <div className='text-muted-foreground mt-1 space-y-0.5 text-xs'>
                <div>
                  {t('qy_smtp_stat_last_hour')}:{' '}
                  <span className='text-foreground font-medium'>
                    {s.last_hour}
                    {s.hourly_limit > 0 ? ` / ${s.hourly_limit}` : ''}
                  </span>
                </div>
                <div>
                  {t('qy_smtp_stat_total')}: {s.total} ({t('qy_smtp_stat_ok')}{' '}
                  {s.success} / {t('qy_smtp_stat_fail')} {s.failed})
                </div>
                <div>
                  {t('qy_smtp_stat_last_sent')}: {formatTime(s.last_sent_at)}
                </div>
              </div>
            </div>
          ))}
        </div>
      )}

      <div className='mb-4 flex flex-wrap items-end gap-3'>
        <div className='space-y-1.5'>
          <Label className='text-xs'>{t('qy_smtp_log_filter_account')}</Label>
          <Select
            value={accountId}
            onValueChange={(v) => {
              setAccountId(v ?? ALL)
              setPage(1)
            }}
          >
            <SelectTrigger className='w-52'>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>{t('qy_smtp_log_all')}</SelectItem>
              {stats.map((s) => (
                <SelectItem key={s.account_id} value={s.account_id}>
                  {s.account_name || s.account_id}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        <div className='space-y-1.5'>
          <Label className='text-xs'>{t('qy_smtp_log_filter_status')}</Label>
          <Select
            value={status || ALL}
            onValueChange={(v) => {
              setStatus(v === ALL || !v ? '' : (v as 'success' | 'failed'))
              setPage(1)
            }}
          >
            <SelectTrigger className='w-40'>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>{t('qy_smtp_log_all')}</SelectItem>
              <SelectItem value='success'>{t('qy_smtp_stat_ok')}</SelectItem>
              <SelectItem value='failed'>{t('qy_smtp_stat_fail')}</SelectItem>
            </SelectContent>
          </Select>
        </div>

        <div className='space-y-1.5'>
          <Label className='text-xs'>{t('qy_smtp_log_filter_receiver')}</Label>
          <Input
            className='w-56'
            value={receiver}
            onChange={(e) => setReceiver(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') search()
            }}
          />
        </div>
        <Button type='button' variant='outline' onClick={search}>
          {t('Search')}
        </Button>
      </div>

      <div className='overflow-x-auto rounded-lg border'>
        <table className='w-full text-sm'>
          <thead className='bg-muted/50'>
            <tr className='text-left'>
              <th className='p-2 font-medium'>{t('qy_smtp_log_col_time')}</th>
              <th className='p-2 font-medium'>
                {t('qy_smtp_log_col_account')}
              </th>
              <th className='p-2 font-medium'>
                {t('qy_smtp_log_col_receiver')}
              </th>
              <th className='p-2 font-medium'>
                {t('qy_smtp_log_col_subject')}
              </th>
              <th className='p-2 font-medium'>{t('qy_smtp_log_col_status')}</th>
              <th className='p-2 font-medium'>
                {t('qy_smtp_log_col_duration')}
              </th>
            </tr>
          </thead>
          <tbody>
            {items.length === 0 && (
              <tr>
                <td
                  className='text-muted-foreground p-4 text-center'
                  colSpan={6}
                >
                  {logsQuery.isPending
                    ? t('Loading...')
                    : t('qy_smtp_log_empty')}
                </td>
              </tr>
            )}
            {items.map((row) => (
              <tr key={row.id} className='border-t align-top'>
                <td className='p-2 whitespace-nowrap'>
                  {formatTime(row.created_at)}
                </td>
                <td className='p-2'>{row.account_name || row.account_id}</td>
                <td className='p-2 break-all'>{row.receiver}</td>
                <td className='p-2'>{row.subject}</td>
                <td className='p-2'>
                  {row.success ? (
                    <span className='text-emerald-600 dark:text-emerald-400'>
                      {t('qy_smtp_stat_ok')}
                    </span>
                  ) : (
                    <span
                      className='text-destructive'
                      /* 失败原因是这张表存在的主要理由，完整挂在 title 上：
                         SMTP 的错误经常带整段服务器响应，直接铺开会把表撑散。 */
                      title={row.error_msg}
                    >
                      {t('qy_smtp_stat_fail')}
                      {row.error_msg ? ` — ${row.error_msg.slice(0, 60)}` : ''}
                    </span>
                  )}
                </td>
                <td className='p-2 whitespace-nowrap'>{row.duration_ms} ms</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <div className='mt-3 flex items-center justify-between'>
        <span className='text-muted-foreground text-xs'>
          {t('qy_smtp_log_total', { count: total })}
        </span>
        <div className='flex gap-2'>
          <Button
            type='button'
            variant='outline'
            size='sm'
            disabled={page <= 1}
            onClick={() => setPage((p) => Math.max(1, p - 1))}
          >
            {t('Previous')}
          </Button>
          <span className='text-muted-foreground self-center text-xs'>
            {page} / {maxPage}
          </span>
          <Button
            type='button'
            variant='outline'
            size='sm'
            disabled={page >= maxPage}
            onClick={() => setPage((p) => p + 1)}
          >
            {t('Next')}
          </Button>
        </div>
      </div>
    </SettingsSection>
  )
}
