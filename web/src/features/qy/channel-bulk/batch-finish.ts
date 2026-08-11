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
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { channelsQueryKeys } from '@/features/channels/lib'

import type { QyChannelBatchResult } from './api'
import { useQyChannelBatchResultStore } from './result-store'

/**
 * 一次渠道批次跑完之后的统一收尾。
 *
 * # 为什么是一个共用的 hook，而不是每个入口各写一遍
 *
 * 「清空已用额度」现在有三个入口（工具条、列表头的直达入口、行内单渠道），
 * 而收尾这件事有三处极易写错、写错了又**没有任何运行时信号**的细节：
 *
 *   1. **判据是响应体，不是 HTTP 状态码。** 接口回 200 不等于事情做成了，
 *      succeeded/skipped/failed 才是真相。少看一眼就会对着一批全失败的结果
 *      弹绿色 toast。
 *   2. **报告必须先于清空选中态打开。** 清空选中会把上游工具条连同挂在里面的
 *      组件一起卸载（见 result-store.ts），顺序反过来那次 setState 被静默丢弃，
 *      屏幕上只剩一句"请查看明细"而明细没有入口。
 *   3. **skipped 要单独说。** "全是本来就不用动"常常意味着选错范围了。
 *
 * 三个入口各抄一份，就是本仓反复出现的「同一概念的第 N 份拷贝」——
 * 而这一份拷贝的错法恰好都是静默的。
 *
 * @param onDone 批次收尾回调。工具条那一路在这里清空选中态，
 *   直达入口那一路在这里关掉自己的弹窗。**它一定在报告打开之后才被调用。**
 */
export function useQyChannelBatchFinish(onDone: () => void) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const openReport = useQyChannelBatchResultStore((s) => s.open)

  return <T extends QyChannelBatchResult>(
    title: string,
    /**
     * 全成功时的提示文案。默认只说条数；重置那一路覆盖它，
     * 把后端回来的**真正被抹掉的金额**说出来 —— 确认框里那个数是估算，
     * 结果不能把估算值复述一遍当成事实。
     */
    okMessage?: (result: T) => string
  ) =>
    (result: T) => {
      // 报告先开：openReport 写的是组件树之外的 store，而下面的 onDone()
      // 会卸载调用方（工具条那一路清空选中数、直达入口那一路关闭弹窗）。
      // 顺序反过来的那一版里，这一句 setState 落在已卸载的实例上被静默丢弃 ——
      // 屏幕上只剩一句红色 toast，而它指向的"明细"没有任何入口。
      if (result.failed > 0 || result.skipped > 0) {
        openReport({ result, title })
      }
      queryClient.invalidateQueries({ queryKey: channelsQueryKeys.lists() })
      onDone()

      if (result.failed > 0) {
        // 有失败就一定把明细摊开：toast 装不下 20 行，也留不住，而管理员
        // 接下来要做的事需要那份名单一直在屏幕上。
        toast.error(t('qy_chops_toast_partial', { count: result.failed }))
        return
      }
      if (result.skipped > 0) {
        // 全是"本来就不用动"也要说出来：它常常意味着选错范围了。
        toast.success(
          t('qy_chops_toast_with_skipped', {
            done: result.succeeded,
            skipped: result.skipped,
          })
        )
        return
      }
      toast.success(
        okMessage
          ? okMessage(result)
          : t('qy_chops_toast_ok', { count: result.succeeded })
      )
    }
}
