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

import { qyStardustName } from '../lib/format-sd'
import { useQyConfig } from './use-qy-config'

/**
 * 星屑的单位名（运营可改，默认「星屑」）。
 *
 * 所有星屑金额旁边的单位都从这里取：`QySdAmount` / `QySdInput` 以及那些把金额
 * 插进句子里的地方（报名弹窗的「立即扣除」那一句）。硬编码「星屑」两个字的
 * 后果是运营改了名之后界面上一半叫新名字、一半叫旧名字。
 */
export function useStardustName(): string {
  const { t } = useTranslation()
  const config = useQyConfig()
  return qyStardustName(config.stardust.name, t)
}
