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
/**
 * 推广页 happy-dom 渲染夹具。集成前它在 `stardust-screen.tsx` 之上叠过片段语言包
 * (`pending-invite.zh.json`),片段并入主包后退化成别名:`inviteZh` 就是主包,
 * `mountQyInviteScreen` 就是 `mountQyStardustScreen`。留着这个文件是为了让
 * 推广页的测试不用因为一次合并而改 import。
 */
import { mountQyStardustScreen, zh } from './stardust-screen'

/** 主语言包。断言从这里取中文文案,不从产品代码回读。 */
export const inviteZh: Record<string, string> = zh

export function mountQyInviteScreen(
  options: Parameters<typeof mountQyStardustScreen>[0]
): ReturnType<typeof mountQyStardustScreen> {
  return mountQyStardustScreen(options)
}

export {
  ROLE,
  cleanupQyStardustScreens,
  setQyProbeRole,
  type QyProbeRequest,
} from './stardust-screen'
