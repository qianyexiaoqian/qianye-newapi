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
import { CircleCheck, Coins, Lock, Ticket } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import {
  QyPhaseTrack,
  type QyPhaseStep,
  type QyPhaseStepState,
} from '../../../components/art/qy-phase-track'
import type { QyLotDrawMode, QyLotStatus } from '../types'

/** 一场活动的生命周期，顺序就是它在后端状态机里的顺序。草稿不在线上出现。 */
const PHASES = [
  { key: 'published', labelKey: 'qy_lot_st_published', icon: Ticket },
  { key: 'locked', labelKey: 'qy_lot_st_locked', icon: Lock },
  { key: 'settling', labelKey: 'qy_lot_st_settling', icon: Coins },
  { key: 'finished', labelKey: 'qy_lot_st_finished', icon: CircleCheck },
] as const

/**
 * 详情页头那一行「现在到哪一步了」。
 *
 * 状态徽章只说"此刻是什么"，看不出前后还有几步；证据链那条纵向时间线在右列
 * 下方，要滚过去才看得到。这一行把四步摊平放在标题下面：走过的填实、当前
 * 呼吸、未到的灰色 —— 不读字也知道离开奖还有几步。
 *
 * 转盘是即时开奖，没有"结算中"这一步（每一转当场到账），少画一个节点；
 * 画上去只会让用户等一个永远不会亮的灯。
 */
export function QyLotPhaseTrack(props: {
  status: QyLotStatus
  drawMode?: QyLotDrawMode
  className?: string
}) {
  const { t } = useTranslation()
  const phases =
    props.drawMode === 'wheel'
      ? PHASES.filter((phase) => phase.key !== 'settling')
      : PHASES
  const currentIndex = phases.findIndex((phase) => phase.key === props.status)

  const steps: QyPhaseStep[] = phases.map((phase, index) => {
    let state: QyPhaseStepState = 'pending'
    if (currentIndex >= 0) {
      if (index < currentIndex) state = 'done'
      // 终态不呼吸：「已结束」不是"正在进行"。
      else if (index === currentIndex) {
        state = phase.key === 'finished' ? 'done' : 'current'
      }
    }
    return {
      key: phase.key,
      label: t(phase.labelKey),
      icon: phase.icon,
      state,
    }
  })

  return (
    <QyPhaseTrack
      steps={steps}
      label={t('qy_lot_phase_track_aria')}
      className={props.className}
    />
  )
}
