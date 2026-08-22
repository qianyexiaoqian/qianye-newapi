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
import { useQuery } from '@tanstack/react-query'
import React, { useState, useCallback, useMemo, useRef, useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import useDialogState from '@/hooks/use-dialog'
import { getUserGroups } from '@/lib/api'

import { fetchTokenKey, fetchTokenKeysBatch } from '../api'
import { ERROR_MESSAGES } from '../constants'
import {
  buildApiKeyGroupOptions,
  type ApiKeyGroupOptionData,
} from '../lib/group-options'
import { tokenTodayUsageQuery, type TokenTodayUsage } from '../lib/today-usage'
import { type ApiKey, type ApiKeysDialogType } from '../types'

type ApiKeysContextType = {
  open: ApiKeysDialogType | null
  setOpen: (str: ApiKeysDialogType | null) => void
  currentRow: ApiKey | null
  setCurrentRow: React.Dispatch<React.SetStateAction<ApiKey | null>>
  refreshTrigger: number
  triggerRefresh: () => void
  resolvedKey: string
  setResolvedKey: React.Dispatch<React.SetStateAction<string>>
  /**
   * 打开 CC Switch 配置窗口之前，用户在线路选择窗口里选中的 API 地址。
   *
   * 与 `resolvedKey` 同生共死：两者都由行内的「CC Switch」菜单项在真正 `setOpen`
   * 之前一起写好，再由 `ApiKeysDialogs` 里那个全局唯一的配置窗口读走。
   */
  ccSwitchAddress: string
  setCcSwitchAddress: React.Dispatch<React.SetStateAction<string>>
  resolveRealKey: (id: number) => Promise<string | null>
  resolveRealKeysBatch: (ids: number[]) => Promise<Record<number, string>>
  resolvedKeys: Record<number, string>
  loadingKeys: Record<number, boolean>
  copiedKeyId: number | null
  markKeyCopied: (id: number) => void
  /**
   * 分组下拉的候选项。
   *
   * **与编辑抽屉同源**：两处都是 `buildApiKeyGroupOptions` 作用在
   * `['user-groups']`（`GET /api/user/self/groups`）这一份数据上。同源不是巧合，
   * 是必须的 —— 本仓的分组可选性同时受用户分组、分组矩阵、套餐解锁三处影响，
   * 前端各算一遍必然漂移，而漂移的表现是「抽屉里选得到的分组、行内选不到」
   * （或者更糟：行内选得到、一提交被写入侧拒绝）。
   *
   * 放在 provider 而不是各单元格自取，是因为列表里的单元格会被 flexRender
   * 反复卸载重挂（见 api-keys-columns.tsx 里那段长注释）。在单元格里挂
   * `useQuery` 会让这个 staleTime=0 的查询每次重挂都重新发一次请求。
   */
  groupOptions: ApiKeyGroupOptionData[]
  groupOptionsLoading: boolean
  /**
   * 每一把密钥今天的消费额。整张表**一次**聚合，见 `lib/today-usage.ts`。
   *
   *   `undefined` —— 还在取 / 取失败：单元格显示未知，不能显示 0
   *   `null`      —— 扩展未启用：整列不渲染
   */
  todayUsage: TokenTodayUsage | null | undefined
  todayUsageLoading: boolean
  todayUsageFailed: boolean
}

const ApiKeysContext = React.createContext<ApiKeysContextType | null>(null)

export function ApiKeysProvider({ children }: { children: React.ReactNode }) {
  const { t } = useTranslation()
  const [open, setOpen] = useDialogState<ApiKeysDialogType>(null)
  const [currentRow, setCurrentRow] = useState<ApiKey | null>(null)
  const [refreshTrigger, setRefreshTrigger] = useState(0)
  const [resolvedKey, setResolvedKey] = useState('')
  const [ccSwitchAddress, setCcSwitchAddress] = useState('')

  const [resolvedKeys, setResolvedKeys] = useState<Record<number, string>>({})
  const [loadingKeys, setLoadingKeys] = useState<Record<number, boolean>>({})
  const pendingRequests = useRef<Record<number, Promise<string | null>>>({})

  const [copiedKeyId, setCopiedKeyId] = useState<number | null>(null)
  const copiedTimerRef = useRef<ReturnType<typeof setTimeout>>(undefined)

  useEffect(() => {
    return () => clearTimeout(copiedTimerRef.current)
  }, [])

  const markKeyCopied = useCallback((id: number) => {
    setCopiedKeyId(id)
    clearTimeout(copiedTimerRef.current)
    copiedTimerRef.current = setTimeout(() => setCopiedKeyId(null), 2000)
  }, [])

  const triggerRefresh = useCallback(() => {
    setRefreshTrigger((prev) => prev + 1)
  }, [])

  // 分组候选项：与编辑抽屉共用 `['user-groups']` 这一把 key，因此两处拿到的
  // 永远是同一份数据（react-query 的同键去重），不存在"各查一次、各算一遍"。
  const { data: groupsData, isPending: groupsPending } = useQuery({
    queryKey: ['user-groups'],
    queryFn: getUserGroups,
    staleTime: 0,
  })
  const groupOptions = useMemo(
    () => buildApiKeyGroupOptions(groupsData?.data),
    [groupsData]
  )

  const todayUsageResult = useQuery(tokenTodayUsageQuery())

  const resolveRealKey = useCallback(
    async (id: number): Promise<string | null> => {
      if (resolvedKeys[id]) return resolvedKeys[id]
      if (id in pendingRequests.current) return pendingRequests.current[id]

      const request = (async () => {
        setLoadingKeys((prev) => ({ ...prev, [id]: true }))
        try {
          const res = await fetchTokenKey(id)
          if (res.success && res.data?.key) {
            const fullKey = `sk-${res.data.key}`
            setResolvedKeys((prev) => ({ ...prev, [id]: fullKey }))
            return fullKey
          }
          toast.error(res.message || t(ERROR_MESSAGES.UNEXPECTED))
          return null
        } catch {
          toast.error(t(ERROR_MESSAGES.UNEXPECTED))
          return null
        } finally {
          delete pendingRequests.current[id]
          setLoadingKeys((prev) => {
            const next = { ...prev }
            delete next[id]
            return next
          })
        }
      })()

      pendingRequests.current[id] = request
      return request
    },
    [resolvedKeys, t]
  )

  const resolveRealKeysBatch = useCallback(
    async (ids: number[]): Promise<Record<number, string>> => {
      const uncachedIds = ids.filter((id) => !resolvedKeys[id])
      if (uncachedIds.length === 0) {
        const result: Record<number, string> = {}
        for (const id of ids) result[id] = resolvedKeys[id]
        return result
      }

      for (const id of uncachedIds) {
        setLoadingKeys((prev) => ({ ...prev, [id]: true }))
      }

      try {
        const res = await fetchTokenKeysBatch(uncachedIds)
        if (res.success && res.data?.keys) {
          const newKeys: Record<number, string> = {}
          for (const [idStr, key] of Object.entries(res.data.keys)) {
            newKeys[Number(idStr)] = `sk-${key}`
          }
          setResolvedKeys((prev) => ({ ...prev, ...newKeys }))

          const result: Record<number, string> = { ...newKeys }
          for (const id of ids) {
            if (resolvedKeys[id]) result[id] = resolvedKeys[id]
          }
          return result
        }
        toast.error(res.message || t(ERROR_MESSAGES.UNEXPECTED))
        return {}
      } catch {
        toast.error(t(ERROR_MESSAGES.UNEXPECTED))
        return {}
      } finally {
        for (const id of uncachedIds) {
          setLoadingKeys((prev) => {
            const next = { ...prev }
            delete next[id]
            return next
          })
        }
      }
    },
    [resolvedKeys, t]
  )

  return (
    <ApiKeysContext
      value={{
        open,
        setOpen,
        currentRow,
        setCurrentRow,
        refreshTrigger,
        triggerRefresh,
        resolvedKey,
        setResolvedKey,
        ccSwitchAddress,
        setCcSwitchAddress,
        resolveRealKey,
        resolveRealKeysBatch,
        resolvedKeys,
        loadingKeys,
        copiedKeyId,
        markKeyCopied,
        groupOptions,
        groupOptionsLoading: groupsPending,
        todayUsage: todayUsageResult.data,
        todayUsageLoading: todayUsageResult.isPending,
        todayUsageFailed: todayUsageResult.isError,
      }}
    >
      {children}
    </ApiKeysContext>
  )
}

// eslint-disable-next-line react-refresh/only-export-components
export const useApiKeys = () => {
  const apiKeysContext = React.useContext(ApiKeysContext)

  if (!apiKeysContext) {
    throw new Error('useApiKeys has to be used within <ApiKeysContext>')
  }

  return apiKeysContext
}
