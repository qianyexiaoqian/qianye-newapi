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
import assert from 'node:assert/strict'
import { test } from 'node:test'

import { qyNextSort } from '../sort'

test('clicking the active column flips between most-first and least-first', () => {
  assert.deepEqual(qyNextSort({ key: 'amount', order: 'desc' }, 'amount'), {
    key: 'amount',
    order: 'asc',
  })
  assert.deepEqual(qyNextSort({ key: 'amount', order: 'asc' }, 'amount'), {
    key: 'amount',
    order: 'desc',
  })
})

test('clicking another column starts from most-first', () => {
  assert.deepEqual(qyNextSort({ key: 'amount', order: 'asc' }, 'created_at'), {
    key: 'created_at',
    order: 'desc',
  })
})
