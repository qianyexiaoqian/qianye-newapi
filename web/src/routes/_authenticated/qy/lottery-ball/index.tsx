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
import { createFileRoute, redirect } from '@tanstack/react-router'

import { qyTabHash } from '@/features/qy/lib/pages'

/**
 * 双色球 —— 本页是 `/qy/lottery` 选择夹里的一张标签，没有独立入口。
 *
 * 它仍然需要一个真实路由：`/qy/lottery-ball` 已经登记进 `QY_PAGES` 与
 * GATE 编号表，而那两处是**对外可见的稳定标识**。少了这个文件，任何人
 * 手敲这个地址（或按 `/qy/lottery-guess` 的经验类推）拿到的是 404，
 * 而不是像它的三张姊妹标签那样被送到宿主页的对应位置。
 *
 * 目标 hash 由 `qyTabHash` 现算，与宿主页认标签用的是同一个函数 ——
 * 不可能出现"跳过去了但选中的是另一张"。
 *
 * `replace`：这个地址不该留在历史栈里，否则用户按返回键会被立刻再弹回来。
 */
export const Route = createFileRoute('/_authenticated/qy/lottery-ball/')({
  beforeLoad: () => {
    throw redirect({
      to: '/qy/lottery',
      hash: qyTabHash('/qy/lottery-ball'),
      replace: true,
    })
  },
})
