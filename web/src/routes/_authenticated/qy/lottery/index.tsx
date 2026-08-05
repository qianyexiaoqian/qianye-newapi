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
import { createFileRoute } from '@tanstack/react-router'

import { QyLotteryHub } from '@/features/qy/pages/lottery/hub'

/**
 * 需求 2：这一页从「抽奖大厅」升成了选择夹宿主（抽奖 / 竞猜 / 我的参与）。
 * 大厅本身降级成第一张标签的正文（`pages/lottery/index.tsx`）。
 */
export const Route = createFileRoute('/_authenticated/qy/lottery/')({
  component: QyLotteryHub,
})
