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

export type QyApiAddressPicker = {
  /** 菜单打开时调用：把清单预热进缓存，让绝大多数点击走同步分支。 */
  prefetch: () => void
  /**
   * 用户点了那个入口。`realKey` 是已解析出的完整 `sk-` 密钥，选完地址之后
   * 原样回传给 {@link QyApiAddressPickerOptions.onPick}。
   */
  pick: (realKey: string) => void
  /** 选择窗口。调用方渲染它即可；未打开时不产生任何 DOM。 */
  dialog: ReactNode
}

export type QyApiAddressPickerOptions = {
  /** 窗口里的说明与确认按钮文案 —— 每个入口选完之后要做的事不一样。 */
  description: string
  confirmLabel: string
  /** 地址定下来之后真正要做的事。`realKey` 就是 `pick()` 收到的那一个。 */
  onPick: (realKey: string, url: string) => void
}

/**
 * 「先让用户选一条 API 线路，再拿着这条线路去做事」。
 *
 * ── 需求 ──
 * 项目方原话：「这里弹出前先弹出一个窗口，让用户选择 url，API 地址。」
 * 后来又追加：「用户在 API 密钥页面，填入 CC Switch，这个选项也要先弹出选择
 * API 线路的窗口选择线路后再弹出 CC Switch 的配置窗口。」
 * 站点常常有多个可用入口（主域 / 备用域 / 加速线路），改造前用户拿到的永远是
 * 系统设置里的那一个，想换只能自己手改剪贴板里那段 JSON、或手改客户端里的
 * 接口地址。
 *
 * ── 为什么是一个 hook，而不是每个入口各写一遍 ──
 * 「弹不弹」的判定、「窗口里列什么」、「一条都没配怎么办」三件事必须同口径。
 * 各写各的，第一份改了第二份不会红：表现是同一个站点里「复制链接信息」直接
 * 复制、而 CC Switch 弹出一个只有一项的窗口（或者反过来）。所以入口之间只允许
 * 差两句文案和一个 `onPick`，其余全在这里。
 *
 * ── 什么时候**不**弹 ──
 * 只有 0 或 1 个可选地址时直接往下走。一个只有一个选项的选择窗口不提供任何
 * 信息，只是在菜单项和它真正做的那件事之间插一道空转的确认 —— 而空转的确认
 * 恰恰会训练用户闭着眼睛点下一步，等到真有多个地址时那一屏也会被同样地略过。
 * **有得选才弹**，是这个交互唯一说得通的口径。
 * 一条都没配时那唯一的选项就是站点自身（见 {@link qyResolveAddressOptions}），
 * 也就是本功能上线之前的行为：运营什么都不配 = 与旧版完全一致。
 *
 * ── 为什么点击时读缓存，而不是直接 await 一次请求 ──
 * 写剪贴板需要用户手势。`await` 一次网络请求之后再写，在部分浏览器上手势已经
 * 过期，复制会**静默失败** —— 表现是"点了没反应"，用户只会再点一次。
 * 所以菜单一打开就预热（{@link QyApiAddressPicker.prefetch}），点击时同步读缓存：
 * 命中且只有一个选项就地执行，手势完整；没命中就打开窗口，而窗口里的确认
 * 按钮本身又是一次新的用户手势。两条路径都不会静默失败。
 * CC Switch 那条路（`window.open`）同样吃这一套手势限制，所以两个入口共用。
 *
 * ── 为什么不记住用户上次选的那条 ──
 * 记住只能是"预选中"，不能是"跳过窗口"：跳过之后用户再也看不到还有别的线路
 * 可选，而换线路恰恰是这个功能存在的理由。而预选中省不下任何一次点击（确认
 * 按钮照按），换来的是一条"记着的地址已经被运营删掉/停用了"的回退分支，以及
 * "运营把优先线路调到第一位、老用户却永远看不到"这种反直觉。默认选第一条 ——
 * 也就是运营排的那个顺序 —— 信息量严格更大。
 */
export function useQyApiAddressPicker(
  options: QyApiAddressPickerOptions
): QyApiAddressPicker {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [pendingKey, setPendingKey] = useState<string | null>(null)
  const { onPick } = options

  const prefetch = useCallback(() => {
    void queryClient.prefetchQuery(qyApiAddressesQuery())
  }, [queryClient])

  const pick = useCallback(
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
      const addresses = qyResolveAddressOptions(cached, t('qy_aa_site_default'))
      if (addresses.length === 1) {
        onPick(realKey, addresses[0].url)
        return
      }
      setPendingKey(realKey)
    },
    [onPick, queryClient, t]
  )

  return {
    prefetch,
    pick,
    dialog: (
      <QyApiAddressPickerDialog
        pendingKey={pendingKey}
        description={options.description}
        confirmLabel={options.confirmLabel}
        onClose={() => setPendingKey(null)}
        onPick={(url) => {
          const realKey = pendingKey
          setPendingKey(null)
          if (realKey != null) onPick(realKey, url)
        }}
      />
    ),
  }
}

export type QyConnectionInfoCopy = {
  /** 见 {@link QyApiAddressPicker.prefetch}。 */
  prefetch: () => void
  /** 用户点了「复制链接信息」。`realKey` 是已解析出的完整 `sk-` 密钥。 */
  copy: (realKey: string) => void
  /** 见 {@link QyApiAddressPicker.dialog}。 */
  dialog: ReactNode
}

/** 「先选地址、再复制连接信息」—— {@link useQyApiAddressPicker} 的复制入口。 */
export function useQyConnectionInfoCopy(): QyConnectionInfoCopy {
  const { t } = useTranslation()

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

  const picker = useQyApiAddressPicker({
    description: t('qy_aa_pick_desc'),
    confirmLabel: t('qy_aa_pick_confirm'),
    onPick: (realKey, url) => {
      void write(realKey, url)
    },
  })

  return {
    prefetch: picker.prefetch,
    copy: picker.pick,
    dialog: picker.dialog,
  }
}

/**
 * 密钥页顶上那条「API 地址 + 复制 / 带 V1 复制」。
 *
 * 从这里再导出一次，而不是让调用方直接 import 那个文件：这个目录对外就是
 * 「API 地址簿的用户侧消费口」，选线路与复制地址读的是同一份清单、同一个
 * react-query 键，入口摆在一处才不会有人再去别处捞一份地址。
 */
export { QyApiAddressCopyBar } from './copy-bar'
