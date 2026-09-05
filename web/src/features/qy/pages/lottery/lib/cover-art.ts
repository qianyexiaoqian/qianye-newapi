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
 * 无封面时的兜底插画按玩法选:双色球 / 竞猜 / 转盘 / 其余抽奖各一张。
 * 文件在 `web/public/qy/art/<name>.jpg`(gpt-image-2 按主题色生成,1280 宽 JPEG,
 * 构建期随 public 目录一起 go:embed 进二进制)。取不到时组件再退到玩法图标。
 */
export function qyLotArtName(activity: {
  kind?: string
  draw_mode?: string
}): 'ball' | 'guess' | 'wheel' | 'draw' {
  if (activity.draw_mode === 'ball') return 'ball'
  if (activity.draw_mode === 'wheel') return 'wheel'
  if (activity.kind === 'guess') return 'guess'
  return 'draw'
}
