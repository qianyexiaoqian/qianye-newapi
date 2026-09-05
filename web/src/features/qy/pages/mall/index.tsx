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
import { Store } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { QyPageBoundary } from '../../components/qy-page-boundary'
import { qyArray } from '../../lib/array'
import { QyPager } from '../components/qy-pager'
import { qyMallProductsQuery } from './api'
import { QyMallOrderDialog } from './components/mall-order-dialog'
import { QyMallProductCard } from './components/mall-product-card'
import { QyMallProductDialog } from './components/mall-product-dialog'
import type { QyMallOrderTarget } from './lib/order'

/** 卡片是三列网格，12 张正好四行；与抽奖大厅同一个数。 */
const PAGE_SIZE = 12

/**
 * 商品列表（「星屑商城」选择夹的第一张标签）。
 *
 * 详情弹窗与下单弹窗由本组件持有、**并列**渲染：点购买时先关详情再开下单，
 * 而不是叠两层模态。下单弹窗会把商品名与价格再复述一遍，用户不会失去上下文；
 * 而叠开的两层模态在移动端抽屉形态下焦点与滚动锁定并不可靠。
 *
 * 下单成功后的收尾（全量失效 / 套餐再刷顶栏余额）在下单弹窗里，这里不重复。
 */
export function QyMallProductsBody() {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  const [detailNo, setDetailNo] = useState<string | null>(null)
  const [orderTarget, setOrderTarget] = useState<QyMallOrderTarget | null>(null)

  const query = useQuery(qyMallProductsQuery({ page, page_size: PAGE_SIZE }))
  const items = qyArray(query.data?.items)
  // 列表用一次渲染时的时钟就够：售期以秒计，这一屏不需要倒计时。
  const nowSeconds = Math.floor(Date.now() / 1000)

  return (
    <>
      <QyPageBoundary
        query={query}
        isEmpty={query.data != null && items.length === 0}
        emptyIcon={Store}
        emptyTitle={t('qy_ml_empty_title')}
        emptyDescription={t('qy_ml_empty_desc')}
      >
        <div className='space-y-3'>
          <div className='grid gap-3 sm:grid-cols-2 xl:grid-cols-3'>
            {items.map((product) => (
              <QyMallProductCard
                key={product.product_no}
                product={product}
                nowSeconds={nowSeconds}
                onOpen={setDetailNo}
              />
            ))}
          </div>
          <QyPager
            page={page}
            pageSize={PAGE_SIZE}
            total={query.data?.total ?? 0}
            disabled={query.isFetching}
            onPageChange={setPage}
          />
        </div>
      </QyPageBoundary>

      <QyMallProductDialog
        productNo={detailNo}
        onClose={() => setDetailNo(null)}
        onBuy={(target) => {
          setDetailNo(null)
          setOrderTarget(target)
        }}
      />
      <QyMallOrderDialog
        target={orderTarget}
        onClose={() => setOrderTarget(null)}
      />
    </>
  )
}
