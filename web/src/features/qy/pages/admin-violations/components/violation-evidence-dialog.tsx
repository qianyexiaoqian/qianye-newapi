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
import { ScrollText, TriangleAlert } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { qyKeys } from '../../../lib/query-keys'
import { qyOpsErrorMessage } from '../../ops/errors'
import { formatQyCount } from '../../ops/format'
import { QyKeyValue } from '../../ops/qy-ops-ui'
import { getQyViolationEvidence } from '../api'
import type { QyViolationRecord } from '../types'

type QyViolationEvidenceDialogProps = {
  record: QyViolationRecord | null
  onClose: () => void
}

/**
 * 违规上下文查看器。
 *
 * **必须先明确告知「查看会记入审计」再取数**：后端在读取成功之后、返回之前
 * 写 `records.view_evidence`（带 actor / target / rec_no）。管理员有权看，
 * 但必须知道自己看了会留痕 —— 事后被问到「你为什么翻这个用户的输入」时，
 * 「我不知道会记录」不是一个能接受的答案。
 *
 * 因此请求只在用户显式确认后才发出（`enabled: confirmed`）。
 */
export function QyViolationEvidenceDialog(
  props: QyViolationEvidenceDialogProps
) {
  const { t } = useTranslation()
  const [confirmed, setConfirmed] = useState(false)
  const record = props.record

  // 换一条记录就要重新确认一次：确认状态残留会让「刻意动作」失去意义。
  useEffect(() => {
    setConfirmed(false)
  }, [record?.id])

  const query = useQuery({
    queryKey: qyKeys.adminViolationEvidence(record?.id ?? 0),
    queryFn: () => getQyViolationEvidence(record?.id ?? 0),
    enabled: confirmed && record != null,
    staleTime: 0,
    gcTime: 0,
  })

  return (
    <QyResponsiveDialog
      open={record != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('qy_vio_evidence_title')}
      description={record?.rec_no}
      contentClassName='sm:max-w-3xl'
    >
      {!confirmed && (
        <div className='space-y-3'>
          <Alert variant='destructive'>
            <TriangleAlert />
            <AlertTitle>{t('qy_vio_evidence_audit_title')}</AlertTitle>
            <AlertDescription>
              {t('qy_vio_evidence_audit_desc')}
            </AlertDescription>
          </Alert>
          <Button type='button' onClick={() => setConfirmed(true)}>
            <ScrollText aria-hidden='true' />
            {t('qy_vio_evidence_open')}
          </Button>
        </div>
      )}

      {confirmed && query.isLoading && <LoadingState />}

      {confirmed && query.isError && (
        <ErrorState
          title={t('qy_cfg_error_title')}
          description={qyOpsErrorMessage(query.error, t)}
          onRetry={() => {
            void query.refetch()
          }}
        />
      )}

      {confirmed && query.data != null && (
        <div className='space-y-3'>
          <div className='flex flex-wrap gap-1'>
            {query.data.truncated === true && (
              <Badge variant='outline'>{t('qy_vio_evidence_truncated')}</Badge>
            )}
            {query.data.redacted === true && (
              <Badge variant='outline'>{t('qy_vio_evidence_redacted')}</Badge>
            )}
          </div>

          <div>
            <QyKeyValue label={t('qy_vio_evidence_origin_bytes')}>
              {formatQyCount(query.data.origin_bytes)}
            </QyKeyValue>
            <QyKeyValue label={t('qy_vio_evidence_stored_bytes')}>
              {formatQyCount(query.data.stored_bytes)}
            </QyKeyValue>
            {query.data.redact_stats != null &&
              query.data.redact_stats !== '' && (
                <QyKeyValue label={t('qy_vio_evidence_redact_stats')}>
                  {query.data.redact_stats}
                </QyKeyValue>
              )}
          </div>

          {!query.data.has_payload && (
            <p className='text-muted-foreground text-sm'>
              {t('qy_vio_evidence_no_payload')}
            </p>
          )}

          {query.data.context != null && query.data.context !== '' && (
            <div>
              <h3 className='mb-1 text-sm font-medium'>
                {t('qy_vio_evidence_context')}
              </h3>
              <pre className='bg-muted/40 max-h-80 overflow-auto rounded-md p-2 text-xs break-words whitespace-pre-wrap'>
                {query.data.context}
              </pre>
            </div>
          )}

          {query.data.files != null && query.data.files !== '' && (
            <div>
              <h3 className='mb-1 text-sm font-medium'>
                {t('qy_vio_evidence_files')}
              </h3>
              <pre className='bg-muted/40 max-h-48 overflow-auto rounded-md p-2 text-xs break-words whitespace-pre-wrap'>
                {query.data.files}
              </pre>
            </div>
          )}
        </div>
      )}
    </QyResponsiveDialog>
  )
}
