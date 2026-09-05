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
import {
  Coins,
  OctagonAlert,
  ServerOff,
  TriangleAlert,
  Unplug,
  UserX,
  type LucideIcon,
} from 'lucide-react'

import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'

import type { QyGmRowWarning } from '../types'

/**
 * 行级待办的**按类型图标表**。key 与后端 `qianye/modules/groupmatrix` 的
 * `Warn*` 常量一一对应，改一处必须同步另一处。
 *
 * 项目方原话:「⚠这个图标可以有多个,多个错误用不同的图标来表示」。所以每一类
 * 问题一个图标、一行并排显示,而不是所有问题共用一个 ⚠。
 *
 * `deprecated_model_group` 是唯一一条会真的挡住用户(「分组已被弃用」403)的,
 * 用红色 destructive;其余是"配了但未必是本意"的软问题,用琥珀色。
 */
const META: Record<
  string,
  { Icon: LucideIcon; className: string; label: string }
> = {
  self_excluded: {
    Icon: UserX,
    className: 'text-amber-500 dark:text-amber-400',
    label: '范围不含自己',
  },
  topup_zero: {
    Icon: Coins,
    className: 'text-amber-500 dark:text-amber-400',
    label: '充值倍率异常',
  },
  empty_token_no_route: {
    Icon: Unplug,
    className: 'text-amber-500 dark:text-amber-400',
    label: '空分组令牌会失败',
  },
  grant_no_channel: {
    Icon: ServerOff,
    className: 'text-amber-500 dark:text-amber-400',
    label: '授权了无渠道的分组',
  },
  deprecated_model_group: {
    Icon: OctagonAlert,
    className: 'text-destructive',
    label: '引用了已删除的模型分组',
  },
}

const FALLBACK = {
  Icon: TriangleAlert,
  className: 'text-amber-500 dark:text-amber-400',
  label: '配置警告',
}

type Grouped = {
  code: string
  meta: { Icon: LucideIcon; className: string; label: string }
  texts: string[]
}

/**
 * 按 code 归组:同一类的多条(少见)折进一个图标,悬停时列全。
 * 图标顺序按 code 稳定,不随后端数组顺序抖动。
 */
export function qyGmGroupRowWarnings(warnings: QyGmRowWarning[]): Grouped[] {
  const byCode = new Map<string, string[]>()
  for (const w of warnings) {
    const arr = byCode.get(w.code) ?? []
    arr.push(w.text)
    byCode.set(w.code, arr)
  }
  return [...byCode.entries()]
    .map(([code, texts]) => ({ code, texts, meta: META[code] ?? FALLBACK }))
    .sort((a, b) => a.code.localeCompare(b.code))
}

/**
 * 一行里的多枚类型图标(Tooltip 版)。用于普通表格单元格。
 *
 * ⚠ **不能用在 `<button>` 里**:Tooltip 触发器本身是交互元素,嵌进按钮是不合法
 * 的嵌套。按钮内的场景(主从式左列表)用 {@link QyGmRowWarningIconsInline}。
 */
export function QyGmRowWarningIcons(props: { warnings: QyGmRowWarning[] }) {
  const groups = qyGmGroupRowWarnings(props.warnings)
  if (groups.length === 0) return null
  return (
    <span className='inline-flex shrink-0 items-center gap-1'>
      {groups.map(({ code, meta, texts }) => (
        <Tooltip key={code}>
          <TooltipTrigger
            render={<span className='inline-flex' aria-label={meta.label} />}
          >
            <meta.Icon
              aria-hidden='true'
              className={`h-4 w-4 ${meta.className}`}
            />
          </TooltipTrigger>
          <TooltipContent className='max-w-sm'>
            <div className='space-y-1 py-0.5 text-xs'>
              <div className='font-medium'>{meta.label}</div>
              {texts.map((text) => (
                <div key={text}>{text}</div>
              ))}
            </div>
          </TooltipContent>
        </Tooltip>
      ))}
    </span>
  )
}

/**
 * 一行里的多枚类型图标(原生 title 版,纯 phrasing content)。
 *
 * 用于**被塞进 `<button>` 里**的场景(主从式左列表的行同时是下拉触发器):
 * 那里不能再挂 Tooltip 触发器,退化成原生 title,内容与 Tooltip 版一致。
 */
export function QyGmRowWarningIconsInline(props: {
  warnings: QyGmRowWarning[]
}) {
  const groups = qyGmGroupRowWarnings(props.warnings)
  if (groups.length === 0) return null
  return (
    <span className='inline-flex shrink-0 items-center gap-1'>
      {groups.map(({ code, meta, texts }) => (
        <span
          key={code}
          className='inline-flex'
          title={`${meta.label}\n${texts.join('\n')}`}
        >
          <meta.Icon
            aria-hidden='true'
            className={`h-3.5 w-3.5 ${meta.className}`}
          />
          <span className='sr-only'>
            {meta.label}: {texts.join('; ')}
          </span>
        </span>
      ))}
    </span>
  )
}
