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

import { QyLanding } from '@/features/qy/landing'

// 未登录首页走二开自己的落地页（`features/qy/landing`）。上游的
// `features/home` 一行不改，仍然承担「管理员配了自定义首页内容」那一支，
// 由 QyLanding 在检测到内容后交回给它。
export const Route = createFileRoute('/')({
  component: QyLanding,
})
