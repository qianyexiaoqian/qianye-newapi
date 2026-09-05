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
import type { TFunction } from 'i18next'

import type { QyInviteRelation } from '../types'

/**
 * 「下线 (#id) ← 上线 (#id)」，主库查不到的那一侧只印 id。
 *
 * 解绑 / 换绑两个弹窗的副标题共用：同一条关系在两处印成两种样子，运营会以为
 * 自己点开的不是同一条。
 */
export function qyRelationSubject(
  relation: QyInviteRelation,
  t: TFunction
): string {
  const invitee = relation.invitee_resolved
    ? `${relation.invitee_username} (#${relation.invitee_id})`
    : `#${relation.invitee_id}`
  const inviter = relation.inviter_resolved
    ? `${relation.inviter_username} (#${relation.inviter_id})`
    : `#${relation.inviter_id}`
  return t('qy_rel_pair', { invitee, inviter })
}
