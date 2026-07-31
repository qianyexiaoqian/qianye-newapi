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
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { generateAffiliateLink } from '@/features/wallet/lib'

type InviteLinkCardProps = {
  code: string
  isLoading: boolean
}

/**
 * 邀请码与邀请链接。
 *
 * 链接由 `generateAffiliateLink`（上游 `features/wallet/lib`）生成，不自己拼
 * `?aff=`：注册页解析哪个 query 参数是上游说了算，两处各拼一遍迟早分叉，
 * 而分叉的后果是"用户分享出去的链接不计佣"。
 */
export function InviteLinkCard(props: InviteLinkCardProps) {
  const { t } = useTranslation()
  const link = props.code === '' ? '' : generateAffiliateLink(props.code)

  return (
    <Card data-card-hover='false'>
      <CardHeader>
        <CardTitle>{t('qy_aff_link_title')}</CardTitle>
        <CardDescription>{t('qy_aff_link_desc')}</CardDescription>
      </CardHeader>
      <CardContent className='space-y-3'>
        {props.isLoading ? (
          <>
            <Skeleton className='h-8 w-full' />
            <Skeleton className='h-8 w-full' />
          </>
        ) : (
          <>
            <LinkRow
              label={t('qy_aff_code')}
              value={props.code}
              copyTooltip={t('qy_aff_copy_code')}
              placeholder={t('qy_aff_code_unavailable')}
            />
            <LinkRow
              label={t('qy_aff_link')}
              value={link}
              copyTooltip={t('qy_aff_copy_link')}
              placeholder={t('qy_aff_code_unavailable')}
            />
          </>
        )}
      </CardContent>
    </Card>
  )
}

function LinkRow(props: {
  label: string
  value: string
  copyTooltip: string
  placeholder: string
}) {
  const empty = props.value === ''
  return (
    <div className='space-y-1'>
      <div className='text-muted-foreground text-xs font-medium'>
        {props.label}
      </div>
      <div className='flex items-center gap-2'>
        <Input
          value={empty ? props.placeholder : props.value}
          readOnly
          aria-label={props.label}
          className='min-w-0 flex-1 font-mono text-xs'
        />
        {/* 空值时不给复制按钮：复制一句提示文案没有任何意义。 */}
        {!empty && (
          <CopyButton
            value={props.value}
            variant='outline'
            className='size-8 shrink-0'
            iconClassName='size-4'
            tooltip={props.copyTooltip}
            aria-label={props.copyTooltip}
          />
        )}
      </div>
    </div>
  )
}
