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
/**
 * 分组列 —— 行内快切。
 *
 * 项目方原话：「用户点击在外面点击分组，把 key 的分组框下拉，快速切换分组。」
 *
 * ── 为什么不加二次确认 ──
 *
 * 改分组确实有副作用（它决定倍率与可用模型）。但行内快切的**全部价值**就是快：
 * 加一个确认弹窗之后，它与"打开编辑抽屉、改分组、保存"的点击数完全一样，
 * 于是这个功能等于没做。所以走的是另一条路：不拦，但**回显后果** ——
 * 成功的 toast 里直接写新分组的倍率是多少。用户切完就知道自己现在按几倍在花钱，
 * 而不是切完之后去别的页面找。
 *
 * 而且它是**可逆**的：切错了再切回来，除了这一格什么都没变（后端走
 * `?group_only=1`，一个别的字段都不碰）。不可逆的动作才值得拦。
 *
 * ── 失败怎么办 ──
 *
 * 显示值**从头到尾没有乐观更新**：请求在飞的时候显示的仍是旧分组 + 一个转圈，
 * 只有后端确认之后才换成后端返回的那个值。所以"回滚"不是一段回滚代码，
 * 而是"根本没动过" —— 这条路径上不存在"显示成功了但库里没改"的中间态。
 *
 * ── 哪些令牌不能改 ──
 *
 * 按状态**一个都不禁**，与编辑抽屉逐位一致：上游的编辑入口对已禁用 / 已过期 /
 * 已耗尽的令牌一律开放，后端 UpdateToken 也没有任何按状态拦截改分组的判据。
 * 在这里自己发明一条"已耗尽不能改分组"，只会造出一个抽屉里做得到、行内做不到
 * 的差异，而用户完全不知道为什么。唯一会禁用的时刻是：请求在飞、或者候选清单
 * 还没到（那时点开是一个空列表）。
 */
import type { Row } from '@tanstack/react-table'
import { ChevronsUpDown, Loader2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { useMediaQuery } from '@/hooks'
import { cn } from '@/lib/utils'

import { updateApiKeyGroup } from '../api'
import { ERROR_MESSAGES } from '../constants'
import type { ApiKeyGroupOptionData } from '../lib/group-options'
import type { ApiKey } from '../types'
import { ApiKeyGroupCell } from './api-key-group-cell'
import { ApiKeyGroupCombobox } from './api-key-group-combobox'
import { useApiKeys } from './api-keys-provider'

/**
 * 这一行**当前**分组的倍率。
 *
 * 显示的是「用户分组 × 令牌分组」解析之后的那一个数 —— 服务端
 * `service.GetUserGroupRatio` 的结论，也就是这一行密钥真正会被乘上的分组倍率。
 * 它不是模型倍率：最终价格还要乘模型自己的倍率，而那个数按模型不同，
 * 一行密钥上根本不存在。
 *
 * 找不到（令牌指向一个当前用户选不了的分组）时返回 `undefined`，
 * 由调用方渲染成「未知」而**不是 1** —— 后端在分组缺失时是 fail-open 返回 1 的，
 * 把那个 1 印在界面上等于编一个价格。
 */
function findGroupRatio(
  options: ApiKeyGroupOptionData[],
  group: string
): number | string | undefined {
  return options.find((option) => option.value === group)?.ratio
}

/**
 * 倍率的显示文本。
 *
 * `Intl.NumberFormat` 而不是模板串：JSON 里的 0.1 + 0.2 这类浮点噪声会原样印成
 * `0.30000000000000004`。6 位小数远超任何现实倍率的精度，所以这里不会把一个
 * 真实配置四舍五入掉，只会把二进制浮点的尾巴削掉。
 */
function formatRatio(ratio: number, locale: string): string {
  return new Intl.NumberFormat(locale, { maximumFractionDigits: 6 }).format(
    ratio
  )
}

/**
 * 分组单元格。
 *
 * **必须是模块级的稳定组件引用**，理由与 `ApiKeyRowActionsCell` 完全一致
 * （见 api-keys-columns.tsx 里那段长注释）：写成内联箭头的话，表格每 30 秒推进
 * 一次 `now` 就会把这个单元格连同它的本地 state 一起卸载重挂 —— 正在飞的那次
 * 切换会在 `await` 回来时把结果写进一个已卸载的实例，转圈永远停不下来，
 * 而且刚确认下来的新分组会闪回旧值。
 */
export function ApiKeyGroupSwitchCell({ row }: { row: Row<ApiKey> }) {
  const { t, i18n } = useTranslation()
  const apiKey = row.original
  const { groupOptions, groupOptionsLoading, triggerRefresh } = useApiKeys()
  const shouldReduceMotion = useMediaQuery('(prefers-reduced-motion: reduce)')
  const [saving, setSaving] = useState(false)
  /*
    后端已经确认、但列表还没刷回来的那一格。

    `base` 是发起这次切换时行数据里的分组。用它做判据而不是无脑覆盖：
    列表刷回来之后 `serverGroup` 会变成新值，`base` 对不上，这一格就自动
    交还给行数据；如果用户随后在编辑抽屉里又改了一次分组，同样对不上，
    也不会被这份陈旧的记忆按住。

    `id` 是第二道、也是更要紧的一道判据：这张表**没有**传 `getRowId`，
    tanstack 于是回落到「row.id = 行序号」，而桌面表格与手机卡片两条渲染
    路径的 key 都是 `row.id`。也就是说这个组件实例是**按行号复用**的 ——
    翻页、排序、搜索、删掉上面一行，都会让同一个实例换上另一把密钥
    （列表查询开着 `placeholderData: previousData`，isLoading 保持 false，
    整行不会卸载重挂）。只用 `base` 判的话，只要新上来的那把密钥分组恰好
    等于 base，它就会显示前一把切换后的分组**连同倍率**——而这一列存在
    的唯一理由就是让用户知道自己按几倍在花钱。所以记忆必须钉在密钥 id 上。
  */
  const [confirmed, setConfirmed] = useState<{
    id: number
    base: string
    value: string
  } | null>(null)

  const serverGroup = apiKey.group ?? ''
  const shownGroup =
    confirmed && confirmed.id === apiKey.id && confirmed.base === serverGroup
      ? confirmed.value
      : serverGroup
  const ratio = findGroupRatio(groupOptions, shownGroup)
  const busy = saving || groupOptionsLoading

  const handleSelect = async (nextGroup: string) => {
    if (nextGroup === shownGroup || saving) return
    setSaving(true)
    try {
      const result = await updateApiKeyGroup(apiKey.id, nextGroup)
      if (!result.success) {
        // 显示值从来没动过，所以这里不需要"回滚"——它本来就还是旧分组。
        toast.error(result.message || t('Failed to switch group'))
        return
      }
      // 以**后端返回的那一份**为准，不是我们发出去的那个值。
      const storedGroup = result.data?.group ?? nextGroup
      setConfirmed({ id: apiKey.id, base: serverGroup, value: storedGroup })
      const storedRatio = findGroupRatio(groupOptions, storedGroup)
      if (typeof storedRatio === 'number') {
        toast.success(
          t('Switched to group {{group}} · group ratio {{ratio}}x', {
            group: storedGroup,
            ratio: formatRatio(
              storedRatio,
              i18n.resolvedLanguage || i18n.language
            ),
          })
        )
      } else if (storedGroup === 'auto') {
        toast.success(
          t('Switched to group {{group}} · ratio depends on the picked group', {
            group: storedGroup,
          })
        )
      } else {
        toast.success(t('Switched to group {{group}}', { group: storedGroup }))
      }
      triggerRefresh()
    } catch {
      toast.error(t(ERROR_MESSAGES.UNEXPECTED))
    } finally {
      setSaving(false)
    }
  }

  const trigger = (
    <Button
      type='button'
      variant='ghost'
      role='combobox'
      size='sm'
      disabled={busy}
      aria-label={t('Switch group')}
      /*
        auto 那句解释在按钮里没有落脚点：默认的 ApiKeyGroupCell 用 Tooltip 挂它，
        而在 `<button>` 内部再挂一层交互元素既不合法也会抢焦点。放进 `title`
        是这一格唯一还能把它说出来的地方 —— 直接删掉的话，「跨分组」这四个字
        在整张表上就再没有任何解释。
      */
      title={
        shownGroup === 'auto'
          ? t(
              'Automatically selects the best available group with circuit breaker mechanism'
            )
          : undefined
      }
      data-api-key-group-switch='trigger'
      className={cn(
        'hover:bg-muted/60 data-popup-open:bg-muted/60 -ml-1.5 h-auto w-full max-w-full justify-between gap-1 overflow-hidden px-1.5 py-1 font-normal',
        busy && 'opacity-70'
      )}
    />
  )

  return (
    <div className='flex min-w-0 items-center gap-1'>
      <ApiKeyGroupCombobox
        options={groupOptions}
        value={shownGroup}
        onValueChange={(next) => void handleSelect(next)}
        disabled={busy}
        trigger={trigger}
        popoverClassName='w-auto min-w-[var(--anchor-width)] sm:min-w-[320px]'
      >
        <span className='min-w-0 flex-1 overflow-hidden'>
          {/* inline：只用 span 渲染。理由见 ApiKeyGroupCell 的 `inline` 那段注释 ——
              这一整块住在 `<button>` 里，`<div>` 与嵌套的 Tooltip 都不该出现在那儿。 */}
          <ApiKeyGroupCell
            inline
            group={shownGroup}
            ratio={ratio}
            crossGroupRetry={apiKey.cross_group_retry}
            shouldReduceMotion={shouldReduceMotion}
          />
        </span>
        {saving ? (
          <Loader2
            aria-hidden='true'
            className='size-3.5 shrink-0 animate-spin opacity-70'
          />
        ) : (
          <ChevronsUpDown
            aria-hidden='true'
            className='size-3.5 shrink-0 opacity-50'
          />
        )}
      </ApiKeyGroupCombobox>
      {/*
        倍率未知时**显式画一个「—」**，而不是什么都不画。

        后端在分组倍率查不到时是 fail-open 返回 1 的（见 ratio_setting 的
        lookupGroupRatio），所以这里绝不能顺手填 1 —— 那是把一个编出来的价格
        印在用户面前。空着也不行：空与「1x 恰好没画出来」在视觉上不可分辨。
        两种"未知"各有各的原因，文案必须分开，因为用户接下来该做的事不一样。
      */}
      {ratio === undefined && !groupOptionsLoading && (
        <Tooltip>
          <TooltipTrigger
            render={
              <span
                data-api-key-group-ratio='unknown'
                className='text-muted-foreground shrink-0 font-mono text-xs'
              />
            }
          >
            —
          </TooltipTrigger>
          <TooltipContent>
            <span className='text-xs'>
              {shownGroup === ''
                ? t(
                    'This key follows your user group, so its group ratio depends on that group'
                  )
                : t(
                    'This group is not in your selectable list, so its group ratio is unknown'
                  )}
            </span>
          </TooltipContent>
        </Tooltip>
      )}
    </div>
  )
}
