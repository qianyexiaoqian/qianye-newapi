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
export const CHANNEL_CONNECTION_INFO_TYPE = 'newapi_channel_conn'

export type ChannelConnectionInfo = {
  key: string
  url: string
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

/**
 * The site's own API base address.
 *
 * This is the fallback used when no explicit address has been picked: the
 * address configured by the operator in system settings, or the current origin
 * when that is unavailable. It lives next to `encodeChannelConnectionInfo`
 * because it is the default value of the very `url` field that function emits —
 * keeping the two apart is what produced separate copies of this logic in the
 * first place.
 */
export function readSiteServerAddress(): string {
  try {
    const raw = localStorage.getItem('status')
    if (raw) {
      const status: unknown = JSON.parse(raw)
      if (
        isRecord(status) &&
        typeof status.server_address === 'string' &&
        status.server_address !== ''
      ) {
        return status.server_address
      }
    }
  } catch {
    /* malformed or unavailable storage — fall through to the origin */
  }
  return window.location.origin
}

export function encodeChannelConnectionInfo(key: string, url: string): string {
  return JSON.stringify({
    _type: CHANNEL_CONNECTION_INFO_TYPE,
    key,
    url,
  })
}

export function parseChannelConnectionInfo(
  text: string | null | undefined
): ChannelConnectionInfo | null {
  if (!text || typeof text !== 'string') return null

  try {
    const parsed: unknown = JSON.parse(text.trim())
    if (
      isRecord(parsed) &&
      parsed._type === CHANNEL_CONNECTION_INFO_TYPE &&
      typeof parsed.key === 'string' &&
      typeof parsed.url === 'string'
    ) {
      return { key: parsed.key, url: parsed.url }
    }
  } catch {
    /* not valid connection info JSON */
  }

  return null
}
