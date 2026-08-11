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

import { QyAmountText } from '../components/qy-amount-text'

/**
 * 一个会被批量动作影响到的渠道。
 *
 * `used_quota` 是列表页「已用额度」那一列的原值（quota 整数）。它必须一路传到
 * 确认框：少了它，管理员在按下一个不可逆按钮之前，屏幕上只有一个条数 ——
 * 而 20 个渠道可能对应 3 块钱，也可能对应 3 万块。
 */
export type QyChannelTarget = {
  id: number
  name: string
  used_quota: number
}

/**
 * 受影响渠道的清单。
 *
 * 需求点名要「明确显示影响条数与渠道名」：只说"确定删除 20 个渠道吗"，
 * 用户无法发现自己多勾了一行 —— 而删除之后没有撤销键。
 *
 * `showUsedQuota` 打开时逐行带上这个渠道的已用额度：重置那一屏的合计能回答
 * "一共多少"，但回答不了"是哪一个渠道占了大头"，而后者恰恰是发现选错范围
 * （比如误勾了主力渠道）唯一的信号。
 *
 * 超过 12 条只列前 12 个再加一句"还有 N 个"：全量铺开会把确认按钮顶出屏幕，
 * 而那正是这一屏最需要用户看见的东西。重置这一路按金额从大到小排，
 * 被折叠掉的因此永远是最小的那些。
 */
export function ChannelNameList(props: {
  channels: Array<{ id: number; name: string; used_quota?: number }>
  showUsedQuota?: boolean
}) {
  const { t } = useTranslation()
  const ordered =
    props.showUsedQuota === true
      ? [...props.channels].sort(
          (a, b) => (b.used_quota ?? 0) - (a.used_quota ?? 0)
        )
      : props.channels
  const shown = ordered.slice(0, 12)
  const rest = ordered.length - shown.length

  return (
    <div className='space-y-2'>
      <ul className='max-h-48 space-y-1 overflow-y-auto rounded-md border p-2 text-sm'>
        {shown.map((channel) => (
          <li key={channel.id} className='flex items-center gap-2'>
            <span className='min-w-0 flex-1 truncate'>
              <span className='font-medium'>
                {channel.name === '' ? `#${channel.id}` : channel.name}
              </span>
              <span className='text-muted-foreground ml-1'>#{channel.id}</span>
            </span>
            {props.showUsedQuota === true && (
              <QyAmountText
                quota={channel.used_quota ?? 0}
                variant='ledger'
                className='shrink-0 text-xs'
              />
            )}
          </li>
        ))}
      </ul>
      {rest > 0 && (
        <p className='text-muted-foreground text-xs'>
          {t('qy_chops_more_channels', { count: rest })}
        </p>
      )}
    </div>
  )
}
