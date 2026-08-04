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
import { Ticket } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'

import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { useQyConfig } from '../../hooks/use-qy-config'
import { qyArray } from '../../lib/array'
import { QyPager } from '../components/qy-pager'
import { QyFilterBar, QyFilterField } from '../ops/qy-ops-ui'
import { QY_LOT_PAGE_SIZE, qyLotActivitiesQuery } from './api'
import { QyLotActivityCard } from './components/lottery-activity-card'
import { useQyNowSeconds } from './lib/use-now'

const ALL = 'all'

/**
 * 抽奖 / 竞猜大厅。
 *
 * 分「进行中」与「已结束」两张标签，而不是把已结束的混在后面翻页 ——
 * 项目方要的「结束的抽奖活动要保留，作为历史公正查询」落点就在第二张标签上：
 * 它不是归档，是每个人随时可以回去复核的地方。
 */
export function QyLottery() {
  const { t } = useTranslation()
  const config = useQyConfig()
  const now = useQyNowSeconds()

  const [kind, setKind] = useState(ALL)
  const [scope, setScope] = useState<'done' | 'open'>('open')
  const [page, setPage] = useState(1)

  const params = {
    p: page,
    page_size: QY_LOT_PAGE_SIZE,
    kind: kind === ALL ? undefined : kind,
    status: scope,
  }
  const query = useQuery(qyLotActivitiesQuery(params))
  const items = qyArray(query.data?.items)

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_nav_lottery')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Actions>
        <Button
          size='sm'
          variant='outline'
          render={<Link to='/qy/lottery-records' />}
        >
          {t('qy_nav_lottery_records')}
        </Button>
      </QySectionPageLayout.Actions>
      <QySectionPageLayout.Content>
        <div className='space-y-3'>
          {/* 站点把展示开关关掉时，导航里已经没有这一行了；能到这里的只有
              直达链接。给一句中性说明而不是红色报错 —— 功能没坏，只是这一期
              不对外开放。 */}
          {config.status === 'enabled' && !config.lottery.show_entry && (
            <p className='text-muted-foreground rounded-lg border p-3 text-sm'>
              {t('qy_lot_entry_hidden_note')}
            </p>
          )}

          <Tabs
            value={scope}
            onValueChange={(value) => {
              setScope(value === 'done' ? 'done' : 'open')
              setPage(1)
            }}
          >
            <div className='flex flex-wrap items-end justify-between gap-2'>
              <TabsList>
                <TabsTrigger value='open'>{t('qy_lot_tab_open')}</TabsTrigger>
                <TabsTrigger value='done'>{t('qy_lot_tab_done')}</TabsTrigger>
              </TabsList>
              <QyFilterBar>
                <QyFilterField label={t('qy_lot_filter_kind')}>
                  <Select
                    value={kind}
                    onValueChange={(value) => {
                      setKind(value ?? ALL)
                      setPage(1)
                    }}
                  >
                    <SelectTrigger className='w-32'>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value={ALL}>{t('qy_common_all')}</SelectItem>
                      <SelectItem value='draw'>
                        {t('qy_lot_kind_draw')}
                      </SelectItem>
                      <SelectItem value='guess'>
                        {t('qy_lot_kind_guess')}
                      </SelectItem>
                    </SelectContent>
                  </Select>
                </QyFilterField>
              </QyFilterBar>
            </div>
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
                onPageChange={setPage}
                disabled={query.isFetching}
              />
            </div>
          </QyPageBoundary>
        </div>
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}
