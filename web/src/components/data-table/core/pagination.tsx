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
import type { Table } from '@tanstack/react-table'
import {
  ChevronLeft as ChevronLeftIcon,
  ChevronRight as ChevronRightIcon,
  ChevronsLeft as DoubleArrowLeftIcon,
  ChevronsRight as DoubleArrowRightIcon,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { cn, getPageNumbers } from '@/lib/utils'

type DataTablePaginationProps<TData> = {
  table: Table<TData>
}

const PAGE_SIZE_OPTIONS = [10, 20, 30, 40, 50, 100] as const
const PAGE_SIZE_SELECT_ITEMS = PAGE_SIZE_OPTIONS.map((pageSize) => ({
  value: `${pageSize}`,
  label: pageSize,
}))

export type DataTablePaginationControlsProps = {
  currentPage: number
  totalPages: number
  pageSize: number
  totalRows: number
  onPageChange: (page: number) => void
  onPageSizeChange: (pageSize: number) => void
}

export function DataTablePaginationControls(
  props: DataTablePaginationControlsProps
) {
  const { t } = useTranslation()
  const totalPages = Math.max(1, props.totalPages)
  const currentPage = Math.max(1, Math.min(props.currentPage, totalPages))
  const pageNumbers = getPageNumbers(currentPage, totalPages)
  const pageNumberOccurrences = new Map<string, number>()

  return (
    <div
      className={cn(
        '@container/pagination flex min-w-0 items-center justify-end overflow-clip'
      )}
      style={{ overflowClipMargin: 1 }}
    >
      <div className='flex min-w-0 shrink-0 items-center gap-2 @xl/pagination:gap-3'>
        <div className='flex shrink-0 items-baseline gap-1.5 text-xs font-medium whitespace-nowrap sm:text-sm'>
          <span className='text-muted-foreground/80'>{t('Total:')}</span>
          <span className='text-foreground tabular-nums'>
            {props.totalRows.toLocaleString()}
          </span>
        </div>

        <div className='flex shrink-0 items-center gap-1.5 @lg/pagination:gap-2'>
          <p className='text-muted-foreground/80 hidden text-sm font-medium whitespace-nowrap @2xl/pagination:block'>
            {t('Rows per page')}
          </p>
          <Select
            items={PAGE_SIZE_SELECT_ITEMS}
            value={`${props.pageSize}`}
            onValueChange={(value) => {
              const nextPageSize = Number(value)
              if (Number.isFinite(nextPageSize)) {
                props.onPageSizeChange(nextPageSize)
              }
            }}
          >
            <SelectTrigger className='text-foreground h-8 w-[64px] font-medium tabular-nums sm:w-[70px]'>
              <SelectValue placeholder={props.pageSize} />
            </SelectTrigger>
            <SelectContent side='top' alignItemWithTrigger={false}>
              <SelectGroup>
                {PAGE_SIZE_OPTIONS.map((pageSize) => (
                  <SelectItem key={pageSize} value={`${pageSize}`}>
                    {pageSize}
                  </SelectItem>
                ))}
              </SelectGroup>
            </SelectContent>
          </Select>
        </div>

        <div className='flex min-w-0 shrink-0 items-center gap-1 @lg/pagination:gap-1.5 @xl/pagination:gap-2'>
          <Button
            type='button'
            variant='outline'
            className='text-muted-foreground hover:text-foreground disabled:text-muted-foreground/50 size-8 p-0 @max-lg/pagination:hidden'
            onClick={() => props.onPageChange(1)}
            disabled={currentPage <= 1}
          >
            <span className='sr-only'>{t('Go to first page')}</span>
            <DoubleArrowLeftIcon className='h-4 w-4' />
          </Button>
          <Button
            type='button'
            variant='outline'
            className='text-muted-foreground hover:text-foreground disabled:text-muted-foreground/50 size-8 p-0'
            onClick={() => props.onPageChange(currentPage - 1)}
            disabled={currentPage <= 1}
          >
            <span className='sr-only'>{t('Go to previous page')}</span>
            <ChevronLeftIcon className='h-4 w-4' />
          </Button>

          {pageNumbers.map((pageNumber) => {
            const pageNumberKey = String(pageNumber)
            const occurrence = pageNumberOccurrences.get(pageNumberKey) ?? 0
            pageNumberOccurrences.set(pageNumberKey, occurrence + 1)
            return (
              <div
                key={`${pageNumberKey}-${occurrence}`}
                className='flex items-center'
              >
                {pageNumber === '...' ? (
                  <span className='text-muted-foreground/60 px-0.5 text-sm @lg/pagination:px-1'>
                    ...
                  </span>
                ) : (
                  <Button
                    type='button'
                    aria-label={t('Go to page {{page}}', { page: pageNumber })}
                    variant={currentPage === pageNumber ? 'default' : 'outline'}
                    className={cn(
                      'h-8 min-w-8 px-2 tabular-nums',
                      currentPage === pageNumber
                        ? 'font-semibold'
                        : 'text-muted-foreground hover:text-foreground'
                    )}
                    onClick={() => props.onPageChange(pageNumber as number)}
                  >
                    {pageNumber}
                  </Button>
                )}
              </div>
            )
          })}

          <Button
            type='button'
            variant='outline'
            className='text-muted-foreground hover:text-foreground disabled:text-muted-foreground/50 size-8 p-0'
            onClick={() => props.onPageChange(currentPage + 1)}
            disabled={currentPage >= totalPages}
          >
            <span className='sr-only'>{t('Go to next page')}</span>
            <ChevronRightIcon className='h-4 w-4' />
          </Button>
          <Button
            type='button'
            variant='outline'
            className='text-muted-foreground hover:text-foreground disabled:text-muted-foreground/50 size-8 p-0 @max-lg/pagination:hidden'
            onClick={() => props.onPageChange(totalPages)}
            disabled={currentPage >= totalPages}
          >
            <span className='sr-only'>{t('Go to last page')}</span>
            <DoubleArrowRightIcon className='h-4 w-4' />
          </Button>
        </div>
      </div>
    </div>
  )
}

export function DataTablePagination<TData>({
  table,
}: DataTablePaginationProps<TData>) {
  const pagination = table.getState().pagination

  return (
    <DataTablePaginationControls
      currentPage={pagination.pageIndex + 1}
      totalPages={table.getPageCount()}
      pageSize={pagination.pageSize}
      totalRows={table.getRowCount()}
      onPageChange={(page) => table.setPageIndex(page - 1)}
      onPageSizeChange={(pageSize) => table.setPageSize(pageSize)}
    />
  )
}
