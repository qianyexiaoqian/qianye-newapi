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
import { PackageX } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'

import { QySdAmount } from '../../../components/qy-sd-amount'
import { useStardustName } from '../../../hooks/use-stardust-name'
import { formatSdWithUnit } from '../../../lib/format-sd'
import type { QyLotActivityDetail } from '../../lottery/types'
import { isQyWheelSoldOut } from '../lib/spin'
import { useQyReducedMotion } from '../lib/use-reduced-motion'
import { QyWheelFace } from './wheel-face'
import { QyWheelSpinDialog } from './wheel-spin-dialog'

/**
 * 详情页上的转盘面板：盘面、余额、「转一次」与它的弹窗。
 *
 * 它替换的是批次玩法的「参与」按钮那一格。按钮亮不亮只是"别让用户白按一次"，
 * 真正说了算的是后端在活动行锁内的那条 CAS（`published` 且在时间窗内），
 * 与奖档完整性校验。
 */
export function QyWheelSpinPanel(props: {
  activity: QyLotActivityDetail
  /** 活动此刻在时间窗内且玩法开着（详情页算好的那一个 `open`）。 */
  open: boolean
  /** 玩法被运营隐藏（置灰的按钮要带上原因）。 */
  playOpen: boolean
}) {
  const { activity } = props
  const { t } = useTranslation()
  const unit = useStardustName()
  const reducedMotion = useQyReducedMotion()
  const [dialogOpen, setDialogOpen] = useState(false)
  const soldOut = isQyWheelSoldOut(activity.spec)

  return (
    <Card data-card-hover='false'>
      <CardHeader>
        <CardTitle>{t('qy_lot_wheel_panel_title')}</CardTitle>
        <CardDescription>
          {t('qy_lot_wheel_panel_desc', {
            amount: formatSdWithUnit(activity.stake_quota, unit),
          })}
        </CardDescription>
      </CardHeader>
      <CardContent className='space-y-4'>
        <QyWheelFace
          spec={activity.spec}
          targetPpm={null}
          reducedMotion={reducedMotion}
        />
        {activity.stardust_balance != null && (
          <div className='flex items-center justify-between gap-2 text-sm'>
            <span className='text-muted-foreground'>
              {t('qy_sd_balance_current', { unit })}
            </span>
            <QySdAmount amount={activity.stardust_balance} />
          </div>
        )}
        <div className='space-y-2'>
          <Button
            className='w-full sm:w-auto'
            disabled={!props.open}
            onClick={() => setDialogOpen(true)}
          >
            {props.open
              ? t('qy_lot_wheel_spin_button')
              : t('qy_lot_join_unavailable')}
          </Button>
          {/* 发完即封盘：按钮灰掉的原因必须说出来，"不可参与"与"奖品已发完"
              对用户是两回事 —— 前者可以等，后者不必等。 */}
          {soldOut && (
            <p className='text-muted-foreground inline-flex items-center gap-1.5 text-xs'>
              <PackageX aria-hidden='true' className='size-3.5 shrink-0' />
              {t('qy_lot_wheel_sold_out_note')}
            </p>
          )}
          {!props.playOpen && (
            <p className='text-muted-foreground text-xs'>
              {t('qy_lot_play_hidden_note')}
            </p>
          )}
          {activity.my_entry_count > 0 && (
            <p className='text-muted-foreground text-xs'>
              {t('qy_lot_wheel_my_spin_count', {
                count: activity.my_entry_count,
              })}
            </p>
          )}
        </div>
      </CardContent>
      <QyWheelSpinDialog
        activity={activity}
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        canSpinAgain={props.open}
      />
    </Card>
  )
}
