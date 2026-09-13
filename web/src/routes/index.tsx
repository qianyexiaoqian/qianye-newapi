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

import { Home } from '@/features/home'

// 首页暂时走回上游的 `features/home`。
//
// 二开落地页(`features/qy/landing`)的代码留在树里,没有删,但它用的 40 个
// `qy_home_*` 文案键**从未写进任何语言包** —— 页面因此整页显示变量名
// (qy_home_title_1、QY_HOME_STAMP_STATION…),自 2026-09-08 的 v208 构建起
// 一直是这个样子。文案要按站点口径中英两套地写,不是机械补键,所以先切回默认,
// 等文案定了再把 component 换回 QyLanding。
export const Route = createFileRoute('/')({
  component: Home,
})
