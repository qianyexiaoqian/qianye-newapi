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
import { Eye, Lock, Wallet } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { cn } from '@/lib/utils'

type QyGpModeBannerProps = {
  shadowMode: boolean
  /** 已启用的规则条数，用来把「影响面有多大」说成具体数字。 */
  enabledRuleCount: number
}

/**
 * 页面顶部的计费模式横幅。
 *
 * 这是整页第一眼要看到的东西：同一张规则表在影子模式下是一份预演，在真实模式
 * 下是正在扣钱的配置，两者长得一样但后果差了一个数量级。所以横幅**无条件渲染**
 * （不折叠、不放进 Tab），并且两种模式用完全不同的配色与图标，让人扫一眼就能
 * 分辨，而不必去读那行小字。
 *
 * **这里没有切换开关，是刻意的。** 后端 `RegisterAdminRoutes` 只注册了规则
 * CRUD、试算与对账三类接口，模式取自扩展配置文件
 * （`qianye/config` 的 `group_pricing.shadow_mode`），没有任何写接口能改它。
 * 与其画一个点了会 404 的开关，不如明确告诉运营去哪里改 —— 而且「从影子切到
 * 真实计费」需要改配置并重启，这道额外的摩擦本身就是一层保护：这个动作的
 * 含义是「从现在起真的按新价扣用户的钱」，不适合做成一次点击。
 */
export function QyGpModeBanner(props: QyGpModeBannerProps) {
  const { t } = useTranslation()
  const shadow = props.shadowMode

  return (
    <div
      className={cn(
        'flex flex-wrap items-start justify-between gap-3 rounded-lg border p-3',
        shadow
          ? 'border-warning/50 bg-warning/5'
          : 'border-destructive/50 bg-destructive/5'
      )}
    >
      <div className='flex min-w-0 flex-1 items-start gap-2.5'>
        {shadow ? (
          <Eye
            className='text-warning mt-0.5 size-4 shrink-0'
            aria-hidden='true'
          />
        ) : (
          <Wallet
            className='text-destructive mt-0.5 size-4 shrink-0'
            aria-hidden='true'
          />
        )}
        <div className='min-w-0 space-y-1'>
          <p
            className={cn(
              'text-sm font-semibold',
              shadow ? 'text-warning' : 'text-destructive'
            )}
          >
            {t(shadow ? 'qy_gp_mode_shadow_title' : 'qy_gp_mode_live_title')}
          </p>
          <p className='text-muted-foreground text-xs'>
            {t(shadow ? 'qy_gp_mode_shadow_desc' : 'qy_gp_mode_live_desc', {
              rules: props.enabledRuleCount,
            })}
          </p>
        </div>
      </div>

      <p className='text-muted-foreground flex max-w-sm shrink-0 items-start gap-1.5 text-xs'>
        <Lock className='mt-0.5 size-3.5 shrink-0' aria-hidden='true' />
        {t('qy_gp_mode_readonly_hint')}
      </p>
    </div>
  )
}
