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
import { Ticket } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'

import { QyPageBoundary } from '../../../components/qy-page-boundary'
import { qyArray } from '../../../lib/array'
import { QyPager } from '../../components/qy-pager'
import { QY_LOT_PAGE_SIZE, qyLotActivitiesQuery } from '../api'
import { useQyNowSeconds } from '../lib/use-now'
import type { QyLotKind } from '../types'
import { QyLotActivityCard } from './lottery-activity-card'

/**
 * 大厅列表正文（抽奖 / 竞猜共用）。
 *
 * ## 为什么 `kind` 变成了参数而不是一个下拉框
 *
 * 合并成选择夹之后，玩法从"同一张列表里的一个筛选值"升成"一张标签"。这不是
 * 换个位置：抽奖与竞猜的**资金语义不同** —— 抽奖是参与费不退、赢多少由奖档
 * 决定；竞猜是奖池再分配、可能亏掉本金。两者混在同一个列表里靠一个下拉框
 * 区分，用户按上一次的预期下注就会亏，而界面上没有任何东西提醒他换了规则。
 *
 * ## 「进行中 / 已结束」为什么留在标签内部
 *
 * 它是同一批对象的过滤条件，不是另一类内容。升成第四张标签会让导航维度跟着
 * 数据维度长，而项目方点名要的就是三张。已结束那一档是「历史公正查询」的
 * 入口 —— 它不是归档，是每个人随时可以回去复核的地方。
 *
 * ## 分段与页码为什么是受控的（由宿主持有）
 *
 * `QyPageTabs` 刻意不加 `keepMounted`，Base UI 的面板隐藏即卸载，本组件自己
 * 的 `useState` 会在切走标签时归零。表现是：用户翻到「已结束」第 3 页做历史
 * 公正查询，切去「我的参与」核对一条记录再切回来，那一屏没了，要重新翻三次。
 * 状态提到宿主（它不随标签卸载）之后，切换标签只是换一张面板，位置留在原处。
 */
export type QyLotHallState = {
  onPageChange: (page: number) => void
  onScopeChange: (scope: 'done' | 'open') => void
  page: number
  scope: 'done' | 'open'
}

export function QyLotHallList(props: QyLotHallState & { kind: QyLotKind }) {
  const { t } = useTranslation()
  const now = useQyNowSeconds()
  const { page, scope } = props

  const params = {
    p: page,
    page_size: QY_LOT_PAGE_SIZE,
    kind: props.kind,
    status: scope,
  }
  const query = useQuery(qyLotActivitiesQuery(params))
  const items = qyArray(query.data?.items)

  return (
    <div className='space-y-3'>
      <Tabs
        value={scope}
        onValueChange={(value) => {
          props.onScopeChange(value === 'done' ? 'done' : 'open')
          props.onPageChange(1)
        }}
      >
        <TabsList>
          <TabsTrigger value='open'>{t('qy_lot_tab_open')}</TabsTrigger>
          <TabsTrigger value='done'>{t('qy_lot_tab_done')}</TabsTrigger>
        </TabsList>
      </Tabs>

      {/* 列表刻意留在 `Tabs` 外面：两张标签的内容形状完全一样，差别只在
          请求参数。写成两个 `TabsContent` 就是同一段渲染的第二份拷贝，
          迟早漂移成"已结束那张忘了显示结局"。 */}
      <QyPageBoundary
        query={query}
        isEmpty={query.data != null && items.length === 0}
        emptyIcon={Ticket}
        emptyTitle={t('qy_lot_empty_title')}
        emptyDescription={t('qy_lot_empty_desc')}
      >
        <div className='space-y-3'>
          <div className='grid gap-3 sm:grid-cols-2 xl:grid-cols-3'>
            {items.map((activity) => (
              <QyLotActivityCard
                key={activity.act_no}
                activity={activity}
                nowSeconds={now}
              />
            ))}
          </div>
          <QyPager
            page={page}
            pageSize={QY_LOT_PAGE_SIZE}
            total={query.data?.total ?? 0}
            onPageChange={props.onPageChange}
            disabled={query.isFetching}
          />
        </div>
      </QyPageBoundary>
    </div>
  )
}
