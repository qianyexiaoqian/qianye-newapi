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
import type { Row } from '@tanstack/react-table'
import {
  Trash2,
  Edit,
  Power,
  PowerOff,
  ExternalLink,
  ArrowRightLeft,
  Copy,
  Link,
  Loader2,
} from 'lucide-react'
import { useCallback, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { DataTableRowActionMenu } from '@/components/data-table/core/row-action-menu'
import { Button } from '@/components/ui/button'
import {
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuShortcut,
} from '@/components/ui/dropdown-menu'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { useChatPresets } from '@/features/chat/hooks/use-chat-presets'
import { resolveChatUrl, type ChatPreset } from '@/features/chat/lib/chat-links'
import { sendToFluent } from '@/features/chat/lib/send-to-fluent'
import {
  useQyApiAddressPicker,
  useQyConnectionInfoCopy,
} from '@/features/qy/pages/api-address-picker'
import { copyToClipboard } from '@/lib/copy-to-clipboard'

import { updateApiKeyStatus } from '../api'
import { API_KEY_STATUS, ERROR_MESSAGES, SUCCESS_MESSAGES } from '../constants'
import { apiKeySchema } from '../types'
import { useApiKeys } from './api-keys-provider'

type DataTableRowActionsProps<TData> = {
  row: Row<TData>
}

export function DataTableRowActions<TData>({
  row,
}: DataTableRowActionsProps<TData>) {
  const { t } = useTranslation()
  const apiKey = apiKeySchema.parse(row.original)
  const {
    setOpen,
    setCurrentRow,
    triggerRefresh,
    setResolvedKey,
    setCcSwitchAddress,
    resolveRealKey,
    resolvedKeys,
    loadingKeys,
  } = useApiKeys()
  const isEnabled = apiKey.status === API_KEY_STATUS.ENABLED
  const { chatPresets, serverAddress } = useChatPresets()
  const [isTogglingStatus, setIsTogglingStatus] = useState(false)
  const resolvedRealKey = resolvedKeys[apiKey.id]
  const isRealKeyLoading = Boolean(loadingKeys[apiKey.id])

  const hasChatPresets = chatPresets.length > 0
  const toggleLabel = isEnabled ? t('Disable') : t('Enable')

  // 「复制链接信息」在复制之前先让用户选一个 API 地址（站点可能有主域 / 备用域 /
  // 加速线路多个入口）。只有一个可选地址时不弹窗、直接复制；一个都没配时回落到
  // 站点地址 —— 也就是这个功能上线之前的行为。详见 useQyConnectionInfoCopy 的说明。
  //
  // 解构而不是整个对象往下传：hook 每次渲染都返回一个新的对象字面量，
  // 把它整个塞进 useCallback 的依赖表等于让那个 useCallback 永不命中。
  const {
    prefetch: prefetchApiAddresses,
    copy: copyConnectionInfo,
    dialog: apiAddressPickerDialog,
  } = useQyConnectionInfoCopy()

  // 「CC Switch」走同一条前置：先选线路，选中的那条再作为配置里的接口地址。
  // 项目方原话：「用户在 API 密钥页面，填入 CC Switch，这个选项也要先弹出选择
  // API 线路的窗口选择线路后再弹出 CC Switch 的配置窗口。」
  //
  // 不取它的 `prefetch`：两个 picker 读的是同一个 react-query 键，上面那次预热
  // 已经把清单暖好了，再调一次只是同一个 promise 的去重命中。
  const { pick: pickCcSwitchAddress, dialog: ccSwitchAddressDialog } =
    useQyApiAddressPicker({
      description: t('qy_aa_pick_desc_cc_switch'),
      confirmLabel: t('qy_aa_pick_confirm_cc_switch'),
      onPick: (realKey, url) => {
        setResolvedKey(realKey)
        setCcSwitchAddress(url)
        setCurrentRow(apiKey)
        setOpen('cc-switch')
      },
    })

  const handleMenuOpenChange = useCallback(
    (open: boolean) => {
      if (!open) return
      if (!resolvedRealKey && !isRealKeyLoading) {
        void resolveRealKey(apiKey.id)
      }
      // 与解析密钥同一时机预热地址清单：点击时才发请求的话，等它回来手势已经
      // 过期，剪贴板写入会静默失败。
      prefetchApiAddresses()
    },
    [
      apiKey.id,
      isRealKeyLoading,
      prefetchApiAddresses,
      resolvedRealKey,
      resolveRealKey,
    ]
  )

  const getCachedRealKey = useCallback(() => {
    if (resolvedRealKey) return resolvedRealKey
    void resolveRealKey(apiKey.id)
    toast.info(t('API key is loading, please try again in a moment'))
    return null
  }, [apiKey.id, resolvedRealKey, resolveRealKey, t])

  const handleOpenChatPreset = useCallback(
    async (preset: ChatPreset) => {
      const realKey = await resolveRealKey(apiKey.id)
      if (!realKey) return

      if (preset.type === 'fluent') {
        const success = sendToFluent(realKey, serverAddress)
        if (success) {
          toast.success(t('Sent the API key to FluentRead.'))
        } else {
          toast.info(
            t(
              'FluentRead extension not detected. Please ensure it is installed and active.'
            )
          )
        }
        return
      }

      const resolvedUrl = resolveChatUrl({
        template: preset.url,
        apiKey: realKey,
        serverAddress,
      })

      if (!resolvedUrl) {
        toast.error(t('Invalid chat link. Please contact your administrator.'))
        return
      }

      if (typeof window === 'undefined') return

      try {
        window.open(resolvedUrl, '_blank', 'noopener')
      } catch {
        window.location.href = resolvedUrl
      }
    },
    [resolveRealKey, apiKey.id, serverAddress, t]
  )

  const handleToggleStatus = async (
    e?: React.MouseEvent<HTMLButtonElement>
  ) => {
    e?.stopPropagation()
    const newStatus = isEnabled
      ? API_KEY_STATUS.DISABLED
      : API_KEY_STATUS.ENABLED

    setIsTogglingStatus(true)
    try {
      const result = await updateApiKeyStatus(apiKey.id, newStatus)
      if (result.success) {
        const message = isEnabled
          ? t(SUCCESS_MESSAGES.API_KEY_DISABLED)
          : t(SUCCESS_MESSAGES.API_KEY_ENABLED)
        toast.success(message)
        triggerRefresh()
      } else {
        toast.error(result.message || t(ERROR_MESSAGES.STATUS_UPDATE_FAILED))
      }
    } catch {
      toast.error(t(ERROR_MESSAGES.UNEXPECTED))
    } finally {
      setIsTogglingStatus(false)
    }
  }

  let statusIcon = <Power className='size-4' />
  if (isTogglingStatus) {
    statusIcon = <Loader2 className='size-4 animate-spin' />
  } else if (isEnabled) {
    statusIcon = <PowerOff className='size-4' />
  }

  return (
    <div className='-ml-1.5 flex items-center gap-1'>
      <Tooltip>
        <TooltipTrigger
          render={
            <Button
              variant='ghost'
              size='icon-sm'
              onClick={handleToggleStatus}
              disabled={isTogglingStatus}
              aria-label={toggleLabel}
              className={
                isEnabled
                  ? 'text-destructive hover:text-destructive'
                  : 'text-emerald-600 hover:text-emerald-600 dark:text-emerald-400 dark:hover:text-emerald-400'
              }
            />
          }
        >
          {statusIcon}
        </TooltipTrigger>
        <TooltipContent>{toggleLabel}</TooltipContent>
      </Tooltip>

      <Tooltip>
        <TooltipTrigger
          render={
            <Button
              variant='ghost'
              size='icon-sm'
              onClick={() => {
                setCurrentRow(apiKey)
                setOpen('update')
              }}
              aria-label={t('Edit')}
            />
          }
        >
          <Edit />
        </TooltipTrigger>
        <TooltipContent>{t('Edit')}</TooltipContent>
      </Tooltip>

      <DataTableRowActionMenu
        ariaLabel={t('Open menu')}
        contentClassName='w-[200px]'
        modal={false}
        onOpenChange={handleMenuOpenChange}
      >
        <DropdownMenuItem
          onClick={async () => {
            const realKey = getCachedRealKey()
            if (!realKey) return
            const ok = await copyToClipboard(realKey)
            if (ok) toast.success(t('Copied'))
          }}
        >
          {t('Copy Key')}
          <DropdownMenuShortcut>
            <Copy size={16} />
          </DropdownMenuShortcut>
        </DropdownMenuItem>
        <DropdownMenuItem
          onClick={() => {
            const realKey = getCachedRealKey()
            if (!realKey) return
            copyConnectionInfo(realKey)
          }}
        >
          {t('Copy Connection Info')}
          <DropdownMenuShortcut>
            <Link size={16} />
          </DropdownMenuShortcut>
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuItem
          onClick={async () => {
            const realKey = await resolveRealKey(apiKey.id)
            if (!realKey) return
            pickCcSwitchAddress(realKey)
          }}
        >
          {t('CC Switch')}
          <DropdownMenuShortcut>
            <ArrowRightLeft size={16} />
          </DropdownMenuShortcut>
        </DropdownMenuItem>
        {hasChatPresets && (
          <DropdownMenuSub>
            <DropdownMenuSubTrigger>{t('Chat')}</DropdownMenuSubTrigger>
            <DropdownMenuSubContent>
              {chatPresets.map((preset) => (
                <DropdownMenuItem
                  key={preset.id}
                  onClick={() => handleOpenChatPreset(preset)}
                >
                  {preset.name}
                  {preset.type !== 'web' && (
                    <DropdownMenuShortcut>
                      <ExternalLink size={16} />
                    </DropdownMenuShortcut>
                  )}
                </DropdownMenuItem>
              ))}
            </DropdownMenuSubContent>
          </DropdownMenuSub>
        )}
        <DropdownMenuSeparator />
        <DropdownMenuItem
          onClick={() => {
            setCurrentRow(apiKey)
            setOpen('delete')
          }}
          className='text-destructive focus:text-destructive'
        >
          {t('Delete')}
          <DropdownMenuShortcut>
            <Trash2 size={16} />
          </DropdownMenuShortcut>
        </DropdownMenuItem>
      </DataTableRowActionMenu>

      {/* 两个入口各自的地址选择窗口（复制链接信息 / CC Switch）。未打开时不产生
          任何 DOM，因此每行各挂两个的开销可以忽略；放在这里而不是 ApiKeysDialogs
          是因为它们要拿的是**本行**已解析出的密钥，走 provider 传递等于给一个
          纯展示的选择步骤加一条全局状态。 */}
      {apiAddressPickerDialog}
      {ccSwitchAddressDialog}
    </div>
  )
}
