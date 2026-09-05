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
import { Route } from 'lucide-react'
import { useState, useCallback } from 'react'
import { useTranslation } from 'react-i18next'

import { IconBadge } from '@/components/ui/icon-badge'
import { ScrollArea } from '@/components/ui/scroll-area'
import {
  testUrlLatency,
  getDefaultPingStatus,
} from '@/features/dashboard/lib/api-info'
import type { PingStatusMap, ApiInfoItem } from '@/features/dashboard/types'
import { qyApiAddressesQuery } from '@/features/qy/pages/api-address-picker/api'

import { PanelWrapper } from '../ui/panel-wrapper'
import { ApiInfoItemComponent } from './api-info-item'

/**
 * 数据源是 qy「API 地址簿」的用户侧清单（`/api/qy/api-addresses`），不再是
 * `/api/status` 里那份对所有访客一视同仁的 `api_info`：地址簿按登录用户的
 * **用户分组**过滤（每行可绑「适用分组」，不绑 = 全员可见兜底），于是这张卡
 * 和密钥页「复制链接信息」看到的是同一批线路。字段映射：route←name、
 * description←remark、color 原样（空串由 getBgColorClass 兜底成默认色）。
 * 「API信息」设置并入地址簿的决策见 qianye/docs/decisions.md。
 */
export function ApiInfoPanel() {
  const { t } = useTranslation()
  const addressesQuery = useQuery(qyApiAddressesQuery('console'))
  const loading = addressesQuery.isLoading
  const list: ApiInfoItem[] = (addressesQuery.data ?? []).map((item) => ({
    url: item.url,
    route: item.name,
    description: item.remark,
    color: item.color ?? '',
  }))
  const [pingStatus, setPingStatus] = useState<PingStatusMap>({})

  const handleTest = useCallback(async (url: string) => {
    setPingStatus((prev) => ({
      ...prev,
      [url]: { latency: null, testing: true, error: false },
    }))

    const result = await testUrlLatency(url)
    setPingStatus((prev) => ({ ...prev, [url]: result }))
  }, [])

  return (
    <PanelWrapper
      title={
        <span className='flex items-center gap-2'>
          <IconBadge tone='info' size='sm'>
            <Route />
          </IconBadge>
          {t('API Info')}
        </span>
      }
      description={t('Configured routes and latency checks')}
      loading={loading}
      empty={!list.length}
      emptyMessage={t('No API routes configured')}
      height='h-72'
      contentClassName='p-0'
    >
      <ScrollArea className='h-72'>
        <div>
          {list.map((item: ApiInfoItem, idx: number) => (
            <div
              key={item.url}
              className={
                idx < list.length - 1 ? 'border-border/60 border-b' : ''
              }
            >
              <ApiInfoItemComponent
                item={item}
                status={pingStatus[item.url] || getDefaultPingStatus()}
                onTest={handleTest}
              />
            </div>
          ))}
        </div>
      </ScrollArea>
    </PanelWrapper>
  )
}
