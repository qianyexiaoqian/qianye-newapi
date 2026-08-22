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
import { Check, ChevronsUpDown } from 'lucide-react'
import { useMemo, useState, type ReactElement, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from '@/components/ui/command'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import { useMediaQuery } from '@/hooks'
import { cn } from '@/lib/utils'

import {
  AUTO_GROUP_FRAME_CLASS_NAME,
  AutoGroupFlowBorder,
  GroupRatioBadge,
} from './auto-group-visuals'

export type ApiKeyGroupOption = {
  value: string
  label: string
  desc?: string
  ratio?: number | string
}

type ApiKeyGroupComboboxProps = {
  options: ApiKeyGroupOption[]
  value?: string
  onValueChange: (value: string) => void
  placeholder?: string
  disabled?: boolean
  /**
   * 自定义触发器。给的话就整个替换掉抽屉里那个高 56/80px 的大按钮。
   *
   * 存在的理由是列表里的**分组行内快切**：它要塞进一格 220px 宽的表格单元，
   * 而抽屉那个触发器是为表单排版设计的。候选项列表、搜索、倍率徽标、auto 的
   * 流光边框在两处必须**逐字相同** —— 两份实现意味着两份候选项过滤规则，
   * 而"抽屉里搜得到、行内搜不到"是没人会写测试的那种差异。
   *
   * 必须是 ReactElement 而不是 ReactNode：`PopoverTrigger` 的 `render` 会把
   * 触发器所需的 aria/事件属性克隆进这个元素，字符串或数组接不住。
   */
  trigger?: ReactElement
  /**
   * 自定义触发器**内部**的内容。只在给了 `trigger` 时使用。
   *
   * 与 `trigger` 分成两个入口，是 Base UI 的 `render` 语义决定的：`render` 收的是
   * 元素外壳（属性会被克隆进去），`children` 才是渲染在里面的东西。合成一个的话，
   * 触发器所需的 aria/事件属性会落到内容的最外层节点上。
   */
  children?: ReactNode
  /** 弹出层的额外类名。行内快切要给它一个下限宽度，否则会跟着窄单元格缩成一条。 */
  popoverClassName?: string
}

export function ApiKeyGroupCombobox({
  options,
  value,
  onValueChange,
  placeholder,
  disabled,
  trigger,
  children,
  popoverClassName,
}: ApiKeyGroupComboboxProps) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const [searchValue, setSearchValue] = useState('')
  const shouldReduceMotion = useMediaQuery('(prefers-reduced-motion: reduce)')
  const selectedOption = options.find((option) => option.value === value)
  const isAutoSelected = selectedOption?.value === 'auto'

  const filteredOptions = useMemo(() => {
    const search = searchValue.trim().toLowerCase()
    if (!search) return options

    return options.filter((option) => {
      const ratioText = String(option.ratio ?? '').toLowerCase()
      return (
        option.value.toLowerCase().includes(search) ||
        option.label.toLowerCase().includes(search) ||
        option.desc?.toLowerCase().includes(search) ||
        ratioText.includes(search)
      )
    })
  }, [options, searchValue])

  const handleSelect = (selectedValue: string) => {
    onValueChange(selectedValue)
    setOpen(false)
    setSearchValue('')
  }

  /*
    候选项列表本身。抽屉与列表行内快切共用**同一段** JSX：搜索过滤规则、倍率
    徽标、auto 的流光边框只要有一处分家，就会出现"抽屉里搜得到、行内搜不到"
    这类没人会写测试的差异。
  */
  const groupList = (
    <PopoverContent
      className={cn(
        'data-closed:zoom-out-100 data-open:zoom-in-100 data-[side=bottom]:slide-in-from-top-0 data-[side=left]:slide-in-from-right-0 data-[side=right]:slide-in-from-left-0 data-[side=top]:slide-in-from-bottom-0 w-[var(--anchor-width)] overflow-hidden rounded-xl p-0 shadow-lg data-closed:duration-75 data-open:duration-100',
        popoverClassName
      )}
      onWheel={(event) => event.stopPropagation()}
      onTouchMove={(event) => event.stopPropagation()}
      onPointerDown={(event) => event.stopPropagation()}
    >
      <Command shouldFilter={false}>
        <CommandInput
          placeholder={t('Search...')}
          value={searchValue}
          onValueChange={setSearchValue}
        />
        <CommandList className='max-h-[360px]'>
          <CommandEmpty>{t('No group found.')}</CommandEmpty>
          <CommandGroup>
            {filteredOptions.map((option) => {
              const isAutoOption = option.value === 'auto'

              return (
                <CommandItem
                  key={option.value}
                  value={option.value}
                  data-auto-group-effect={isAutoOption ? 'option' : undefined}
                  onSelect={() => handleSelect(option.value)}
                  className={cn(
                    'data-[selected=true]:bg-muted items-start gap-3 rounded-lg px-3 py-3 transition-colors',
                    isAutoOption &&
                      cn(
                        AUTO_GROUP_FRAME_CLASS_NAME,
                        'border-primary/35 data-[selected=true]:border-primary/55'
                      )
                  )}
                >
                  {isAutoOption && (
                    <AutoGroupFlowBorder
                      shouldReduceMotion={shouldReduceMotion}
                    />
                  )}
                  <Check
                    aria-hidden='true'
                    className={cn(
                      'mt-0.5 size-4',
                      value === option.value ? 'opacity-100' : 'opacity-0'
                    )}
                  />
                  <span className='min-w-0 flex-1'>
                    <span className='block truncate font-medium'>
                      {option.label}
                    </span>
                    {option.desc && (
                      <span className='text-muted-foreground block truncate text-xs'>
                        {option.desc}
                      </span>
                    )}
                  </span>
                  <GroupRatioBadge
                    ratio={option.ratio}
                    isAuto={isAutoOption}
                    shouldReduceMotion={shouldReduceMotion}
                  />
                </CommandItem>
              )
            })}
          </CommandGroup>
        </CommandList>
      </Command>
    </PopoverContent>
  )

  if (trigger) {
    return (
      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger render={trigger}>{children}</PopoverTrigger>
        {groupList}
      </Popover>
    )
  }

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        render={
          <Button
            type='button'
            variant='outline'
            role='combobox'
            aria-expanded={open}
            data-auto-group-effect={isAutoSelected ? 'trigger' : undefined}
            disabled={disabled}
            className={cn(
              'border-input bg-muted/40 hover:bg-muted/55 hover:text-foreground active:bg-background data-popup-open:border-ring data-popup-open:bg-background data-popup-open:ring-ring/20 relative h-auto min-h-14 w-full justify-between gap-2 rounded-lg px-3 py-2 text-start shadow-none transition-[background-color,border-color,box-shadow] duration-150 data-popup-open:ring-[3px] sm:min-h-20 sm:gap-3 sm:px-4 sm:py-3',
              isAutoSelected &&
                cn(
                  AUTO_GROUP_FRAME_CLASS_NAME,
                  'hover:border-primary/55 data-popup-open:border-primary/55 data-popup-open:ring-primary/20'
                )
            )}
          />
        }
      >
        {isAutoSelected && (
          <AutoGroupFlowBorder shouldReduceMotion={shouldReduceMotion} />
        )}
        <span className='flex min-w-0 flex-1 items-center justify-between gap-2 sm:gap-3'>
          <span className='min-w-0'>
            <span className='block truncate font-medium'>
              {selectedOption?.label || placeholder || t('Select a group')}
            </span>
            {selectedOption?.desc && (
              <span className='text-muted-foreground block truncate text-[11px] sm:text-xs'>
                {selectedOption.desc}
              </span>
            )}
          </span>
          <span className='hidden sm:block'>
            <GroupRatioBadge
              ratio={selectedOption?.ratio}
              isAuto={isAutoSelected}
              shouldReduceMotion={shouldReduceMotion}
            />
          </span>
        </span>
        <ChevronsUpDown
          aria-hidden='true'
          className='size-4 shrink-0 opacity-50'
        />
      </PopoverTrigger>
      {groupList}
    </Popover>
  )
}
