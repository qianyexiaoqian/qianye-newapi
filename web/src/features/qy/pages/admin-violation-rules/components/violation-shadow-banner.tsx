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
   * 设置全局影子开关；`null` = 清掉覆盖，重新跟随 YAML。
   *
   * 省略即只读展示。违规记录页复用这个横幅是为了「不要对着一堆没真正扣钱的
   * 记录做处置」，改模式是规则页的职责，两处都能改会让人不知道自己刚改的是哪个。
   */
  onSetShadow?: (shadow: boolean | null) => void
  isSaving?: boolean
  onResetBreaker?: () => void
  isResetting?: boolean
  className?: string
}

/**
 * 影子模式 / 熔断状态横幅，同时是**全局模式的唯一控制入口**。
 *
 * **规则编辑界面必须一眼看到当前是不是影子模式。** 两种误判都很贵：
 *   - 以为在影子模式、其实在真实模式 → 一条正则上线就是全站误封；
 *   - 以为在真实模式、其实在影子模式 → 管理员会不断加码规则，
 *     等到影子模式关闭时全部同时生效。
 *
 * 因此真实模式也要显示（绿色），而不是「有问题才提示」。
 *
 * 这一版补上了控件。上一版只在熔断（`forced`）时才渲染「解除熔断」按钮，
 * 而全局影子的默认来源是 YAML —— 于是最常见的那种影子状态下，整页一个可点的
 * 控件都没有，这就是需求原文说的「违规规则无法调整模式」的直接观感。
 */
export function QyViolationShadowBanner(props: QyViolationShadowBannerProps) {
  const { t } = useTranslation()
  const stats = props.stats
  if (stats == null) return null

  const breaker = stats.breaker
  const now = Math.floor(Date.now() / 1000)
  const forced = breaker.forced_shadow_until > now
  const overridden = breaker.shadow_override !== 'unset'

  /**
   * 模式切换按钮。全局影子与熔断是两回事：熔断期间照样允许改全局口径，
   * 只是熔断没解除之前它不会立刻生效 —— 描述里已经写明当前的真实原因。
   *
   * 没有传处理器就是只读展示，一个按钮都不渲染。
   */
  const setShadow = props.onSetShadow
  const resetBreaker = props.onResetBreaker
  const modeButtons =
    setShadow == null ? null : (
      <AlertAction>
        <div className='flex flex-wrap items-center gap-2'>
          <Button
            type='button'
            variant='outline'
            size='sm'
            disabled={props.isSaving === true || breaker.global_shadow}
            onClick={() => setShadow(true)}
          >
            {t('qy_vio_mode_set_shadow')}
          </Button>
          <Button
            type='button'
            variant='outline'
            size='sm'
            disabled={props.isSaving === true || !breaker.global_shadow}
            onClick={() => setShadow(false)}
          >
            {t('qy_vio_mode_set_live')}
          </Button>
          {overridden && (
            <Button
              type='button'
              variant='ghost'
              size='sm'
              disabled={props.isSaving === true}
              onClick={() => setShadow(null)}
            >
              {t('qy_vio_mode_follow_yaml', {
                value: breaker.config_shadow
                  ? t('qy_vio_mode_word_shadow')
                  : t('qy_vio_mode_word_live'),
              })}
            </Button>
          )}
          {forced && resetBreaker != null && (
            <Button
              type='button'
              variant='outline'
              size='sm'
              disabled={props.isResetting === true}
              onClick={resetBreaker}
            >
              {t('qy_vio_breaker_reset')}
            </Button>
          )}
        </div>
      </AlertAction>
    )

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
          <span className='block'>
            {t('qy_vio_mode_live_desc', {
              rules: stats.rules.prompt_rule + stats.rules.post_rule,
              blocked: stats.blocked,
              hours: stats.hours,
            })}
          </span>
          <span className='block'>
            {breaker.shadow_override === 'unset'
              ? t('qy_vio_mode_source_yaml')
              : t('qy_vio_mode_source_settings')}
          </span>
        </AlertDescription>
        {modeButtons}
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
            reason: reasonText(breaker.shadow_reason, t),
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
      {modeButtons}
    </Alert>
  )
}

/**
 * 把后端的影子原因翻成人话。
 *
 * `settings` / `config` 是两个必须分清的来源：前者能在这一页改回去，
 * 后者要改文件再重载（或者在这里写一条覆盖）。其余是熔断的触发描述，
 * 由后端拼好，原样展示。
 */
function reasonText(reason: string, t: (key: string) => string): string {
  if (reason === 'settings') return t('qy_vio_mode_reason_settings')
  if (reason === 'config' || reason === '')
    return t('qy_vio_mode_reason_config')
  return reason
}
