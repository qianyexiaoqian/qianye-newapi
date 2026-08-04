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
import { useQueryClient } from '@tanstack/react-query'
import { useCallback, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { encodeChannelConnectionInfo } from '@/lib/channel-connection-info'
import { copyToClipboard } from '@/lib/copy-to-clipboard'

import { qyKeys } from '../../lib/query-keys'
import { qyApiAddressesQuery, type QyApiAddressOption } from './api'
import { QyApiAddressPickerDialog } from './picker-dialog'
import { qyResolveAddressOptions } from './resolve-options'

export type QyConnectionInfoCopy = {
  /** 菜单打开时调用：把清单预热进缓存，让绝大多数点击走同步分支。 */
  prefetch: () => void
  /** 用户点了「复制链接信息」。`realKey` 是已解析出的完整 `sk-` 密钥。 */
  copy: (realKey: string) => void
  /** 选择窗口。调用方渲染它即可；未打开时不产生任何 DOM。 */
  dialog: ReactNode
}

/**
 * 给上游密钥列表用的「先选地址、再复制连接信息」。
 *
 * ── 需求 ──
 * 项目方原话：「这里弹出前先弹出一个窗口，让用户选择 url，API 地址。」
 * 站点常常有多个可用入口（主域 / 备用域 / 加速线路），改造前用户拿到的永远是
 * 系统设置里的那一个，想换只能自己手改剪贴板里那段 JSON。
 *
 * ── 什么时候**不**弹 ──
 * 只有 0 或 1 个可选地址时直接复制。一个只有一个选项的选择窗口不提供任何信息，
 * 只是在菜单项「复制链接信息」和它真正做的那件事之间插一道空转的确认 ——
 * 而空转的确认恰恰会训练用户闭着眼睛点下一步，等到真有多个地址时那一屏也会被
 * 同样地略过。**有得选才弹**，是这个交互唯一说得通的口径。
 * 一条都没配时那唯一的选项就是站点自身（见 {@link qyResolveAddressOptions}）。
 *
 * ── 为什么点击时读缓存，而不是直接 await 一次请求 ──
 * 写剪贴板需要用户手势。`await` 一次网络请求之后再写，在部分浏览器上手势已经
 * 过期，复制会**静默失败** —— 表现是"点了没反应"，用户只会再点一次。
 * 所以菜单一打开就预热（{@link QyConnectionInfoCopy.prefetch}），点击时同步读缓存：
 * 命中且只有一个选项就地复制，手势完整；没命中就打开窗口，而窗口里的「复制」
 * 按钮本身又是一次新的用户手势。两条路径都不会静默失败。
 */
export function useQyConnectionInfoCopy(): QyConnectionInfoCopy {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [pendingKey, setPendingKey] = useState<string | null>(null)

  const write = useCallback(
    async (realKey: string, url: string) => {
      const ok = await copyToClipboard(
        encodeChannelConnectionInfo(realKey, url)
      )
      if (ok) toast.success(t('qy_aa_copied', { url }))
      else toast.error(t('qy_aa_copy_failed'))
    },
    [t]
  )

  const prefetch = useCallback(() => {
    void queryClient.prefetchQuery(qyApiAddressesQuery())
  }, [queryClient])

  const copy = useCallback(
    (realKey: string) => {
      const cached = queryClient.getQueryData<QyApiAddressOption[]>(
        qyKeys.apiAddresses()
      )
      // 缓存还没到（预热在途，或用户用键盘直接唤起了菜单项）：交给窗口，
      // 它自己会显示加载态。绝不在这里 await —— 见上面对用户手势的说明。
      if (cached == null) {
        setPendingKey(realKey)
        return
      }
      const options = qyResolveAddressOptions(cached, t('qy_aa_site_default'))
      if (options.length === 1) {
        void write(realKey, options[0].url)
        return
      }
      setPendingKey(realKey)
    },
    [queryClient, t, write]
  )

  return {
    prefetch,
    copy,
    dialog: (
      <QyApiAddressPickerDialog
        pendingKey={pendingKey}
        onClose={() => setPendingKey(null)}
        onPick={(url) => {
          const realKey = pendingKey
          setPendingKey(null)
          if (realKey != null) void write(realKey, url)
        }}
      />
    ),
  }
}
