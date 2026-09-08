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
import { Pencil, Plus, Search, Trash2 } from 'lucide-react'
import { useState, useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table/static/static-data-table'
import { StaticRowActions } from '@/components/data-table/static/static-row-actions'
import { ReactIconByName } from '@/components/react-icon-by-name'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

import { safeJsonParseWithValidation } from '../utils/json-parser'
import { isArray } from '../utils/json-validators'
import {
  ExternalLinkDialog,
  type ExternalLinkData,
} from './external-link-dialog'

type ExternalLinksVisualEditorProps = {
  value: string
  onChange: (value: string) => void
  /** The configured preset top-up amounts, offered as the picker's options. */
  amountOptions: number[]
}

function isExternalLink(item: unknown): item is ExternalLinkData {
  return (
    typeof item === 'object' &&
    item !== null &&
    'amount' in item &&
    'name' in item &&
    'url' in item &&
    typeof item.amount === 'number' &&
    typeof item.name === 'string' &&
    typeof item.url === 'string' &&
    (!('icon' in item) || typeof item.icon === 'string')
  )
}

/**
 * Links have no id, so the bound amount plus the destination is the row
 * identity used for edit and delete.
 */
function isSameLink(item: unknown, link: ExternalLinkData): boolean {
  return (
    isExternalLink(item) && item.amount === link.amount && item.url === link.url
  )
}

export function ExternalLinksVisualEditor(
  props: ExternalLinksVisualEditorProps
) {
  const { t } = useTranslation()
  const [searchText, setSearchText] = useState('')
  const [dialogOpen, setDialogOpen] = useState(false)
  const [editData, setEditData] = useState<ExternalLinkData | null>(null)

  const links = useMemo(() => {
    const parsed = safeJsonParseWithValidation<unknown[]>(props.value, {
      fallback: [],
      validator: isArray,
      validatorMessage: 'External links must be a JSON array',
      context: 'topup external links',
    })

    return parsed.filter(isExternalLink)
  }, [props.value])

  const filteredLinks = useMemo(() => {
    if (!searchText) return links
    const lowerSearch = searchText.toLowerCase()
    return links.filter(
      (link) =>
        link.name.toLowerCase().includes(lowerSearch) ||
        link.url.toLowerCase().includes(lowerSearch) ||
        String(link.amount).includes(lowerSearch)
    )
  }, [links, searchText])

  const parseCurrent = () =>
    safeJsonParseWithValidation<unknown[]>(props.value, {
      fallback: [],
      validator: isArray,
      silent: true,
    })

  const handleSave = (data: ExternalLinkData) => {
    const updatedArray = [...parseCurrent()]
    const index = editData
      ? updatedArray.findIndex((item) => isSameLink(item, editData))
      : -1

    if (index === -1) {
      updatedArray.push(data)
    } else {
      updatedArray[index] = data
    }

    props.onChange(JSON.stringify(updatedArray, null, 2))
  }

  const handleDelete = (link: ExternalLinkData) => {
    props.onChange(
      JSON.stringify(
        parseCurrent().filter((item) => !isSameLink(item, link)),
        null,
        2
      )
    )
  }

  const handleEdit = (link: ExternalLinkData) => {
    setEditData(link)
    setDialogOpen(true)
  }

  return (
    <div className='space-y-4'>
      <div className='flex flex-col gap-3 sm:flex-row sm:items-center'>
        <div className='relative flex-1'>
          <Search className='text-muted-foreground absolute top-2.5 left-2.5 h-4 w-4' />
          <Input
            placeholder={t('Search external links...')}
            value={searchText}
            onChange={(e) => setSearchText(e.target.value)}
            className='pl-9'
          />
        </div>
        <Button
          type='button'
          onClick={(e) => {
            e.preventDefault()
            e.stopPropagation()
            setEditData(null)
            setDialogOpen(true)
          }}
          className='sm:flex-none'
        >
          <Plus className='h-4 w-4 sm:mr-2' />
          <span className='sm:inline'>{t('Add link')}</span>
        </Button>
      </div>

      {filteredLinks.length === 0 ? (
        <div className='text-muted-foreground rounded-lg border border-dashed p-8 text-center text-sm'>
          {searchText
            ? t('No external links match your search')
            : t(
                'No external links configured. Click "Add link" to bind one to a top-up amount.'
              )}
        </div>
      ) : (
        <div className='rounded-md border'>
          {/* Desktop table view */}
          <StaticDataTable
            className='hidden rounded-none border-0 md:block'
            data={filteredLinks}
            getRowKey={(link, index) => `${link.url}-${index}`}
            columns={[
              {
                id: 'amount',
                header: t('Bound top-up amount'),
                cell: (link) => (
                  <code className='bg-muted rounded px-1.5 py-0.5 text-sm'>
                    {link.amount}
                  </code>
                ),
              },
              {
                id: 'name',
                header: t('Name'),
                cellClassName: 'font-medium',
                cell: (link) => link.name,
              },
              {
                id: 'url',
                header: t('Target URL'),
                cell: (link) => (
                  <span className='text-muted-foreground block max-w-xs truncate font-mono text-sm'>
                    {link.url}
                  </span>
                ),
              },
              {
                id: 'icon',
                header: t('Icon'),
                cell: (link) =>
                  link.icon ? (
                    <div className='flex items-center gap-2'>
                      <ReactIconByName
                        name={link.icon}
                        className='text-muted-foreground size-5 shrink-0'
                        title={link.icon}
                      />
                      <span className='text-muted-foreground truncate font-mono text-sm'>
                        {link.icon}
                      </span>
                    </div>
                  ) : (
                    <span className='text-muted-foreground text-sm'>—</span>
                  ),
              },
              {
                id: 'actions',
                header: t('Actions'),
                className: 'text-right',
                cellClassName: 'text-right',
                cell: (link) => (
                  <StaticRowActions
                    editLabel={t('Edit')}
                    deleteLabel={t('Delete')}
                    menuLabel={t('Open menu')}
                    onEdit={() => handleEdit(link)}
                    onDelete={() => handleDelete(link)}
                  />
                ),
              },
            ]}
          />

          {/* Mobile card view */}
          <div className='divide-y md:hidden'>
            {filteredLinks.map((link) => (
              <div key={`${link.amount}-${link.url}`} className='p-4'>
                <div className='mb-3 flex items-start justify-between gap-2'>
                  <div className='min-w-0 flex-1'>
                    <div className='mb-1 flex items-center gap-2'>
                      <code className='bg-muted rounded px-1.5 py-0.5 text-xs'>
                        {link.amount}
                      </code>
                      <span className='truncate font-medium'>{link.name}</span>
                    </div>
                    <span className='text-muted-foreground block truncate font-mono text-xs'>
                      {link.url}
                    </span>
                  </div>
                  <div className='flex gap-1'>
                    <Button
                      type='button'
                      variant='ghost'
                      size='sm'
                      onClick={(e) => {
                        e.preventDefault()
                        e.stopPropagation()
                        handleEdit(link)
                      }}
                    >
                      <Pencil className='h-4 w-4' />
                    </Button>
                    <Button
                      type='button'
                      variant='ghost'
                      size='sm'
                      onClick={(e) => {
                        e.preventDefault()
                        e.stopPropagation()
                        handleDelete(link)
                      }}
                    >
                      <Trash2 className='h-4 w-4' />
                    </Button>
                  </div>
                </div>
                <div className='flex items-center gap-2 text-sm'>
                  <span className='text-muted-foreground min-w-20'>
                    {t('Icon')}
                  </span>
                  {link.icon ? (
                    <div className='flex min-w-0 items-center gap-2'>
                      <ReactIconByName
                        name={link.icon}
                        className='text-muted-foreground size-5 shrink-0'
                        title={link.icon}
                      />
                      <span className='text-muted-foreground truncate font-mono text-xs'>
                        {link.icon}
                      </span>
                    </div>
                  ) : (
                    <span className='text-muted-foreground text-xs'>—</span>
                  )}
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      <ExternalLinkDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        onSave={handleSave}
        editData={editData}
        amountOptions={props.amountOptions}
      />
    </div>
  )
}
