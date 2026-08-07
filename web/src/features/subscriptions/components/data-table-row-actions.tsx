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
import type { Row } from '@tanstack/react-table'
import { Boxes, Pencil, Power, PowerOff, RotateCcw, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { useQyConfig } from '@/features/qy/hooks/use-qy-config'
import { QyPlanEntitlementDialog } from '@/features/qy/plan-entitlement/plan-entitlement-dialog'

import type { PlanRecord } from '../types'
import { useSubscriptions } from './subscriptions-provider'

interface DataTableRowActionsProps {
  row: Row<PlanRecord>
}

export function DataTableRowActions({ row }: DataTableRowActionsProps) {
  const { t } = useTranslation()
  const { setOpen, setCurrentRow, complianceConfirmed } = useSubscriptions()
  const qyConfig = useQyConfig()
  // 「解锁模型分组 + 余额使用范围」是扩展提供的能力，落扩展库的两张表。
  // 弹窗状态留在本组件里，不进上游那个 dialog 类型联合 —— 那会把一次纯新增
  // 变成对上游状态机的改动，而它带来的唯一好处只是少一个 useState。
  const [entitlementOpen, setEntitlementOpen] = useState(false)
  const isEnabled = row.original.plan.enabled
  const toggleLabel = isEnabled ? t('Disable') : t('Enable')
  // 删除是扩展提供的能力（上游没有这个接口）。扩展明确关闭时按 qy 的口径零痕迹
  // 隐藏入口，而不是留一个点了必然 404 的按钮；status 还是 'unknown' 时先显示，
  // 真按下去也只是在弹窗里报一次失败，比入口忽隐忽现好。
  const canDelete = qyConfig.status !== 'disabled'

  const handleEdit = () => {
    setCurrentRow(row.original)
    setOpen('update')
  }

  const handleToggleStatus = () => {
    setCurrentRow(row.original)
    setOpen('toggle-status')
  }

  const handleResetSubscriptions = () => {
    setCurrentRow(row.original)
    setOpen('reset-subscriptions')
  }

  const handleDelete = () => {
    setCurrentRow(row.original)
    setOpen('delete')
  }

  return (
    <div className='-ml-1.5 flex items-center gap-1'>
      <Tooltip>
        <TooltipTrigger
          render={
            <Button
              variant='ghost'
              size='icon-sm'
              disabled={!complianceConfirmed}
              onClick={handleEdit}
              aria-label={t('Edit')}
            />
          }
        >
          <Pencil />
        </TooltipTrigger>
        <TooltipContent>{t('Edit')}</TooltipContent>
      </Tooltip>

      <Tooltip>
        <TooltipTrigger
          render={
            <Button
              variant='ghost'
              size='icon-sm'
              disabled={!complianceConfirmed}
              onClick={handleResetSubscriptions}
              aria-label={t('Reset subscription quota')}
            />
          }
        >
          <RotateCcw />
        </TooltipTrigger>
        <TooltipContent>{t('Reset subscription quota')}</TooltipContent>
      </Tooltip>

      <Tooltip>
        <TooltipTrigger
          render={
            <Button
              variant='ghost'
              size='icon-sm'
              disabled={!complianceConfirmed}
              onClick={handleToggleStatus}
              aria-label={toggleLabel}
              className={
                isEnabled
                  ? 'text-destructive hover:text-destructive'
                  : 'text-success hover:text-success'
              }
            />
          }
        >
          {isEnabled ? <PowerOff /> : <Power />}
        </TooltipTrigger>
        <TooltipContent>{toggleLabel}</TooltipContent>
      </Tooltip>

      {canDelete && (
        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                variant='ghost'
                size='icon-sm'
                disabled={!complianceConfirmed}
                onClick={() => setEntitlementOpen(true)}
                aria-label={t('qy_plan_entitlement_title', {
                  plan: row.original.plan.title,
                })}
              />
            }
          >
            <Boxes />
          </TooltipTrigger>
          <TooltipContent>{t('qy_plan_entitlement_action')}</TooltipContent>
        </Tooltip>
      )}

      {canDelete && (
        <QyPlanEntitlementDialog
          open={entitlementOpen}
          onOpenChange={setEntitlementOpen}
          planId={row.original.plan.id}
          planTitle={row.original.plan.title}
        />
      )}

      {canDelete && (
        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                variant='ghost'
                size='icon-sm'
                disabled={!complianceConfirmed}
                onClick={handleDelete}
                aria-label={t('qy_plan_delete_title')}
                className='text-destructive hover:text-destructive'
              />
            }
          >
            <Trash2 />
          </TooltipTrigger>
          <TooltipContent>{t('qy_plan_delete_title')}</TooltipContent>
        </Tooltip>
      )}
    </div>
  )
}
