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
import { MapPin } from 'lucide-react'
import { useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'

import { qyErrorMessage } from '../../../lib/api'
import { qyKeys } from '../../../lib/query-keys'
import { qyRuneLength } from '../../lib/constants'
import { setQyMallOrderAddress } from '../api'

/** 与后端 `purchase.go` / `api_user.go` 的 maxAddressRunes / maxContactRunes 同值。 */
const MAX_ADDRESS_RUNES = 500
const MAX_CONTACT_RUNES = 128

/**
 * 实物奖品单的收货地址补填表单。
 *
 * 同一份表单挂在两处：转盘的中奖结果屏（当场填，不必再找）与「我的订单」详情
 * （关掉结果屏之后还能填）。两处只有这一个组件 —— 地址 / 联系方式的长度上限、
 * 「只能填一次」这条规则、两个错误码的文案，都不该有第二份。
 *
 * ## 只能填一次
 *
 * 后端 `POST /mall/orders/:no/address` 对已有地址的单回 409 `qy_ml_address_exists`，
 * 非奖品单回 403 `qy_ml_not_prize_order`；两条都登记在 `QY_ERROR_CODE_I18N`。
 * 成功后不本地改状态，全量失效让列表与详情重取 `address_missing`。
 */
export function QyMallPrizeAddressForm(props: {
  orderNo: string
  /** 保存成功后（调用方通常切成"已填写"或关掉表单）。 */
  onSaved?: () => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const addressId = useId()
  const contactId = useId()
  const [address, setAddress] = useState('')
  const [contact, setContact] = useState('')

  const mutation = useMutation({
    mutationFn: () =>
      setQyMallOrderAddress(props.orderNo, {
        address: address.trim(),
        contact: contact.trim(),
      }),
    onSuccess: async () => {
      toast.success(t('qy_ml_prize_address_saved'))
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
      props.onSaved?.()
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const addressLength = qyRuneLength(address.trim())
  const contactLength = qyRuneLength(contact.trim())
  const addressInvalid =
    addressLength === 0 || addressLength > MAX_ADDRESS_RUNES
  const contactInvalid =
    contactLength === 0 || contactLength > MAX_CONTACT_RUNES
  const canSubmit = !mutation.isPending && !addressInvalid && !contactInvalid

  return (
    <div
      className='space-y-3 rounded-lg border p-3'
      data-testid='qy-mall-prize-address-form'
    >
      <p className='inline-flex items-center gap-1.5 text-sm font-medium'>
        <MapPin aria-hidden='true' className='size-4 shrink-0' />
        {t('qy_ml_prize_address_title')}
      </p>
      <p className='text-muted-foreground text-xs'>
        {t('qy_ml_prize_address_desc')}
      </p>
      <div className='space-y-1.5'>
        <Label htmlFor={addressId}>{t('qy_ml_address')}</Label>
        <Textarea
          id={addressId}
          rows={3}
          value={address}
          autoComplete='street-address'
          placeholder={t('qy_ml_address_ph')}
          aria-invalid={addressLength > MAX_ADDRESS_RUNES}
          disabled={mutation.isPending}
          onChange={(event) => setAddress(event.target.value)}
        />
        <p className='text-muted-foreground text-end text-xs tabular-nums'>
          {t('qy_common_rune_counter', {
            used: addressLength,
            max: MAX_ADDRESS_RUNES,
          })}
        </p>
      </div>
      <div className='space-y-1.5'>
        <Label htmlFor={contactId}>{t('qy_ml_contact')}</Label>
        <Input
          id={contactId}
          value={contact}
          autoComplete='off'
          placeholder={t('qy_ml_contact_ph')}
          aria-invalid={contactLength > MAX_CONTACT_RUNES}
          disabled={mutation.isPending}
          onChange={(event) => setContact(event.target.value)}
        />
        <p className='text-muted-foreground text-xs'>
          {t('qy_ml_address_hint')}
        </p>
      </div>
      <Button
        type='button'
        size='sm'
        disabled={!canSubmit}
        onClick={() => mutation.mutate()}
      >
        {t('qy_ml_prize_address_submit')}
      </Button>
    </div>
  )
}
