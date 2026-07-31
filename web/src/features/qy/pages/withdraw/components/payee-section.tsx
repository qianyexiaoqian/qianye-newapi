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
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Save, Trash2 } from 'lucide-react'
import { useId } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { cn } from '@/lib/utils'

import { qyErrorMessage } from '../../../lib/api'
import { qyKeys } from '../../../lib/query-keys'
import { qyCreatePayee, qyDeletePayee } from '../api'
import { QY_PAYEE_NEW, qyPayeeChannelKey, qyPayeeSpec } from '../lib/payee-spec'
import { normalizeQyPayee, validateQyPayee } from '../lib/validate'
import type { QyPayeeAccount, QyPayeeChannel } from '../types'

type PayeeSectionProps = {
  channels: QyPayeeChannel[]
  accounts: QyPayeeAccount[]
  accountMax: number
  /** 选中的收款方式 ref，或 {@link QY_PAYEE_NEW}。 */
  selectedRef: string
  onSelectedRefChange: (ref: string) => void
  channel: QyPayeeChannel
  onChannelChange: (channel: QyPayeeChannel) => void
  values: Record<string, string>
  onValuesChange: (values: Record<string, string>) => void
  /** 字段 key → i18n key。由父组件在提交前调用 `validateQyPayee` 填入。 */
  errors: Record<string, string>
  disabled?: boolean
}

/**
 * 法币提现的收款信息区。
 *
 * 两条路径互斥：复用已保存的收款方式（只传 `payee_ref`），或本次现填
 * （传 `payee_channel` + `payee`）。后端两者二选一，前端也必须是单选 ——
 * 同时传的话后端会优先用 ref，用户填的那份被静默丢弃。
 *
 * 已保存项**只展示脱敏值**。明文从不下发到任何用户端接口，想改就删了重加。
 */
export function PayeeSection(props: PayeeSectionProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const channelId = useId()
  const spec = qyPayeeSpec(props.channel)
  const isNew = props.selectedRef === QY_PAYEE_NEW

  const savePayee = useMutation({
    mutationFn: qyCreatePayee,
    onSuccess: async (account) => {
      toast.success(t('qy_wd_payee_saved'))
      await queryClient.invalidateQueries({ queryKey: qyKeys.withdrawPayees() })
      // 存完直接选中它：用户的下一步一定是拿它提现。
      props.onSelectedRefChange(account.ref)
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const deletePayee = useMutation({
    mutationFn: qyDeletePayee,
    onSuccess: async (result) => {
      toast.success(t('qy_wd_payee_deleted'))
      await queryClient.invalidateQueries({ queryKey: qyKeys.withdrawPayees() })
      if (props.selectedRef === result.ref) {
        props.onSelectedRefChange(QY_PAYEE_NEW)
      }
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const atAccountLimit =
    props.accountMax > 0 && props.accounts.length >= props.accountMax

  return (
    <div className='space-y-3'>
      <Label>{t('qy_wd_payee_title')}</Label>

      <RadioGroup
        value={props.selectedRef}
        onValueChange={(value) => props.onSelectedRefChange(String(value))}
        disabled={props.disabled}
        className='gap-2'
      >
        {props.accounts.map((account) => (
          <div
            key={account.ref}
            className={cn(
              'flex items-center gap-2 rounded-md border p-2.5',
              props.selectedRef === account.ref && 'border-primary'
            )}
          >
            <RadioGroupItem value={account.ref} id={`payee-${account.ref}`} />
            <Label
              htmlFor={`payee-${account.ref}`}
              className='min-w-0 flex-1 cursor-pointer font-normal'
            >
              <span className='flex min-w-0 flex-wrap items-center gap-x-2'>
                <span className='text-xs font-medium'>
                  {t(qyPayeeChannelKey(account.channel), account.channel)}
                </span>
                <span className='text-muted-foreground truncate font-mono text-xs'>
                  {account.masked}
                </span>
                {account.label !== '' && (
                  <span className='text-muted-foreground truncate text-xs'>
                    {account.label}
                  </span>
                )}
              </span>
            </Label>
            <Button
              type='button'
              variant='ghost'
              size='icon-sm'
              aria-label={t('qy_wd_payee_delete')}
              disabled={props.disabled === true || deletePayee.isPending}
              onClick={() => deletePayee.mutate(account.ref)}
            >
              <Trash2 aria-hidden='true' />
            </Button>
          </div>
        ))}

        <div
          className={cn(
            'flex items-center gap-2 rounded-md border p-2.5',
            isNew && 'border-primary'
          )}
        >
          <RadioGroupItem value={QY_PAYEE_NEW} id='payee-new' />
          <Label
            htmlFor='payee-new'
            className='flex-1 cursor-pointer font-normal'
          >
            {t('qy_wd_payee_new')}
          </Label>
        </div>
      </RadioGroup>

      {isNew && (
        <div className='space-y-3 rounded-md border p-3'>
          <div className='space-y-1.5'>
            <Label htmlFor={channelId}>{t('qy_wd_channel')}</Label>
            <NativeSelect
              id={channelId}
              className='w-full'
              value={props.channel}
              disabled={props.disabled}
              onChange={(event) => {
                // 换渠道必须清空已填字段：不同渠道的字段名完全不同，
                // 留着旧值会把"银行卡号"当成"支付宝账号"提交上去。
                props.onChannelChange(event.target.value)
                props.onValuesChange({})
              }}
            >
              {props.channels.map((channel) => (
                <NativeSelectOption key={channel} value={channel}>
                  {t(qyPayeeChannelKey(channel), channel)}
                </NativeSelectOption>
              ))}
            </NativeSelect>
            {props.errors._channel != null && (
              <p className='text-destructive text-sm'>
                {t(props.errors._channel)}
              </p>
            )}
          </div>

          {spec.map((field) => (
            <PayeeField
              key={field.key}
              labelKey={field.labelKey}
              required={field.required}
              value={props.values[field.key] ?? ''}
              errorKey={props.errors[field.key]}
              disabled={props.disabled}
              onChange={(value) =>
                props.onValuesChange({ ...props.values, [field.key]: value })
              }
            />
          ))}

          <div className='flex flex-wrap items-center gap-2'>
            <Button
              type='button'
              variant='outline'
              size='sm'
              disabled={
                props.disabled === true ||
                savePayee.isPending ||
                atAccountLimit ||
                Object.keys(validateQyPayee(props.channel, props.values))
                  .length > 0
              }
              onClick={() =>
                savePayee.mutate({
                  channel: props.channel,
                  label: '',
                  payee: normalizeQyPayee(props.channel, props.values),
                })
              }
            >
              <Save aria-hidden='true' />
              {t('qy_wd_payee_save')}
            </Button>
            <p className='text-muted-foreground text-xs'>
              {atAccountLimit
                ? t('qy_wd_payee_limit_reached', { max: props.accountMax })
                : t('qy_wd_payee_save_hint')}
            </p>
          </div>
        </div>
      )}
    </div>
  )
}

function PayeeField(props: {
  labelKey: string
  required: boolean
  value: string
  errorKey?: string
  disabled?: boolean
  onChange: (value: string) => void
}) {
  const { t } = useTranslation()
  const id = useId()

  return (
    <div className='space-y-1.5'>
      <Label htmlFor={id}>
        {t(props.labelKey)}
        {props.required && (
          <span className='text-destructive' aria-hidden='true'>
            *
          </span>
        )}
      </Label>
      <Input
        id={id}
        value={props.value}
        autoComplete='off'
        disabled={props.disabled}
        aria-invalid={props.errorKey != null}
        onChange={(event) => props.onChange(event.target.value)}
      />
      {props.errorKey != null && (
        <p className='text-destructive text-sm'>{t(props.errorKey)}</p>
      )}
    </div>
  )
}
