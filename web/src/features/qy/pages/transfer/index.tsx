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

import { QyPageBoundary } from '../../components/qy-page-boundary'
import { useQyConfig } from '../../hooks/use-qy-config'
import { qyTransferLimitsQuery } from './api'
import { TransferForm } from './components/transfer-form'
import { TransferLimitsCard } from './components/transfer-limits-card'

/**
 * 发起划转（钱包页「余额划转」选择夹的第一张标签，需求 2）。
 *
 * 只导出正文：区段头、以及原来指向「划转记录」的那个按钮都由选择夹接管 ——
 * 记录现在就是隔壁一张标签，再放一个跳转按钮等于让用户离开当前这一屏去到
 * 同一屏的另一个位置。
 *
 * 本组件只负责取限额与排版；不可逆确认、幂等键、错误分流全部收敛在
 * `TransferForm` 里，因为那三件事必须一起看才说得清。
 */
export function QyTransferBody() {
  const config = useQyConfig()
  const limitsQuery = useQuery(qyTransferLimitsQuery())

  return (
    <QyPageBoundary query={limitsQuery}>
      {limitsQuery.data != null && (
        <div className='grid gap-3 lg:grid-cols-[minmax(0,1.6fr)_minmax(0,1fr)] lg:items-start'>
          <TransferForm
            limits={limitsQuery.data}
            degraded={config.status === 'enabled' && !config.available}
          />
          <TransferLimitsCard limits={limitsQuery.data} />
        </div>
      )}
    </QyPageBoundary>
  )
}
