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

import { StatusBadge } from '@/components/status-badge'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { formatTimestampToDate } from '@/lib/format'

import type { QyPayPasswordStatus } from '../../../lib/pay-password'

/**
 * 支付密码状态卡。
 *
 * 明确回答四个问题：设了没有、锁没锁、还剩几次、锁多久。
 * 「还剩几次 / 锁多久」直接取后端下发的 `max_attempts` / `lock_minutes` ——
 * 它们是运营在 qy_settings 里可改的值，前端硬编码一份必然与后台配置对不上。
 */
export function PayPasswordStatusCard(props: { status: QyPayPasswordStatus }) {
  const { t } = useTranslation()
  const status = props.status

  return (
    <Card data-card-hover='false'>
      <CardHeader>
        <CardTitle>{t('qy_pp_status_title')}</CardTitle>
        <CardDescription>{t('qy_pp_status_desc')}</CardDescription>
      </CardHeader>
      <CardContent className='space-y-3 text-sm'>
        <Row label={t('qy_pp_status_set')}>
          <StatusBadge
            variant={status.is_set ? 'success' : 'warning'}
            label={t(status.is_set ? 'qy_pp_state_set' : 'qy_pp_state_unset')}
            copyable={false}
          />
        </Row>
        <Row label={t('qy_pp_status_lock')}>
          <StatusBadge
            variant={status.locked ? 'danger' : 'success'}
            label={
              status.locked
                ? t('qy_pp_state_locked_until', {
                    time: formatTimestampToDate(status.locked_until),
                  })
                : t('qy_pp_state_unlocked')
            }
            copyable={false}
          />
        </Row>
        {status.is_set && (
          <Row label={t('qy_pp_status_remaining')}>
            <span className='tabular-nums'>
              {t('qy_pp_status_remaining_value', {
                remaining: status.remaining_attempts,
                max: status.max_attempts,
              })}
            </span>
          </Row>
        )}
        <Row label={t('qy_pp_status_policy')}>
          <span className='text-muted-foreground'>
            {t('qy_pp_status_policy_value', {
              max: status.max_attempts,
              minutes: status.lock_minutes,
            })}
          </span>
        </Row>
        {status.changed_at > 0 && (
          <Row label={t('qy_pp_status_changed_at')}>
            <span className='text-muted-foreground tabular-nums'>
              {formatTimestampToDate(status.changed_at)}
            </span>
          </Row>
        )}
      </CardContent>
    </Card>
  )
}

function Row(props: { label: string; children: React.ReactNode }) {
  return (
    <div className='flex items-center justify-between gap-3'>
      <span className='text-muted-foreground'>{props.label}</span>
      {props.children}
    </div>
  )
}
