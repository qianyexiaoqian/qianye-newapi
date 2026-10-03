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

// 首页走二开落地页(`features/qy/landing`)。
//
// 它用的 40 个 `qy_home_*` 文案键曾经从未写进任何语言包,页面因此整页显示变量名
// (qy_home_title_1、QY_HOME_STAMP_STATION…),自 2026-09-08 的 v208 构建起一直
// 如此;2026-09-14 已按被删的 `home-{en,zh}.json`(见 9bd858acd^)的口径把中英
// 两套补齐,这条路由随之换回。
//
// 后台「设置 → 首页内容」配了内容时,`QyLanding` 自己会交回上游的 `<Home>`,
// 这里不需要再判一次。
export const Route = createFileRoute('/')({
  component: QyLanding,
})
