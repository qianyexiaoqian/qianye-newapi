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
import { Link } from '@tanstack/react-router'
import { FerrisWheel, Plus } from 'lucide-react'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { QyPageBoundary } from '../../../components/qy-page-boundary'
import { qyArray } from '../../../lib/array'
import { QyPager } from '../../components/qy-pager'
import type { QyLotHallState } from '../../lottery/components/lottery-hall-list'
import { useQyNowSeconds } from '../../lottery/lib/use-now'
import { QY_WHEEL_PAGE_SIZE, qyWheelActivitiesQuery } from '../api'
import { QyWheelActivityCard } from './wheel-activity-card'

/**
 * 转盘期次列表：「进行中 / 已结束」分段 + 卡片网格 + 翻页。
 *
 * 形状照 `QyLotHallList`，但**不是**它的一张 lane：转盘不进大厅（design-15 §7.5），
 * 这里自己带 `draw_mode=wheel` 拉列表，`lane` 一律不发 —— 两者同给是 400。
 * 分段与页码由宿主持有（`useQyLotHallCursor`），理由与大厅相同。
 */
export function QyWheelList(props: QyLotHallState) {
  const { t } = useTranslation()
  const now = useQyNowSeconds()
  const { page, scope } = props
  const isAdmin =
    (useAuthStore((state) => state.auth.user?.role) ?? ROLE.GUEST) >= ROLE.ADMIN

  const query = useQuery(
    qyWheelActivitiesQuery({
      p: page,
      page_size: QY_WHEEL_PAGE_SIZE,
      phase: scope,
    })
  )
  // 草稿再挡一次，与大厅同一条第二道（说了算的仍是后端的 `status <> 'draft'`）。
  const items = qyArray(query.data?.items).filter(
    (activity) => activity.status !== 'draft'
  )

  // 空态的出口按人分：管理员要的是"去开一场"，普通用户在「进行中」空着时
  // 给一条通往历史的出口 —— 「已结束」空着就是真的一场都没跑过，没有出口可给。
  let emptyAction: ReactNode
  if (isAdmin) {
    emptyAction = (
      <Button
        size='sm'
        render={<Link to='/qy/admin/lottery' />}
        aria-label={t('qy_lot_empty_admin_create')}
      >
        <Plus aria-hidden='true' />
        {t('qy_lot_empty_admin_create')}
      </Button>
    )
  } else if (scope === 'live') {
    emptyAction = (
      <Button
        size='sm'
        variant='outline'
        onClick={() => {
          props.onScopeChange('ended')
          props.onPageChange(1)
        }}
      >
        {t('qy_lot_empty_open_see_done')}
      </Button>
    )
  }

  return (
    <div className='space-y-3'>
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <Tabs
          value={scope}
          onValueChange={(value) => {
            props.onScopeChange(value === 'ended' ? 'ended' : 'live')
            props.onPageChange(1)
          }}
        >
          <TabsList>
            <TabsTrigger value='live'>{t('qy_lot_tab_open')}</TabsTrigger>
            <TabsTrigger value='ended'>{t('qy_lot_tab_done')}</TabsTrigger>
          </TabsList>
        </Tabs>
        {/* 资金语义与三种抽奖相同：每一转的参与费当场花掉、不退。 */}
        <Badge variant='outline'>{t('qy_lot_risk_badge_stake_lost')}</Badge>
      </div>

      <QyPageBoundary
        query={query}
        isEmpty={query.data != null && items.length === 0}
        emptyIcon={FerrisWheel}
        emptyTitle={
          scope === 'live'
            ? t('qy_lot_empty_open_title')
            : t('qy_lot_empty_done_title')
        }
        emptyDescription={t('qy_lot_wheel_empty_desc')}
        emptyAction={emptyAction}
      >
        <div className='space-y-3'>
          <div className='grid gap-3 sm:grid-cols-2 xl:grid-cols-3'>
            {items.map((activity) => (
              <QyWheelActivityCard
                key={activity.act_no}
                activity={activity}
                nowSeconds={now}
              />
            ))}
          </div>
          <QyPager
            page={page}
            pageSize={QY_WHEEL_PAGE_SIZE}
            total={query.data?.total ?? 0}
            onPageChange={props.onPageChange}
            disabled={query.isFetching}
          />
        </div>
      </QyPageBoundary>
    </div>
  )
}
