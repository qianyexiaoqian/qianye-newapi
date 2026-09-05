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

import type { QyLotHallState } from '../lottery/components/lottery-hall-list'
import { QyWheelList } from './components/wheel-list'

/**
 * 星屑转盘（「抽奖竞猜」选择夹的第四张标签）。
 *
 * 项目方原话（2026-09-05）：「星屑转盘的页面移动到抽奖竞猜里面去。」
 *
 * 它曾是一个独立页面（`QyWheel`，侧栏上自己占一行）：design-15 §7.5 的理由是
 * 转盘即时开奖，卡片上"奖池 / 截止 / 开奖"三个数的语义与批次玩法对不上。那条
 * 理由现在仍然成立，只是解决的方式换了 —— 它不是大厅列表里的一类活动，而是
 * 一张有自己卡片与自己取数的标签：正文照旧按 `draw_mode=wheel` 拉列表，`lane`
 * 一律不发（两者同给是 400）。
 *
 * 只是正文：区段头与标签栏由宿主 `lottery/hub.tsx` 提供（标签页里再套一层区段
 * 头会得到两级标题），"入口被关掉"的那句说明也由宿主统一给。`/qy/wheel` 这个
 * 地址保留成重定向（`routes/_authenticated/qy/wheel/index.tsx`）。
 *
 * 详情、转动、我的转动与证据链都在 `/qy/lottery/$actNo` 那一张详情页上按
 * `draw_mode` 换正文 —— 一场转盘就是一场活动，规则、承诺、公开名单与其余玩法
 * 共用同一套证据链展示，只是"参与"这一格换成了转盘。
 */
export function QyWheelBody(props: QyLotHallState) {
  const { t } = useTranslation()

  return (
    <div className='space-y-3'>
      <p className='text-muted-foreground text-sm'>
        {t('qy_lot_wheel_page_desc')}
      </p>
      <QyWheelList {...props} />
    </div>
  )
}
