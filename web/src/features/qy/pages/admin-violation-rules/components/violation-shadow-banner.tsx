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
import { EyeOff, ShieldAlert, ShieldCheck } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import {
  Alert,
  AlertAction,
  AlertDescription,
  AlertTitle,
} from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

import { formatQyCount, formatQyTs } from '../../ops/format'
import type { QyViolationStats } from '../types'

type QyViolationShadowBannerProps = {
  stats: QyViolationStats | undefined
  onResetBreaker?: () => void
  isResetting?: boolean
  className?: string
}

/**
 * 影子模式 / 熔断状态横幅。
 *
 * **规则编辑界面必须一眼看到当前是不是影子模式。** 两种误判都很贵：
 *   - 以为在影子模式、其实在真实模式 → 一条正则上线就是全站误封；
 *   - 以为在真实模式、其实在影子模式 → 管理员会不断加码规则，
 *     等到影子模式关闭时全部同时生效。
 *
 * 因此真实模式也要显示（绿色），而不是「有问题才提示」。
 */
export function QyViolationShadowBanner(props: QyViolationShadowBannerProps) {
  const { t } = useTranslation()
  const stats = props.stats
  if (stats == null) return null

  const breaker = stats.breaker
  const now = Math.floor(Date.now() / 1000)
  const forced = breaker.forced_shadow_until > now

  if (!breaker.shadow) {
    return (
      <Alert
        className={cn(
          'border-success/40 bg-success/5 [&>svg]:text-success',
          props.className
        )}
      >
        <ShieldCheck />
        <AlertTitle>{t('qy_vio_mode_live_title')}</AlertTitle>
        <AlertDescription>
          {t('qy_vio_mode_live_desc', {
            rules: stats.rules.prompt_rule + stats.rules.post_rule,
            blocked: stats.blocked,
            hours: stats.hours,
          })}
        </AlertDescription>
      </Alert>
    )
  }

  return (
    <Alert
      variant={forced ? 'destructive' : 'default'}
      className={cn(
        !forced && 'border-warning/40 bg-warning/5 [&>svg]:text-warning',
        props.className
      )}
    >
      {forced ? <ShieldAlert /> : <EyeOff />}
      <AlertTitle>
        {forced
          ? t('qy_vio_mode_breaker_title')
          : t('qy_vio_mode_shadow_title')}
      </AlertTitle>
      <AlertDescription>
        <span className='block'>{t('qy_vio_mode_shadow_desc')}</span>
        <span className='block'>
          {t('qy_vio_mode_shadow_reason', {
            reason:
              breaker.shadow_reason === ''
                ? t('qy_vio_mode_reason_config')
                : breaker.shadow_reason,
          })}
        </span>
        {forced && (
          <span className='block'>
            {t('qy_vio_mode_breaker_until', {
              time: formatQyTs(breaker.forced_shadow_until),
            })}
          </span>
        )}
        {/* shadow_hits 是切真实模式前唯一的决策依据：它回答
            「如果现在打开真实模式，过去 N 小时会有多少用户被扣费或封号」。 */}
        <span className='block'>
          {t('qy_vio_mode_shadow_hits', {
            hits: formatQyCount(stats.shadow_count),
            hours: stats.hours,
          })}
        </span>
      </AlertDescription>
      {forced && props.onResetBreaker != null && (
        <AlertAction>
          <Button
            type='button'
            variant='outline'
            size='sm'
            disabled={props.isResetting}
            onClick={props.onResetBreaker}
          >
            {t('qy_vio_breaker_reset')}
          </Button>
        </AlertAction>
      )}
    </Alert>
  )
}
