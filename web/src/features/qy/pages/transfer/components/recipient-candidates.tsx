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
import { Users } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'

import { QyMaskedUser } from '../../../components/qy-masked-user'
import type { QyRecipientCandidate } from '../types'

type RecipientCandidatesProps = {
  /** 用户原始输入，复述在提示里，让人知道是哪一串引起的歧义。 */
  identifier: string
  candidates: QyRecipientCandidate[]
  disabled?: boolean
  onPick: (candidate: QyRecipientCandidate) => void
}

/**
 * 收款人歧义时的手动二选一。
 *
 * 纯数字输入在 `id_or_username` 模式下既可能是用户 ID、也可能是某人的用户名；
 * 两边命中不同的人时，后端**不替用户猜**，把两个都列出来。这里同样不预选、
 * 不按顺序默认第一个 —— 选错就是转错人，必须是一次显式点击。
 *
 * 选中之后照旧走完整链路（重新预校验 → 二次确认 → 支付密码），本组件只回传
 * 选了谁。划转表单与通讯录「添加」共用这一份渲染。
 */
export function RecipientCandidates(props: RecipientCandidatesProps) {
  const { t } = useTranslation()
  return (
    <Alert>
      <Users />
      <AlertTitle>{t('qy_tr_ambiguous_title')}</AlertTitle>
      <AlertDescription className='space-y-2'>
        <p>{t('qy_tr_ambiguous_desc', { identifier: props.identifier })}</p>
        <ul className='space-y-1.5'>
          {props.candidates.map((candidate) => (
            <li
              key={candidate.user_id}
              className='flex flex-wrap items-center gap-2 rounded-md border p-2'
            >
              <QyMaskedUser
                className='min-w-0 flex-1'
                userId={candidate.user_id}
                maskedName={candidate.masked_username}
              />
              <Badge variant='outline'>
                {candidate.matched_by === 'id'
                  ? t('qy_tr_matched_by_id')
                  : t('qy_tr_matched_by_username')}
              </Badge>
              <Button
                type='button'
                size='sm'
                variant='outline'
                disabled={props.disabled}
                onClick={() => props.onPick(candidate)}
              >
                {t('qy_tr_pick_candidate')}
              </Button>
            </li>
          ))}
        </ul>
      </AlertDescription>
    </Alert>
  )
}
