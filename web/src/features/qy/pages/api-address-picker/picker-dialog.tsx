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
import { Label } from '@/components/ui/label'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { Spinner } from '@/components/ui/spinner'

import { QyResponsiveDialog } from '../../components/qy-responsive-dialog'
import { qyErrorMessage } from '../../lib/api'
import { qyApiAddressesQuery } from './api'
import { qyResolveAddressOptions } from './resolve-options'

type Props = {
  /**
   * 非 null 即打开，同时也是待处理的密钥。
   *
   * 两者合成一个字段而不是 `open` + `pendingKey`：它们本来就同生共死，
   * 拆成两个状态就存在「开着但没有密钥」这种构造得出来、却毫无意义的组合。
   */
  pendingKey: string | null
  /**
   * 「选完之后会发生什么」。
   *
   * 标题（「选择 API 地址」）对每个入口都一样，说明与确认按钮不一样：复制链接
   * 信息的按钮就该写「复制链接信息」，CC Switch 的按钮写「复制」是在骗人 ——
   * 点下去出现的是另一个配置窗口。所以这两句由调用方给，而不是写死在这里。
   */
  description: string
  confirmLabel: string
  onClose: () => void
  onPick: (url: string) => void
}

/**
 * 各入口（复制链接信息 / CC Switch）在动手之前共用的地址选择窗口。
 *
 * 只在**有得选**的时候才会被打开（判定见 `index.tsx` 的 `useQyApiAddressPicker`）。
 */
export function QyApiAddressPickerDialog(props: Props) {
  const { t } = useTranslation()
  const open = props.pendingKey != null
  const query = useQuery({ ...qyApiAddressesQuery(), enabled: open })
  const options = qyResolveAddressOptions(query.data, t('qy_aa_site_default'))
  const [picked, setPicked] = useState('')
  const value = picked !== '' ? picked : (options[0]?.url ?? '')

  return (
    <QyResponsiveDialog
      open={open}
      onOpenChange={(next) => {
        if (next) return
        setPicked('')
        props.onClose()
      }}
      title={t('qy_aa_pick_title')}
      description={props.description}
      contentClassName='sm:max-w-lg'
      footer={
        <>
          <Button variant='outline' onClick={props.onClose}>
            {t('qy_aa_cancel')}
          </Button>
          <Button
            disabled={query.isLoading || value === ''}
            onClick={() => {
              setPicked('')
              props.onPick(value)
            }}
          >
            {props.confirmLabel}
          </Button>
        </>
      }
    >
      {query.isLoading ? (
        <div className='flex justify-center py-8'>
          <Spinner />
        </div>
      ) : (
        <div className='space-y-3'>
          {/* 取数失败（503 / 网络）不挡路：仍然能用站点地址复制。但必须把
              "这不是完整清单"说出来，否则用户会以为运营一条都没配。 */}
          {query.isError && (
            <p className='text-destructive text-xs'>
              {qyErrorMessage(query.error, t)}
            </p>
          )}
          <RadioGroup
            value={value}
            onValueChange={(next) => setPicked(next ?? '')}
            className='gap-2'
          >
            {options.map((option) => (
              <Label
                key={option.id}
                htmlFor={`qy-aa-${option.id}`}
                className='hover:bg-accent/50 flex cursor-pointer items-start gap-3 rounded-lg border p-3'
              >
                <RadioGroupItem
                  id={`qy-aa-${option.id}`}
                  value={option.url}
                  className='mt-0.5'
                />
                <span className='min-w-0 flex-1 space-y-0.5'>
                  <span className='block text-sm font-medium'>
                    {option.name}
                  </span>
                  <span className='text-muted-foreground block truncate font-mono text-xs'>
                    {option.url}
                  </span>
                  {option.remark !== '' && (
                    <span className='text-muted-foreground block text-xs'>
                      {option.remark}
                    </span>
                  )}
                </span>
              </Label>
            ))}
          </RadioGroup>
        </div>
      )}
    </QyResponsiveDialog>
  )
}
