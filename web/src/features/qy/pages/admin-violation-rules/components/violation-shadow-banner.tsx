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
  /**
   * 解除熔断。省略即只读展示（违规记录页复用这个横幅时就是只读的）。
   *
   * **这里不再有任何模式开关。** 曾经的「切到影子 / 切到真实 / 跟随配置文件」
   * 三个按钮改的是一个全局层，而项目方的原话是「切来切去的本来简单的功能搞得
   * 那么复杂」—— 模式现在绑在每条规则上，改模式就是去改那条规则。
   */
  onResetBreaker?: () => void
  isResetting?: boolean
  className?: string
}

/**
 * 规则模式概览 + 熔断告警。
 *
 * 这一块回答两个问题，都是删掉全局开关之后**只能由数据回答**的：
 *
 *  1. **现在到底有没有规则在真实扣钱？** 以前这是一个全局布尔，一眼能看到；
 *     现在它是「N 条真实 / M 条影子」。不摆出来的话，运营会以为自己还在观察期，
 *     而其实已经有几条规则在扣费 —— 那正是旧横幅唯一还算有用的功能。
 *  2. **熔断响了吗？** 熔断不是模式，是机器踩的刹车。所以它平时整块不渲染，
 *     只在触发时以红色告警出现，并给出唯一的动作：解除。
 */
export function QyViolationShadowBanner(props: QyViolationShadowBannerProps) {
  const { t } = useTranslation()
  const stats = props.stats
  if (stats == null) return null

  // `breaker` 与 `rules` 在类型上是必填的，这里仍然按可选读。类型说的是
  // 「后端应该给」，运行期拿到的是「后端这次给了什么」：接口降级、老版本
  // 后端、反向代理裁剪字段，任何一种都会让这两个子对象缺席，而直读一层
  // 会在渲染中途抛 TypeError —— 整条规则页白屏，连"统计拿不到"这句话都
  // 显示不出来。少一块横幅远好过少一整页。
  const breaker = stats.breaker
  const now = Math.floor(Date.now() / 1000)
  // 以 forced_shadow_until 为准而不是只信 forced_shadow：后者是服务端算好的
  // 快照值，而这份统计会被缓存 30 秒，熔断到期后不应继续挂着红色告警。
  const forcedUntil = breaker?.forced_shadow_until ?? 0
  const tripped = breaker?.forced_shadow === true && forcedUntil > now

  const enforcing = stats.rules?.enforce_rule ?? 0
  const shadowing = stats.rules?.shadow_rule ?? 0

  if (tripped) {
    return (
      <Alert variant='destructive' className={props.className}>
        <ShieldAlert />
        <AlertTitle>{t('qy_vio_breaker_title')}</AlertTitle>
        <AlertDescription>
          <span className='block'>{t('qy_vio_breaker_desc')}</span>
          <span className='block'>
            {t('qy_vio_breaker_reason', {
              reason: breaker?.forced_shadow_reason ?? '',
            })}
          </span>
          <span className='block'>
            {t('qy_vio_breaker_until', {
              time: formatQyTs(forcedUntil),
            })}
          </span>
        </AlertDescription>
        {props.onResetBreaker != null && (
          <AlertAction>
            <Button
              type='button'
              variant='outline'
              size='sm'
              disabled={props.isResetting === true}
              onClick={props.onResetBreaker}
            >
              {t('qy_vio_breaker_reset')}
            </Button>
          </AlertAction>
        )}
      </Alert>
    )
  }

  // 没有任何规则在真实执行 = 整站还在观察期。这不是错误状态，但必须说清楚，
  // 否则管理员会以为规则已经在拦人。
  if (enforcing === 0) {
    return (
      <Alert
        className={cn(
          'border-warning/40 bg-warning/5 [&>svg]:text-warning',
          props.className
        )}
      >
        <EyeOff />
        <AlertTitle>{t('qy_vio_mode_all_shadow_title')}</AlertTitle>
        <AlertDescription>
          <span className='block'>
            {t('qy_vio_mode_all_shadow_desc', { shadow: shadowing })}
          </span>
          {/* shadow_hits 回答「若把这些规则切成真实，过去 N 小时会发生什么」。
              那是切模式之前唯一的决策依据。 */}
          <span className='block'>
            {t('qy_vio_mode_shadow_hits', {
              hits: formatQyCount(stats.shadow_count),
              hours: stats.hours,
            })}
          </span>
        </AlertDescription>
      </Alert>
    )
  }

  return (
    <Alert
      className={cn(
        'border-success/40 bg-success/5 [&>svg]:text-success',
        props.className
      )}
    >
      <ShieldCheck />
      <AlertTitle>
        {t('qy_vio_mode_mixed_title', { count: enforcing })}
      </AlertTitle>
      <AlertDescription>
        <span className='block'>
          {t('qy_vio_mode_mixed_desc', {
            enforce: enforcing,
            shadow: shadowing,
            blocked: stats.blocked,
            hours: stats.hours,
          })}
        </span>
        <span className='block'>
          {t('qy_vio_mode_shadow_hits', {
            hits: formatQyCount(stats.shadow_count),
            hours: stats.hours,
          })}
        </span>
      </AlertDescription>
    </Alert>
  )
}
