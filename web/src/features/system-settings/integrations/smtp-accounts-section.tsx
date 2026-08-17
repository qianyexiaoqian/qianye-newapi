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
import { Pencil, Plus, Trash2 } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
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
  createSmtpAccount,
  deleteSmtpAccount,
  emptySmtpAccountPayload,
  getSmtpAccountStats,
  listSmtpAccounts,
  toPayload,
  updateSmtpAccount,
  type SmtpAccountPayload,
  type SmtpSendMode,
} from './smtp-accounts-api'

type Props = {
  defaultValues: {
    SMTPSendMode: string
    SMTPFixedAccountID: string
  }
}

/**
 * SMTP 发件账号。
 *
 * ── 存储形态 ──
 *
 * 一行一个账号存在独立数据库表里（不是 options 里的一块 JSON），因此单条增删改
 * 只碰那一行：两个管理员同屏各改一个账号不会互相覆盖，停用一个号也不必把全部
 * 账号（含密码）重新写回去。发件模式与固定账号仍是站级标量，留在 options。
 *
 * ── 与旧的「SMTP 邮件」单账号表单的关系 ──
 *
 * 旧表单已整体移除。原有配置在升级后第一次启动时被一次性迁进本表（account_id
 * 固定为 `legacy`），此后账号表是唯一事实源，发件路径不再回落老配置。
 */
export function SmtpAccountsSection({ defaultValues }: Props) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const updateOption = useUpdateOption()

  const [mode, setMode] = useState<SmtpSendMode>(
    (defaultValues.SMTPSendMode as SmtpSendMode) || 'sequential'
  )
  const [fixedId, setFixedId] = useState(defaultValues.SMTPFixedAccountID || '')
  const [editing, setEditing] = useState<SmtpAccountPayload | null>(null)
  const [isNew, setIsNew] = useState(false)

  useEffect(() => {
    setMode((defaultValues.SMTPSendMode as SmtpSendMode) || 'sequential')
  }, [defaultValues.SMTPSendMode])
  useEffect(() => {
    setFixedId(defaultValues.SMTPFixedAccountID || '')
  }, [defaultValues.SMTPFixedAccountID])

  const accountsQuery = useQuery({
    queryKey: ['smtp-accounts'],
    queryFn: listSmtpAccounts,
    retry: false,
  })
  const statsQuery = useQuery({
    queryKey: ['smtp-account-stats'],
    queryFn: () => getSmtpAccountStats(),
    retry: false,
  })

  const accounts = useMemo(
    () => accountsQuery.data?.data ?? [],
    [accountsQuery.data]
  )
  const statsById = useMemo(() => {
    const map = new Map<string, number>()
    for (const s of statsQuery.data?.data ?? [])
      map.set(s.account_id, s.last_hour)
    return map
  }, [statsQuery.data])

  const refresh = () => {
    queryClient.invalidateQueries({ queryKey: ['smtp-accounts'] })
    queryClient.invalidateQueries({ queryKey: ['smtp-account-stats'] })
  }

  const saveMutation = useMutation({
    mutationFn: (payload: SmtpAccountPayload) =>
      payload.id > 0 ? updateSmtpAccount(payload) : createSmtpAccount(payload),
    onSuccess: (data) => {
      if (!data.success) {
        toast.error(data.message || t('Failed to update setting'))
        return
      }
      toast.success(t('Setting updated successfully'))
      setEditing(null)
      refresh()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const removeMutation = useMutation({
    mutationFn: (id: number) => deleteSmtpAccount(id),
    onSuccess: (data) => {
      if (!data.success) {
        toast.error(data.message || t('Failed to update setting'))
        return
      }
      toast.success(t('Setting updated successfully'))
      refresh()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const saveMode = async () => {
    try {
      await updateOption.mutateAsync({ key: 'SMTPSendMode', value: mode })
      await updateOption.mutateAsync({
        key: 'SMTPFixedAccountID',
        value: fixedId,
      })
    } catch {
      // useUpdateOption 已经弹过失败详情。
    }
  }

  return (
    <SettingsSection title={t('qy_smtp_accounts_title')}>
      <p className='text-muted-foreground mb-4 text-sm'>
        {t('qy_smtp_accounts_desc')}
      </p>

      <div className='mb-6 flex flex-wrap items-end gap-3'>
        <div className='space-y-1.5'>
          <Label className='text-xs'>{t('qy_smtp_send_mode')}</Label>
          <Select
            value={mode}
            onValueChange={(v) => setMode((v as SmtpSendMode) ?? 'sequential')}
          >
            <SelectTrigger className='w-48'>
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
          <div className='space-y-1.5'>
            <Label className='text-xs'>{t('qy_smtp_fixed_account')}</Label>
            <Select value={fixedId} onValueChange={(v) => setFixedId(v ?? '')}>
              <SelectTrigger className='w-56'>
                <SelectValue placeholder={t('qy_smtp_fixed_placeholder')} />
              </SelectTrigger>
              <SelectContent>
                {accounts.map((a) => (
                  <SelectItem key={a.account_id} value={a.account_id}>
                    {a.name || a.account || a.account_id}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        )}

        <Button
          type='button'
          variant='outline'
          onClick={saveMode}
          disabled={updateOption.isPending}
        >
          {t('qy_smtp_save_mode')}
        </Button>
      </div>

      <div className='overflow-x-auto rounded-lg border'>
        <table className='w-full text-sm'>
          <thead className='bg-muted/50'>
            <tr className='text-left'>
              <th className='p-2 font-medium'>{t('qy_smtp_col_enabled')}</th>
              <th className='p-2 font-medium'>{t('qy_smtp_field_name')}</th>
              <th className='p-2 font-medium'>{t('qy_smtp_field_server')}</th>
              <th className='p-2 font-medium'>{t('qy_smtp_field_account')}</th>
              <th className='p-2 font-medium'>{t('qy_smtp_col_hour_usage')}</th>
              <th className='p-2 font-medium'>{t('qy_smtp_col_actions')}</th>
            </tr>
          </thead>
          <tbody>
            {accounts.length === 0 && (
              <tr>
                <td
                  className='text-muted-foreground p-4 text-center'
                  colSpan={6}
                >
                  {accountsQuery.isPending
                    ? t('Loading...')
                    : t('qy_smtp_no_accounts')}
                </td>
              </tr>
            )}
            {accounts.map((a) => (
              <tr key={a.id} className='border-t'>
                <td className='p-2'>
                  <Switch
                    checked={a.enabled}
                    onCheckedChange={(v) =>
                      saveMutation.mutate({ ...toPayload(a), enabled: v })
                    }
                  />
                </td>
                <td className='p-2'>
                  <span className='font-medium'>{a.name || '—'}</span>
                  {!a.has_token && (
                    <span className='text-destructive ml-2 text-xs'>
                      {t('qy_smtp_no_password')}
                    </span>
                  )}
                </td>
                <td className='p-2 break-all'>
                  {a.server}:{a.port}
                </td>
                <td className='p-2 break-all'>{a.account}</td>
                <td className='p-2 whitespace-nowrap'>
                  {statsById.get(a.account_id) ?? 0}
                  {a.hourly_limit > 0 ? ` / ${a.hourly_limit}` : ''}
                </td>
                <td className='p-2'>
                  <div className='flex gap-1'>
                    <Button
                      type='button'
                      variant='ghost'
                      size='icon'
                      aria-label={t('Edit')}
                      onClick={() => {
                        setIsNew(false)
                        setEditing(toPayload(a))
                      }}
                    >
                      <Pencil className='size-4' />
                    </Button>
                    <Button
                      type='button'
                      variant='ghost'
                      size='icon'
                      aria-label={t('Delete')}
                      onClick={() => removeMutation.mutate(a.id)}
                    >
                      <Trash2 className='size-4' />
                    </Button>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <div className='mt-4'>
        <Button
          type='button'
          variant='outline'
          onClick={() => {
            setIsNew(true)
            setEditing(emptySmtpAccountPayload())
          }}
        >
          <Plus className='mr-1 size-4' />
          {t('qy_smtp_add_account')}
        </Button>
      </div>

      <SmtpAccountDialog
        value={editing}
        isNew={isNew}
        onClose={() => setEditing(null)}
        onSave={(payload) => saveMutation.mutate(payload)}
        saving={saveMutation.isPending}
      />
    </SettingsSection>
  )
}

function SmtpAccountDialog(props: {
  value: SmtpAccountPayload | null
  isNew: boolean
  onClose: () => void
  onSave: (payload: SmtpAccountPayload) => void
  saving: boolean
}) {
  const { t } = useTranslation()
  const [draft, setDraft] = useState<SmtpAccountPayload | null>(props.value)

  useEffect(() => setDraft(props.value), [props.value])

  if (!draft) return null
  const patch = (next: Partial<SmtpAccountPayload>) =>
    setDraft({ ...draft, ...next })

  return (
    <Dialog open onOpenChange={(open) => !open && props.onClose()}>
      <DialogContent className='max-h-[85vh] overflow-y-auto sm:max-w-2xl'>
        <DialogHeader>
          <DialogTitle>
            {props.isNew ? t('qy_smtp_add_account') : t('qy_smtp_edit_account')}
          </DialogTitle>
          <DialogDescription>{t('qy_smtp_dialog_desc')}</DialogDescription>
        </DialogHeader>

        <div className='grid gap-3 sm:grid-cols-2'>
          <Field label={t('qy_smtp_field_name')}>
            <Input
              value={draft.name}
              onChange={(e) => patch({ name: e.target.value })}
            />
          </Field>
          <Field label={t('qy_smtp_field_server')}>
            <Input
              value={draft.server}
              onChange={(e) => patch({ server: e.target.value })}
            />
          </Field>
          <Field label={t('qy_smtp_field_port')}>
            <Input
              type='number'
              value={draft.port}
              onChange={(e) => patch({ port: Number(e.target.value) })}
            />
          </Field>
          <Field label={t('qy_smtp_field_account')}>
            <Input
              value={draft.account}
              onChange={(e) => patch({ account: e.target.value })}
            />
          </Field>
          <Field
            label={t('qy_smtp_field_token')}
            hint={props.isNew ? undefined : t('qy_smtp_token_keep_hint')}
          >
            <Input
              type='password'
              value={draft.token}
              onChange={(e) => patch({ token: e.target.value })}
            />
          </Field>
          <Field label={t('qy_smtp_field_from')}>
            <Input
              value={draft.from_addr}
              onChange={(e) => patch({ from_addr: e.target.value })}
            />
          </Field>
          <Field label={t('qy_smtp_field_hourly_limit')}>
            <Input
              type='number'
              min={0}
              value={draft.hourly_limit}
              onChange={(e) => patch({ hourly_limit: Number(e.target.value) })}
            />
          </Field>
          <Field label={t('qy_smtp_field_sort_order')}>
            <Input
              type='number'
              value={draft.sort_order}
              onChange={(e) => patch({ sort_order: Number(e.target.value) })}
            />
          </Field>
        </div>

        <div className='mt-2 flex flex-wrap gap-x-6 gap-y-2'>
          <Toggle
            label={t('qy_smtp_field_ssl')}
            checked={draft.ssl_enabled}
            onChange={(v) => patch({ ssl_enabled: v })}
          />
          <Toggle
            label={t('qy_smtp_field_starttls')}
            checked={draft.start_tls_enabled}
            onChange={(v) => patch({ start_tls_enabled: v })}
          />
          <Toggle
            label={t('qy_smtp_field_skip_verify')}
            checked={draft.insecure_skip_verify}
            onChange={(v) => patch({ insecure_skip_verify: v })}
          />
          <Toggle
            label={t('qy_smtp_field_force_login')}
            checked={draft.force_auth_login}
            onChange={(v) => patch({ force_auth_login: v })}
          />
        </div>

        <DialogFooter>
          <Button type='button' variant='outline' onClick={props.onClose}>
            {t('Cancel')}
          </Button>
          <Button
            type='button'
            onClick={() => props.onSave(draft)}
            disabled={props.saving}
          >
            {t('Save')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function Field(props: {
  label: string
  hint?: string
  children: React.ReactNode
}) {
  return (
    <div className='space-y-1.5'>
      <Label className='text-xs'>{props.label}</Label>
      {props.children}
      {props.hint && (
        <p className='text-muted-foreground text-xs'>{props.hint}</p>
      )}
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
