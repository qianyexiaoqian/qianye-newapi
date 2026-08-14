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
import { Plus, Trash2 } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
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
import { Switch } from '@/components/ui/switch'

import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'
import {
  emptySmtpAccount,
  getSmtpAccountStats,
  parseSmtpAccounts,
  type SmtpAccount,
  type SmtpSendMode,
} from './smtp-accounts-api'

type Props = {
  defaultValues: {
    SMTPAccounts: string
    SMTPSendMode: string
    SMTPFixedAccountID: string
  }
}

/**
 * 生成一个新账号的 id。
 *
 * id 是发件台账与用量统计的归集键，一旦用过就不能再改，因此这里生成的是一个
 * 与账号名无关的随机串 —— 拿邮箱地址或名字当 id 的话，运营改个备注名就会让
 * 历史统计断成两截。
 */
function newAccountId(): string {
  return `smtp_${Math.random().toString(36).slice(2, 10)}`
}

/**
 * 多 SMTP 发件账号。
 *
 * ── 与上面那个「SMTP 邮件」单账号表单的关系 ──
 *
 * **这张表非空时，单账号那份配置整体不再参与发件。** 后端
 * `common.ResolveSMTPAccount` 只在账号表为空时才回落到那组老配置（"legacy" 账号）。
 * 两者刻意不合并：老配置是升级安全网，必须能在多账号出问题时原样退回去。
 *
 * ── 为什么要多个账号 ──
 *
 * 单个发件邮箱一小时内发太多，收件方会开始把整批邮件判成垃圾，而 SMTP 依旧
 * 返回成功 —— 这件事对发件方完全不可见。所以每个账号可以配一个小时上限，
 * 触顶的账号在择号时被跳过。
 */
export function SmtpAccountsSection({ defaultValues }: Props) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()

  const [accounts, setAccounts] = useState<SmtpAccount[]>(() =>
    parseSmtpAccounts(defaultValues.SMTPAccounts)
  )
  const [mode, setMode] = useState<SmtpSendMode>(
    (defaultValues.SMTPSendMode as SmtpSendMode) || 'sequential'
  )
  const [fixedId, setFixedId] = useState(defaultValues.SMTPFixedAccountID || '')

  /*
    服务端回读到达时用它替换本地一切。本地状态是「我请求过什么」，回读是
    「服务端现在是什么」；把前者当后者渲染，一次部分失败就会画出一个从未
    存在过的成功画面。依赖列原始字符串而不是 defaultValues 对象 —— 上层每次
    渲染都新造一个对象，按对象比会在父级重渲染时把正在编辑的内容清掉。
  */
  useEffect(() => {
    setAccounts(parseSmtpAccounts(defaultValues.SMTPAccounts))
  }, [defaultValues.SMTPAccounts])
  useEffect(() => {
    setMode((defaultValues.SMTPSendMode as SmtpSendMode) || 'sequential')
  }, [defaultValues.SMTPSendMode])
  useEffect(() => {
    setFixedId(defaultValues.SMTPFixedAccountID || '')
  }, [defaultValues.SMTPFixedAccountID])

  const statsQuery = useQuery({
    queryKey: ['smtp-account-stats'],
    queryFn: () => getSmtpAccountStats(),
    retry: false,
  })
  const statsById = useMemo(() => {
    const map = new Map<string, { lastHour: number; total: number }>()
    for (const s of statsQuery.data?.data ?? []) {
      map.set(s.account_id, { lastHour: s.last_hour, total: s.total })
    }
    return map
  }, [statsQuery.data])

  const patch = (index: number, next: Partial<SmtpAccount>) => {
    setAccounts((prev) =>
      prev.map((a, i) => (i === index ? { ...a, ...next } : a))
    )
  }

  const onSave = async () => {
    // 保存前把端口与上限归一成数字：受控 Input 给的是字符串，直接 JSON 化会让
    // 后端拿到 `"port":"587"` 而结构体字段是 int —— 整张表解析失败，
    // 表现是「保存报了个看不懂的错，而界面上每个字段都填得好好的」。
    const payload = accounts.map((a) => ({
      ...a,
      port: Number(a.port) || 0,
      hourly_limit: Number(a.hourly_limit) || 0,
    }))
    try {
      await updateOption.mutateAsync({
        key: 'SMTPAccounts',
        value: JSON.stringify(payload),
      })
      await updateOption.mutateAsync({ key: 'SMTPSendMode', value: mode })
      await updateOption.mutateAsync({
        key: 'SMTPFixedAccountID',
        value: fixedId,
      })
    } catch {
      // useUpdateOption 已经弹过失败详情，这里不重复报错。
    }
  }

  const addAccount = () => {
    setAccounts((prev) => [...prev, emptySmtpAccount(newAccountId())])
  }

  const removeAccount = (index: number) => {
    const removed = accounts[index]
    setAccounts((prev) => prev.filter((_, i) => i !== index))
    // 删掉的正好是固定模式指定的那个 → 一并清掉，否则保存后后端会打一条
    // 「固定账号不存在，本次回落到 X」的告警，而设置页显示的仍是那个已删的号。
    if (removed && removed.id === fixedId) setFixedId('')
    toast.message(t('qy_smtp_removed_pending_save'))
  }

  return (
    <SettingsSection title={t('qy_smtp_accounts_title')}>
      <p className='text-muted-foreground mb-4 text-sm'>
        {t('qy_smtp_accounts_desc')}
      </p>

      <div className='mb-6 grid gap-4 sm:grid-cols-2'>
        <div className='space-y-2'>
          <Label>{t('qy_smtp_send_mode')}</Label>
          <Select
            value={mode}
            onValueChange={(v) => setMode((v as SmtpSendMode) ?? 'sequential')}
          >
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value='sequential'>
                {t('qy_smtp_mode_sequential')}
              </SelectItem>
              <SelectItem value='random'>{t('qy_smtp_mode_random')}</SelectItem>
              <SelectItem value='fixed'>{t('qy_smtp_mode_fixed')}</SelectItem>
            </SelectContent>
          </Select>
        </div>

        {mode === 'fixed' && (
          <div className='space-y-2'>
            <Label>{t('qy_smtp_fixed_account')}</Label>
            <Select value={fixedId} onValueChange={(v) => setFixedId(v ?? '')}>
              <SelectTrigger>
                <SelectValue placeholder={t('qy_smtp_fixed_placeholder')} />
              </SelectTrigger>
              <SelectContent>
                {accounts.map((a) => (
                  <SelectItem key={a.id} value={a.id}>
                    {a.name || a.account || a.id}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        )}
      </div>

      <div className='space-y-4'>
        {accounts.length === 0 && (
          <p className='text-muted-foreground text-sm'>
            {t('qy_smtp_no_accounts')}
          </p>
        )}

        {accounts.map((account, index) => {
          const stat = statsById.get(account.id)
          return (
            <div key={account.id} className='rounded-lg border p-4'>
              <div className='mb-3 flex items-center justify-between gap-3'>
                <div className='flex items-center gap-3'>
                  <Switch
                    checked={account.enabled}
                    onCheckedChange={(v) => patch(index, { enabled: v })}
                  />
                  <span className='text-sm font-medium'>
                    {account.name || account.account || account.id}
                  </span>
                  {stat && (
                    <span className='text-muted-foreground text-xs'>
                      {t('qy_smtp_stat_inline', {
                        hour: stat.lastHour,
                        total: stat.total,
                      })}
                    </span>
                  )}
                </div>
                <Button
                  type='button'
                  variant='ghost'
                  size='icon'
                  onClick={() => removeAccount(index)}
                  aria-label={t('Delete')}
                >
                  <Trash2 className='size-4' />
                </Button>
              </div>

              <div className='grid gap-3 sm:grid-cols-2 lg:grid-cols-3'>
                <Field label={t('qy_smtp_field_name')}>
                  <Input
                    value={account.name}
                    onChange={(e) => patch(index, { name: e.target.value })}
                  />
                </Field>
                <Field label={t('qy_smtp_field_server')}>
                  <Input
                    value={account.server}
                    onChange={(e) => patch(index, { server: e.target.value })}
                  />
                </Field>
                <Field label={t('qy_smtp_field_port')}>
                  <Input
                    type='number'
                    value={account.port}
                    onChange={(e) =>
                      patch(index, { port: Number(e.target.value) })
                    }
                  />
                </Field>
                <Field label={t('qy_smtp_field_account')}>
                  <Input
                    value={account.account}
                    onChange={(e) => patch(index, { account: e.target.value })}
                  />
                </Field>
                <Field label={t('qy_smtp_field_token')}>
                  <Input
                    type='password'
                    value={account.token}
                    onChange={(e) => patch(index, { token: e.target.value })}
                  />
                </Field>
                <Field label={t('qy_smtp_field_from')}>
                  <Input
                    value={account.from}
                    onChange={(e) => patch(index, { from: e.target.value })}
                  />
                </Field>
                <Field label={t('qy_smtp_field_hourly_limit')}>
                  <Input
                    type='number'
                    min={0}
                    value={account.hourly_limit}
                    onChange={(e) =>
                      patch(index, { hourly_limit: Number(e.target.value) })
                    }
                  />
                </Field>
              </div>

              <div className='mt-3 flex flex-wrap gap-x-6 gap-y-2'>
                <Toggle
                  label={t('qy_smtp_field_ssl')}
                  checked={account.ssl_enabled}
                  onChange={(v) => patch(index, { ssl_enabled: v })}
                />
                <Toggle
                  label={t('qy_smtp_field_starttls')}
                  checked={account.start_tls_enabled}
                  onChange={(v) => patch(index, { start_tls_enabled: v })}
                />
                <Toggle
                  label={t('qy_smtp_field_skip_verify')}
                  checked={account.insecure_skip_verify}
                  onChange={(v) => patch(index, { insecure_skip_verify: v })}
                />
                <Toggle
                  label={t('qy_smtp_field_force_login')}
                  checked={account.force_auth_login}
                  onChange={(v) => patch(index, { force_auth_login: v })}
                />
              </div>
            </div>
          )
        })}
      </div>

      <div className='mt-4 flex gap-2'>
        <Button type='button' variant='outline' onClick={addAccount}>
          <Plus className='mr-1 size-4' />
          {t('qy_smtp_add_account')}
        </Button>
        <Button type='button' onClick={onSave} disabled={updateOption.isPending}>
          {t('Save')}
        </Button>
      </div>
    </SettingsSection>
  )
}

function Field(props: { label: string; children: React.ReactNode }) {
  return (
    <div className='space-y-1.5'>
      <Label className='text-xs'>{props.label}</Label>
      {props.children}
    </div>
  )
}

function Toggle(props: {
  label: string
  checked: boolean
  onChange: (v: boolean) => void
}) {
  return (
    <label className='flex items-center gap-2 text-sm'>
      <Switch checked={props.checked} onCheckedChange={props.onChange} />
      {props.label}
    </label>
  )
}
