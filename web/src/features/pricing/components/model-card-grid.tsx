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
import { ChevronLeft, ChevronRight } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { getPerfMetricsSummary } from '@/features/performance-metrics/api'
import { useQyConfig } from '@/features/qy/hooks/use-qy-config'
import { qyKeys } from '@/features/qy/lib/query-keys'
import { getQyAvailabilityPlazaCards } from '@/features/qy/pages/availability/api'
import {
  qyPlazaCardToPerfBadge,
  qyPricingPerfSource,
} from '@/features/qy/pages/availability/constants'

import { DEFAULT_PRICING_PAGE_SIZE, DEFAULT_TOKEN_UNIT } from '../constants'
import type { PricingModel, TokenUnit } from '../types'
import { ModelCard } from './model-card'
import type { ModelPerfBadgeData } from './model-perf-badge'

export interface ModelCardGridProps {
  models: PricingModel[]
  onModelClick: (modelName: string) => void
  priceRate?: number
  usdExchangeRate?: number
  tokenUnit?: TokenUnit
  showRechargePrice?: boolean
  selectedGroup?: string
}

export function ModelCardGrid(props: ModelCardGridProps) {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  const pageSize = DEFAULT_PRICING_PAGE_SIZE
  const tokenUnit = props.tokenUnit ?? DEFAULT_TOKEN_UNIT
  const totalPages = Math.max(1, Math.ceil(props.models.length / pageSize))
  const currentPage = Math.min(page, totalPages)

  const pagedModels = useMemo(() => {
    const start = (currentPage - 1) * pageSize
    return props.models.slice(start, start + pageSize)
  }, [currentPage, pageSize, props.models])

  // 卡片状态条按「选中的分组」取扩展的分组可用率(与「模型可用率」页同一口径,
  // 未登录按注册默认分组)。上游 perf_metrics 的全站汇总不分分组,切分组时数字
  // 不会变 —— 只在扩展或可用率功能整体关闭时才回落它。
  const qyConfig = useQyConfig()
  const source = qyPricingPerfSource({
    configLoading: qyConfig.isLoading,
    extensionEnabled: qyConfig.status === 'enabled',
    availabilityOn: qyConfig.features.availability,
  })
  const qyParams = useMemo(
    () => ({
      models: pagedModels.map((model) => model.model_name || '').join(','),
      group: props.selectedGroup ?? 'all',
    }),
    [pagedModels, props.selectedGroup]
  )
  const qyCardsQuery = useQuery({
    queryKey: qyKeys.availabilityPlazaCards(qyParams),
    queryFn: () => getQyAvailabilityPlazaCards(qyParams),
    enabled: source === 'qy' && qyParams.models !== '',
    staleTime: 60 * 1000,
    retry: false,
  })
  const perfQuery = useQuery({
    queryKey: ['perf-metrics-summary', 24],
    queryFn: () => getPerfMetricsSummary(24),
    staleTime: 60 * 1000,
    retry: false,
    enabled: source === 'upstream',
  })

  const perfMap = useMemo(() => {
    const map = new Map<string, ModelPerfBadgeData>()
    if (source === 'qy') {
      for (const card of qyCardsQuery.data?.cards ?? []) {
        map.set(card.model, qyPlazaCardToPerfBadge(card))
      }
      return map
    }
    if (source === 'upstream') {
      for (const model of perfQuery.data?.data?.models ?? []) {
        map.set(model.model_name, model)
      }
    }
    return map
  }, [source, qyCardsQuery.data, perfQuery.data])

  if (props.models.length === 0) {
    return null
  }

  return (
    <div className='flex flex-col gap-4 sm:gap-5'>
      <div className='grid grid-cols-1 gap-3 sm:gap-4 md:grid-cols-2 xl:grid-cols-3'>
        {pagedModels.map((model) => (
          <ModelCard
            key={model.id ?? model.model_name}
            model={model}
            tokenUnit={tokenUnit}
            priceRate={props.priceRate}
            usdExchangeRate={props.usdExchangeRate}
            showRechargePrice={props.showRechargePrice}
            selectedGroup={props.selectedGroup}
            perf={perfMap.get(model.model_name || '')}
            onClick={() => props.onModelClick(model.model_name || '')}
          />
        ))}
      </div>

      {totalPages > 1 && (
        <div className='text-muted-foreground flex flex-col items-center justify-between gap-3 border-t px-4 py-3 text-sm sm:flex-row'>
          <p className='text-muted-foreground'>
            {t('Page {{current}} of {{total}}', {
              current: currentPage,
              total: totalPages,
            })}
          </p>
          <div className='flex items-center gap-2'>
            <Button
              type='button'
              variant='outline'
              size='sm'
              onClick={() => setPage((current) => Math.max(1, current - 1))}
              disabled={currentPage <= 1}
              className='gap-1.5'
            >
              <ChevronLeft className='size-4' />
              {t('Previous page')}
            </Button>
            <Button
              type='button'
              variant='outline'
              size='sm'
              onClick={() =>
                setPage((current) => Math.min(totalPages, current + 1))
              }
              disabled={currentPage >= totalPages}
              className='gap-1.5'
            >
              {t('Next page')}
              <ChevronRight className='size-4' />
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}
