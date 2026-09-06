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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { X } from 'lucide-react'
import { useCallback, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { DataTablePaginationControls } from '@/components/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { CompactDateTimeRangePicker } from '@/features/usage-logs/components/compact-date-time-range-picker'
import {
  LogsFilterField,
  LogsFilterInput,
  LogsFilterToolbar,
} from '@/features/usage-logs/components/logs-filter-toolbar'

import {
  listContentModerationEvents,
  resolveContentModerationEvent,
  restoreContentModerationUser,
} from '../api'
import { SettingsSection } from '../components/settings-section'
import type { ModerationEvent } from '../types'

function displayTime(timestamp: number) {
  if (!timestamp) return '—'
  return new Date(timestamp * 1000).toLocaleString()
}

function parseCategories(raw: string) {
  try {
    const parsed: unknown = JSON.parse(raw)
    if (Array.isArray(parsed)) {
      return parsed.filter((item): item is string => typeof item === 'string')
    }
  } catch {
    // keep the original string when it is not JSON
  }
  return raw ? [raw] : []
}

function statusLabel(status: string, t: (key: string) => string) {
  const labels: Record<string, string> = {
    active: 'Active',
    false_positive: 'False positive',
    reversed: 'Reversed',
  }
  return t(labels[status] ?? status)
}

function sourceLabel(source: string, t: (key: string) => string) {
  const labels: Record<string, string> = {
    preflight: 'User request',
    postflight: 'Assistant reply',
  }
  return t(labels[source] ?? source)
}

type EventRowProps = {
  event: ModerationEvent
  selected: boolean
  onSelect: () => void
}

function EventRow(props: EventRowProps) {
  const { t } = useTranslation()
  const excerpt = props.event.user_excerpt || props.event.assistant_excerpt
  return (
    <button
      type='button'
      className='hover:bg-muted/50 flex w-full flex-col gap-3 rounded-xl border p-3 text-left transition-colors md:flex-row md:items-center md:justify-between'
      data-selected={props.selected ? 'true' : undefined}
      onClick={props.onSelect}
    >
      <div className='min-w-0 space-y-1 text-sm'>
        <div className='flex flex-wrap items-center gap-2'>
          <span className='font-medium'>
            {t('User')} #{props.event.user_id}
          </span>
          <Badge
            variant={
              props.event.status === 'active' ? 'destructive' : 'secondary'
            }
          >
            {statusLabel(props.event.status, t)}
          </Badge>
          <Badge variant='outline'>{sourceLabel(props.event.source, t)}</Badge>
          {props.event.severity && (
            <Badge variant='outline'>{props.event.severity}</Badge>
          )}
        </div>
        <p className='text-muted-foreground line-clamp-2 text-xs'>
          {excerpt || t('No excerpt saved')}
        </p>
      </div>
      <span className='text-muted-foreground shrink-0 text-xs'>
        {displayTime(props.event.created_at)}
      </span>
    </button>
  )
}

type EventDetailProps = {
  event: ModerationEvent
  onRefresh: () => void
  onClose: () => void
}

export function EventDetail(props: EventDetailProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [reason, setReason] = useState('')
  const categories = parseCategories(props.event.categories)

  const invalidate = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ['moderation-events'] }),
      queryClient.invalidateQueries({ queryKey: ['moderation-users'] }),
    ])
    props.onRefresh()
  }

  const restoreMutation = useMutation({
    mutationFn: (restoreReason: string) =>
      restoreContentModerationUser(props.event.user_id, restoreReason),
    onSuccess: async () => {
      toast.success(t('Account restore requested'))
      await invalidate()
    },
    onError: () => toast.error(t('Failed to restore account')),
  })

  const resolveMutation = useMutation({
    mutationFn: (status: 'false_positive' | 'reversed') =>
      resolveContentModerationEvent(props.event.id, status, reason),
    onSuccess: async () => {
      setReason('')
      toast.success(t('Moderation event updated'))
      await invalidate()
    },
    onError: () => toast.error(t('Failed to update moderation event')),
  })

  return (
    <div className='space-y-4 rounded-xl border p-4'>
      <div className='flex flex-wrap items-start justify-between gap-3'>
        <div>
          <h3 className='font-semibold'>
            {t('Violation event')} #{props.event.id} ·{' '}
            {statusLabel(props.event.status, t)}
          </h3>
          <p className='text-muted-foreground text-sm'>
            {t('User')} #{props.event.user_id} ·{' '}
            {sourceLabel(props.event.source, t)}
            {props.event.model ? ` · ${props.event.model}` : ''}
          </p>
        </div>
        <div className='flex flex-wrap items-center gap-2'>
          <Button
            type='button'
            variant='outline'
            size='sm'
            onClick={() =>
              restoreMutation.mutate(reason || t('Restored from event inbox'))
            }
            disabled={restoreMutation.isPending}
          >
            {t('Restore account')}
          </Button>
          <Button
            type='button'
            variant='ghost'
            size='icon-sm'
            onClick={props.onClose}
            aria-label={t('Close')}
          >
            <X className='h-4 w-4' />
          </Button>
        </div>
      </div>

      <div className='grid gap-3 sm:grid-cols-2'>
        <div className='bg-card rounded-lg border p-3 text-sm'>
          <p className='text-muted-foreground mb-1 text-xs'>
            {t('User excerpt')}
          </p>
          <p className='whitespace-pre-wrap'>
            {props.event.user_excerpt || t('No excerpt saved')}
          </p>
        </div>
        <div className='bg-card rounded-lg border p-3 text-sm'>
          <p className='text-muted-foreground mb-1 text-xs'>
            {t('Assistant excerpt')}
          </p>
          <p className='whitespace-pre-wrap'>
            {props.event.assistant_excerpt || t('No excerpt saved')}
          </p>
        </div>
      </div>

      <div className='flex flex-wrap gap-2 text-xs'>
        {categories.map((category) => (
          <Badge key={category} variant='outline'>
            {category}
          </Badge>
        ))}
        {props.event.reason_code && (
          <Badge variant='secondary'>{props.event.reason_code}</Badge>
        )}
        {props.event.image_count > 0 && (
          <Badge variant='outline'>
            {t('Images')}: {props.event.image_count}
          </Badge>
        )}
        <span className='text-muted-foreground'>
          {Math.round(props.event.confidence * 100)}% ·{' '}
          {displayTime(props.event.created_at)}
        </span>
      </div>

      {props.event.status === 'active' && (
        <div className='space-y-2'>
          <Textarea
            value={reason}
            maxLength={4096}
            onChange={(event) => setReason(event.target.value)}
            placeholder={t('Reason for this review action')}
          />
          <div className='flex flex-wrap gap-2'>
            <Button
              type='button'
              variant='outline'
              size='sm'
              onClick={() => resolveMutation.mutate('false_positive')}
              disabled={resolveMutation.isPending || reason.trim().length === 0}
            >
              {t('Mark as false positive')}
            </Button>
            <Button
              type='button'
              variant='outline'
              size='sm'
              onClick={() => resolveMutation.mutate('reversed')}
              disabled={resolveMutation.isPending || reason.trim().length === 0}
            >
              {t('Reverse this decision')}
            </Button>
          </div>
        </div>
      )}
      {props.event.resolution_note && (
        <p className='text-muted-foreground text-xs'>
          {t('Resolution note')}: {props.event.resolution_note}
        </p>
      )}
    </div>
  )
}

type ModerationFilterState = {
  userId: string
  status: string
  source: string
  start?: Date
  end?: Date
}

const initialFilters: ModerationFilterState = {
  userId: '',
  status: 'all',
  source: 'all',
  start: undefined,
  end: undefined,
}

export function ContentModerationRecordsSection() {
  const { t } = useTranslation()
  const [selectedID, setSelectedID] = useState<number | null>(null)
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(20)
  const [draftFilters, setDraftFilters] =
    useState<ModerationFilterState>(initialFilters)
  const [appliedFilters, setAppliedFilters] =
    useState<ModerationFilterState>(initialFilters)

  const queryParams = useMemo(() => {
    const params: {
      user_id?: number
      status?: string
      source?: string
      start_timestamp?: number
      end_timestamp?: number
      limit: number
      offset: number
    } = {
      limit: pageSize,
      offset: (page - 1) * pageSize,
    }
    const uid = Number.parseInt(appliedFilters.userId.trim(), 10)
    if (!Number.isNaN(uid) && uid > 0) {
      params.user_id = uid
    }
    if (appliedFilters.status && appliedFilters.status !== 'all') {
      params.status = appliedFilters.status
    }
    if (appliedFilters.source && appliedFilters.source !== 'all') {
      params.source = appliedFilters.source
    }
    if (appliedFilters.start) {
      params.start_timestamp = Math.floor(appliedFilters.start.getTime() / 1000)
    }
    if (appliedFilters.end) {
      params.end_timestamp = Math.floor(appliedFilters.end.getTime() / 1000)
    }
    return params
  }, [appliedFilters, page, pageSize])

  const query = useQuery({
    queryKey: ['moderation-events', queryParams],
    queryFn: () => listContentModerationEvents(queryParams),
  })

  const selectedEvent = query.data?.data.find(
    (event) => event.id === selectedID
  )
  const total = query.data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / pageSize))
  const hasActiveFilters =
    appliedFilters.status !== 'all' ||
    appliedFilters.source !== 'all' ||
    appliedFilters.userId.trim() !== '' ||
    appliedFilters.start !== undefined ||
    appliedFilters.end !== undefined

  const handleSearch = useCallback(() => {
    setAppliedFilters(draftFilters)
    setPage(1)
  }, [draftFilters])
  const handleReset = useCallback(() => {
    setDraftFilters(initialFilters)
    setAppliedFilters(initialFilters)
    setPage(1)
  }, [])
  const handleKeyDown = (event: { key: string }) => {
    if (event.key === 'Enter') {
      handleSearch()
    }
  }

  const dateRangeFilter = (
    <LogsFilterField>
      <CompactDateTimeRangePicker
        start={draftFilters.start}
        end={draftFilters.end}
        onChange={(range) =>
          setDraftFilters((current) => ({
            ...current,
            start: range.start,
            end: range.end,
          }))
        }
      />
    </LogsFilterField>
  )
  const statusFilter = (
    <LogsFilterField>
      <Select
        items={[
          { value: 'all', label: t('All Status') },
          { value: 'active', label: t('Active') },
          { value: 'false_positive', label: t('False positive') },
          { value: 'reversed', label: t('Reversed') },
        ]}
        value={draftFilters.status}
        onValueChange={(value) =>
          setDraftFilters((current) => ({
            ...current,
            status: value ?? 'all',
          }))
        }
      >
        <SelectTrigger>
          <SelectValue placeholder={t('All Status')} />
        </SelectTrigger>
        <SelectContent alignItemWithTrigger={false}>
          <SelectGroup>
            <SelectItem value='all'>{t('All Status')}</SelectItem>
            <SelectItem value='active'>{t('Active')}</SelectItem>
            <SelectItem value='false_positive'>
              {t('False positive')}
            </SelectItem>
            <SelectItem value='reversed'>{t('Reversed')}</SelectItem>
          </SelectGroup>
        </SelectContent>
      </Select>
    </LogsFilterField>
  )
  const sourceFilter = (
    <LogsFilterField>
      <Select
        items={[
          { value: 'all', label: t('All sources') },
          { value: 'preflight', label: t('User request') },
          { value: 'postflight', label: t('Assistant reply') },
        ]}
        value={draftFilters.source}
        onValueChange={(value) =>
          setDraftFilters((current) => ({
            ...current,
            source: value ?? 'all',
          }))
        }
      >
        <SelectTrigger>
          <SelectValue placeholder={t('All sources')} />
        </SelectTrigger>
        <SelectContent alignItemWithTrigger={false}>
          <SelectGroup>
            <SelectItem value='all'>{t('All sources')}</SelectItem>
            <SelectItem value='preflight'>{t('User request')}</SelectItem>
            <SelectItem value='postflight'>{t('Assistant reply')}</SelectItem>
          </SelectGroup>
        </SelectContent>
      </Select>
    </LogsFilterField>
  )
  const userIDFilter = (
    <LogsFilterField>
      <LogsFilterInput
        type='number'
        min={1}
        placeholder={t('User ID')}
        value={draftFilters.userId}
        onChange={(event) =>
          setDraftFilters((current) => ({
            ...current,
            userId: event.target.value,
          }))
        }
        onKeyDown={handleKeyDown}
      />
    </LogsFilterField>
  )
  const stats = (
    <div className='flex flex-wrap items-center gap-2 text-xs font-medium sm:text-sm'>
      <span className='text-muted-foreground/80'>{t('Total:')}</span>
      <span className='text-foreground tabular-nums'>
        {total.toLocaleString()}
      </span>
    </div>
  )

  return (
    <SettingsSection title={t('Moderation Records')}>
      <div className='flex items-center justify-between gap-3'>
        <p className='text-muted-foreground text-sm'>
          {t(
            'Only flagged requests are stored here as truncated excerpts. Marking a result as a false positive keeps the original record but excludes it from the future violation count; it does not automatically restore an account.'
          )}
        </p>
      </div>

      <LogsFilterToolbar
        stats={stats}
        primaryFilters={
          <>
            {dateRangeFilter}
            {statusFilter}
            {sourceFilter}
            {userIDFilter}
          </>
        }
        mobilePinnedFilters={dateRangeFilter}
        mobileFilters={
          <>
            {statusFilter}
            {sourceFilter}
            {userIDFilter}
          </>
        }
        mobileFilterCount={
          [
            appliedFilters.status !== 'all' ? appliedFilters.status : undefined,
            appliedFilters.source !== 'all' ? appliedFilters.source : undefined,
            appliedFilters.userId.trim(),
          ].filter(Boolean).length
        }
        hasActiveFilters={hasActiveFilters}
        onSearch={handleSearch}
        searchLoading={query.isFetching}
        onReset={handleReset}
      />

      {query.isLoading && (
        <p className='text-muted-foreground text-sm'>
          {t('Loading moderation records...')}
        </p>
      )}
      {!query.isLoading && query.data?.data.length === 0 && (
        <p className='text-muted-foreground text-sm'>
          {t('No moderation events found.')}
        </p>
      )}
      <div className='space-y-3'>
        {query.data?.data.map((event) => (
          <EventRow
            key={event.id}
            event={event}
            selected={event.id === selectedID}
            onSelect={() => setSelectedID(event.id)}
          />
        ))}
      </div>

      <div className='pt-2'>
        <DataTablePaginationControls
          currentPage={page}
          totalPages={totalPages}
          pageSize={pageSize}
          totalRows={total}
          onPageChange={setPage}
          onPageSizeChange={(nextPageSize) => {
            setPageSize(nextPageSize)
            setPage(1)
          }}
        />
      </div>

      {selectedEvent && (
        <EventDetail
          event={selectedEvent}
          onRefresh={() => {
            void query.refetch()
          }}
          onClose={() => setSelectedID(null)}
        />
      )}
    </SettingsSection>
  )
}
