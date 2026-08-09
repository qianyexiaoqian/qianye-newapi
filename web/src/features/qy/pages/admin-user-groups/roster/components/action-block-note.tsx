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

import { cn } from '@/lib/utils'

/**
 * 一个被禁用（或注定按不出结果）的入口旁边**必须有字**说明为什么。
 *
 * ── 为什么不是 `title` ──
 *
 * 项目方原话：「我选择了其他分组仍然无法删除」。他点了、没反应、也没有解释。
 * 此前这两个入口（表行上的删除、编辑弹窗里的改名）把理由塞在 `title` 里，
 * 而 `title` 只在 hover 时出现 —— **禁用的按钮在多数浏览器上根本不派发指针
 * 事件**，那句解释因此永远不会显示。运营看到的就是一个死按钮。
 *
 * 所以理由改成常驻的一行字，与按钮同处一个容器。它同时是无障碍上的正解：
 * 禁用按钮不参与 Tab 序，读屏也读不到它的 `title`。
 *
 * ── 它印的是**短状态标签**，不是完整理由 ──
 *
 * 完整理由一律来自后端（删除弹窗的 `block_reason`、编辑弹窗的
 * `rename_block_reason`）。前端在这里复述一遍会造出两份没有任何一致性检查的
 * 文本，后端改口径时界面上仍旧是旧的那一份；而且它落在表格最右一列，
 * 整段渲染会把行撑到七八行高。
 */
export function QyUgrActionBlockNote(props: {
  /** `null` = 这个入口现在没有话要说，什么都不渲染。 */
  noteKey: string | null
  className?: string
}) {
  const { t } = useTranslation()
  if (props.noteKey == null) return null
  return (
    <p
      data-slot='qy-ugr-block-note'
      className={cn(
        'text-muted-foreground text-xs leading-5 break-words',
        props.className
      )}
    >
      {t(props.noteKey)}
    </p>
  )
}
