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
'use client'

import { Select as SelectPrimitive } from '@base-ui/react/select'
import {
  UnfoldMoreIcon,
  Tick02Icon,
  ArrowUp01Icon,
  ArrowDown01Icon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import * as React from 'react'

import { useMediaQuery } from '@/hooks'
import { cn } from '@/lib/utils'

/**
 * selectItemText 把一个 `<SelectItem>` 的子树折成纯文本。
 *
 * 只取字符串与数字叶子:图标、徽章之类的元素在关着的触发器上没有位置,
 * 而拿 ReactNode 当 label 会让 Base UI 的 typeahead(它对 label 做 String())
 * 匹配到 "[object Object]"。
 */
function selectItemText(node: React.ReactNode): string {
  if (node == null || typeof node === 'boolean') return ''
  if (typeof node === 'string') return node
  if (typeof node === 'number') return String(node)
  if (Array.isArray(node)) return node.map(selectItemText).join('')
  if (React.isValidElement(node)) {
    return selectItemText(
      (node.props as { children?: React.ReactNode }).children
    )
  }
  return ''
}

/**
 * collectSelectItemLabels 在 `<Select>` 的子树里找出全部 `<SelectItem>`,
 * 收成 Base UI 需要的 `value → 文案` 映射。
 */
function collectSelectItemLabels(
  node: React.ReactNode,
  out: Record<string, React.ReactNode>
) {
  React.Children.forEach(node, (child) => {
    if (!React.isValidElement(child)) return
    const props = child.props as {
      value?: unknown
      children?: React.ReactNode
    }
    if (child.type === SelectItem) {
      // value 为 null/undefined 的项一律跳过:Base UI 的 hasNullItemLabel 会因为
      // 映射里存在 "null" 这个键而**吞掉 placeholder**,那是一个与本修复无关的
      // 行为改变。
      if (props.value == null) return
      const label = selectItemText(props.children).trim()
      if (label !== '') out[String(props.value)] = label
      return
    }
    collectSelectItemLabels(props.children, out)
  })
}

/**
 * Select 在调用方没有显式给 `items` 时,自动从 `<SelectItem>` 子树里推出它。
 *
 * ── 为什么必须做这件事 ──
 * Base UI 的 `<Select.Value/>` 只会从 Root 的 `items`(或 `itemToStringLabel`)
 * 里查译名,查不到就退回 `stringifyAsLabel(value)`,也就是**枚举原始取值**。
 * 而 `<SelectContent>` 走 Portal 且关闭时不 keepMounted —— `<SelectItem>` 的
 * 标签从未注册过。结果是:下拉**展开时**是「转发前」「正则」,**收起时**变成
 * `prompt` `regex`,主键型的取值(违规类型)干脆显示成裸数字 `2`。
 * 也就是说,一张表单上同一个字段有两种写法,而先看到的那一种是英文/数字。
 *
 * ── 为什么修在这里,而不是逐个调用点补 items ──
 * 全站 65 个文件用到这个 Select,改动前基本没有一处传 items。逐处补是 65 次
 * 手工同步,而且每加一个新的下拉都要有人记得再补一次 —— 这种约定第一次就会漏。
 * 放在共享组件里,`items` 与 `<SelectItem>` 的文案**同源**,不存在漂移。
 *
 * ── 影响面为什么是安全的 ──
 * `items` 在 Base UI 的 select 里只被 `SelectValue` 一处消费
 * (store.js 里唯一的读点是 `selectors.items`,再就是 `hasNullItemLabel`,
 * 而后者只在"未选中 + 给了 placeholder"时才查,上面已经跳过了 null 取值)。
 * 显式传了 `items` 的调用方保持原样,推不出任何标签时回退 `undefined`,
 * 即完全维持改动前的行为。
 */
function Select<Value, Multiple extends boolean | undefined = false>(
  props: SelectPrimitive.Root.Props<Value, Multiple>
) {
  const { items, children } = props
  const derivedItems = React.useMemo(() => {
    if (items != null) return items
    const collected: Record<string, React.ReactNode> = {}
    collectSelectItemLabels(children, collected)
    return Object.keys(collected).length > 0 ? collected : undefined
  }, [items, children])

  return <SelectPrimitive.Root {...props} items={derivedItems} />
}

function SelectGroup({ className, ...props }: SelectPrimitive.Group.Props) {
  return (
    <SelectPrimitive.Group
      data-slot='select-group'
      className={cn('scroll-my-1 p-1', className)}
      {...props}
    />
  )
}

function SelectValue({ className, ...props }: SelectPrimitive.Value.Props) {
  return (
    <SelectPrimitive.Value
      data-slot='select-value'
      className={cn('flex flex-1 text-left', className)}
      {...props}
    />
  )
}

function SelectTrigger({
  className,
  size = 'default',
  children,
  ...props
}: SelectPrimitive.Trigger.Props & {
  size?: 'sm' | 'default'
}) {
  return (
    <SelectPrimitive.Trigger
      data-slot='select-trigger'
      data-size={size}
      className={cn(
        "border-input focus-visible:border-ring focus-visible:ring-ring/50 aria-invalid:border-destructive aria-invalid:ring-destructive/20 data-placeholder:text-muted-foreground dark:bg-input/30 dark:hover:bg-input/50 dark:aria-invalid:border-destructive/50 dark:aria-invalid:ring-destructive/40 flex w-fit items-center justify-between gap-1.5 rounded-lg border bg-transparent py-2 pr-2 pl-2.5 text-sm whitespace-nowrap transition-colors outline-none select-none focus-visible:ring-3 disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:ring-3 data-[size=default]:h-8 data-[size=sm]:h-7 data-[size=sm]:rounded-md *:data-[slot=select-value]:line-clamp-1 *:data-[slot=select-value]:flex *:data-[slot=select-value]:items-center *:data-[slot=select-value]:gap-1.5 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4",
        className
      )}
      {...props}
    >
      {children}
      <SelectPrimitive.Icon
        render={
          <HugeiconsIcon
            icon={UnfoldMoreIcon}
            strokeWidth={2}
            className='text-muted-foreground pointer-events-none size-4'
          />
        }
      />
    </SelectPrimitive.Trigger>
  )
}

function SelectContent({
  className,
  children,
  side = 'bottom',
  sideOffset = 4,
  align = 'center',
  alignOffset = 0,
  alignItemWithTrigger = true,
  ...props
}: SelectPrimitive.Popup.Props &
  Pick<
    SelectPrimitive.Positioner.Props,
    'align' | 'alignOffset' | 'side' | 'sideOffset' | 'alignItemWithTrigger'
  >) {
  const isMobile = useMediaQuery('(max-width: 640px)')

  const content = (
    <SelectPrimitive.Positioner
      side={side}
      sideOffset={sideOffset}
      align={align}
      alignOffset={alignOffset}
      alignItemWithTrigger={alignItemWithTrigger}
      className='isolate z-50'
    >
      <SelectPrimitive.Popup
        data-slot='select-content'
        data-align-trigger={alignItemWithTrigger}
        className={cn(
          'bg-popover text-popover-foreground ring-foreground/10 data-[side=bottom]:slide-in-from-top-2 data-[side=inline-end]:slide-in-from-left-2 data-[side=inline-start]:slide-in-from-right-2 data-[side=left]:slide-in-from-right-2 data-[side=right]:slide-in-from-left-2 data-[side=top]:slide-in-from-bottom-2 data-open:animate-in data-open:fade-in-0 data-open:zoom-in-95 data-closed:animate-out data-closed:fade-out-0 data-closed:zoom-out-95 relative isolate z-50 max-h-(--available-height) w-(--anchor-width) min-w-36 origin-(--transform-origin) overflow-x-hidden overflow-y-auto rounded-lg shadow-md ring-1 duration-100 data-[align-trigger=true]:animate-none',
          className
        )}
        {...props}
      >
        <SelectScrollUpButton />
        <SelectPrimitive.List>{children}</SelectPrimitive.List>
        <SelectScrollDownButton />
      </SelectPrimitive.Popup>
    </SelectPrimitive.Positioner>
  )

  if (isMobile) {
    return content
  }

  return <SelectPrimitive.Portal>{content}</SelectPrimitive.Portal>
}

function SelectLabel({
  className,
  ...props
}: SelectPrimitive.GroupLabel.Props) {
  return (
    <SelectPrimitive.GroupLabel
      data-slot='select-label'
      className={cn('text-muted-foreground px-1.5 py-1 text-xs', className)}
      {...props}
    />
  )
}

function SelectItem({
  className,
  children,
  ...props
}: SelectPrimitive.Item.Props) {
  return (
    <SelectPrimitive.Item
      data-slot='select-item'
      className={cn(
        "focus:bg-accent focus:text-accent-foreground not-data-[variant=destructive]:focus:**:text-accent-foreground relative flex w-full cursor-default items-center gap-1.5 rounded-md py-1 pr-8 pl-1.5 text-sm outline-hidden select-none data-disabled:pointer-events-none data-disabled:opacity-50 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4 *:[span]:last:flex *:[span]:last:items-center *:[span]:last:gap-2",
        className
      )}
      {...props}
    >
      <SelectPrimitive.ItemText
        data-slot='select-item-text'
        className='flex flex-1 shrink-0 gap-2 whitespace-nowrap'
      >
        {children}
      </SelectPrimitive.ItemText>
      <SelectPrimitive.ItemIndicator
        render={
          <span className='pointer-events-none absolute right-2 flex size-4 items-center justify-center' />
        }
      >
        <HugeiconsIcon
          icon={Tick02Icon}
          strokeWidth={2}
          className='pointer-events-none'
        />
      </SelectPrimitive.ItemIndicator>
    </SelectPrimitive.Item>
  )
}

function SelectSeparator({
  className,
  ...props
}: SelectPrimitive.Separator.Props) {
  return (
    <SelectPrimitive.Separator
      data-slot='select-separator'
      className={cn('bg-border pointer-events-none -mx-1 my-1 h-px', className)}
      {...props}
    />
  )
}

function SelectScrollUpButton({
  className,
  ...props
}: React.ComponentProps<typeof SelectPrimitive.ScrollUpArrow>) {
  return (
    <SelectPrimitive.ScrollUpArrow
      data-slot='select-scroll-up-button'
      className={cn(
        "bg-popover top-0 z-10 flex w-full cursor-default items-center justify-center py-1 [&_svg:not([class*='size-'])]:size-4",
        className
      )}
      {...props}
    >
      <HugeiconsIcon icon={ArrowUp01Icon} strokeWidth={2} />
    </SelectPrimitive.ScrollUpArrow>
  )
}

function SelectScrollDownButton({
  className,
  ...props
}: React.ComponentProps<typeof SelectPrimitive.ScrollDownArrow>) {
  return (
    <SelectPrimitive.ScrollDownArrow
      data-slot='select-scroll-down-button'
      className={cn(
        "bg-popover bottom-0 z-10 flex w-full cursor-default items-center justify-center py-1 [&_svg:not([class*='size-'])]:size-4",
        className
      )}
      {...props}
    >
      <HugeiconsIcon icon={ArrowDown01Icon} strokeWidth={2} />
    </SelectPrimitive.ScrollDownArrow>
  )
}

export {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectScrollDownButton,
  SelectScrollUpButton,
  SelectSeparator,
  SelectTrigger,
  SelectValue,
}
