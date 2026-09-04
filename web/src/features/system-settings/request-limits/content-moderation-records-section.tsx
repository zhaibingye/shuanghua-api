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
import {
  ChevronLeft,
  ChevronRight,
  RefreshCw,
  RotateCcw,
  Search,
  X,
} from 'lucide-react'
import { useCallback, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
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
import { cn } from '@/lib/utils'

import {
  getContentModerationConversation,
  listContentModerationConversations,
  resolveContentModerationViolation,
  restoreContentModerationUser,
  unblockContentModerationConversation,
} from '../api'
import { SettingsSection } from '../components/settings-section'
import type {
  ModerationConversation,
  ModerationConversationDetail,
} from '../types'

function displayTime(timestamp: number) {
  if (!timestamp) return '—'
  return new Date(timestamp * 1000).toLocaleString()
}

function formatModerationPayload(value: string) {
  try {
    return JSON.stringify(JSON.parse(value), null, 2)
  } catch {
    return value
  }
}

function statusLabel(status: string, t: (key: string) => string) {
  const labels: Record<string, string> = {
    active: 'Active',
    blocked: 'Blocked',
    resolved: 'Resolved',
    pending: 'Pending',
    running: 'Running',
    succeeded: 'Succeeded',
    failed: 'Failed',
    sent: 'Success',
    partial: 'Partial',
  }
  return t(labels[status] ?? status)
}

type ConversationRowProps = {
  conversation: ModerationConversation
  selected: boolean
  onSelect: () => void
}

function ConversationRow(props: ConversationRowProps) {
  const { t } = useTranslation()
  return (
    <div className='flex flex-col gap-3 rounded-xl border p-3 md:flex-row md:items-center md:justify-between'>
      <div className='min-w-0 space-y-1 text-sm'>
        <div className='flex flex-wrap items-center gap-2'>
          <span className='font-medium'>
            {t('User')} #{props.conversation.user_id}
          </span>
          <Badge
            variant={
              props.conversation.status === 'blocked'
                ? 'destructive'
                : 'secondary'
            }
          >
            {statusLabel(props.conversation.status, t)}
          </Badge>
        </div>
        <p className='text-muted-foreground truncate'>
          {t('Conversation')}: {props.conversation.conversation_id}
        </p>
        <p className='text-muted-foreground'>
          {t('Last activity')}:{' '}
          {displayTime(props.conversation.last_activity_at)}
        </p>
      </div>
      <Button
        type='button'
        variant={props.selected ? 'secondary' : 'outline'}
        onClick={props.onSelect}
      >
        {props.selected ? t('Selected') : t('Open details')}
      </Button>
    </div>
  )
}

type ConversationDetailProps = {
  detail: ModerationConversationDetail
  onRefresh: () => void
  onClose: () => void
}

export function ConversationDetail(props: ConversationDetailProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [actionReason, setActionReason] = useState('')
  const unblockMutation = useMutation({
    mutationFn: (reason: string) =>
      unblockContentModerationConversation(
        props.detail.conversation.id,
        reason
      ),
    onSuccess: async () => {
      setActionReason('')
      toast.success(t('Conversation unblocked'))
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: ['moderation-conversations'],
        }),
        queryClient.invalidateQueries({
          queryKey: ['moderation-conversation', props.detail.conversation.id],
        }),
      ])
      props.onRefresh()
    },
    onError: () => toast.error(t('Failed to update moderation record')),
  })
  const restoreMutation = useMutation({
    mutationFn: (reason: string) =>
      restoreContentModerationUser(props.detail.conversation.user_id, reason),
    onSuccess: async () => {
      setActionReason('')
      toast.success(t('Account restored'))
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: ['moderation-conversations'],
        }),
        queryClient.invalidateQueries({
          queryKey: ['moderation-conversation', props.detail.conversation.id],
        }),
      ])
      props.onRefresh()
    },
    onError: () => toast.error(t('Failed to update moderation record')),
  })
  const resolveMutation = useMutation({
    mutationFn: ({
      violationID,
      reason,
    }: {
      violationID: number
      reason: string
    }) =>
      resolveContentModerationViolation(violationID, 'false_positive', reason),
    onSuccess: async () => {
      setActionReason('')
      toast.success(t('Violation resolved'))
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: ['moderation-conversation', props.detail.conversation.id],
        }),
        queryClient.invalidateQueries({
          queryKey: ['moderation-conversations'],
        }),
      ])
      props.onRefresh()
    },
    onError: () => toast.error(t('Failed to update moderation record')),
  })

  return (
    <div className='bg-muted/20 space-y-5 rounded-xl border p-4'>
      <div className='flex flex-wrap items-start justify-between gap-2'>
        <div>
          <h4 className='font-semibold'>{t('Timeline')}</h4>
          <p className='text-muted-foreground text-sm'>
            {t('Conversation')} #{props.detail.conversation.id} ·{' '}
            {statusLabel(props.detail.conversation.status, t)}
          </p>
        </div>
        <div className='flex flex-wrap items-center gap-2'>
          <Button
            type='button'
            variant='ghost'
            size='icon-sm'
            onClick={props.onClose}
            aria-label={t('Close details')}
            title={t('Close details')}
          >
            <X />
          </Button>
          {props.detail.conversation.status === 'blocked' && (
            <Button
              type='button'
              variant='outline'
              onClick={() => unblockMutation.mutate(actionReason)}
              disabled={unblockMutation.isPending}
            >
              {t('Unblock conversation')}
            </Button>
          )}
          <Button
            type='button'
            variant='outline'
            onClick={() => restoreMutation.mutate(actionReason)}
            disabled={restoreMutation.isPending}
          >
            {t('Restore account')}
          </Button>
        </div>
      </div>
      <div className='space-y-2'>
        <label
          className='text-sm font-medium'
          htmlFor='moderation-action-reason'
        >
          {t('Reason')}
        </label>
        <Textarea
          id='moderation-action-reason'
          value={actionReason}
          maxLength={4096}
          onChange={(event) => setActionReason(event.target.value)}
          aria-label={t('Reason')}
        />
      </div>
      {props.detail.turns.map((turn) => (
        <article
          key={turn.id}
          className='bg-background space-y-3 rounded-lg border p-3'
        >
          <div className='text-muted-foreground flex flex-wrap gap-x-4 gap-y-1 text-xs'>
            <span>
              {t('Round')} {turn.round_number}
            </span>
            {turn.channel_id ? (
              <span>
                {t('Channel')} #{turn.channel_id}
              </span>
            ) : null}
            <span>{statusLabel(turn.response_status, t)}</span>
            <span>{turn.model}</span>
            <span>{displayTime(turn.created_at)}</span>
          </div>
          <div className='grid gap-3 text-sm'>
            <div className='overflow-hidden rounded-lg border border-sky-500/25 bg-sky-500/5'>
              <div className='border-b border-sky-500/20 px-3 py-2 font-medium text-sky-700 dark:text-sky-300'>
                {t('System prompt')}
              </div>
              <p className='max-h-56 overflow-auto px-3 py-3 leading-6 break-words whitespace-pre-wrap'>
                {turn.system_prompt || '—'}
              </p>
            </div>
            <div className='overflow-hidden rounded-lg border border-amber-500/25 bg-amber-500/5'>
              <div className='border-b border-amber-500/20 px-3 py-2 font-medium text-amber-700 dark:text-amber-300'>
                {t('User prompt')}
              </div>
              <p className='max-h-72 overflow-auto px-3 py-3 leading-6 break-words whitespace-pre-wrap'>
                {turn.user_prompt || '—'}
              </p>
            </div>
            <div className='overflow-hidden rounded-lg border border-emerald-500/25 bg-emerald-500/5'>
              <div className='border-b border-emerald-500/20 px-3 py-2 font-medium text-emerald-700 dark:text-emerald-300'>
                {t('Assistant response')}
              </div>
              <p className='max-h-72 overflow-auto px-3 py-3 leading-7 break-words whitespace-pre-wrap'>
                {turn.assistant_reply || '—'}
              </p>
            </div>
          </div>
        </article>
      ))}
      <div className='space-y-2'>
        <h4 className='font-semibold'>{t('Review jobs')}</h4>
        {props.detail.jobs.map((job) => (
          <div
            key={job.id}
            className='text-muted-foreground rounded-lg border p-3 text-sm'
          >
            <div className='flex flex-wrap gap-3'>
              <span>{statusLabel(job.status, t)}</span>
              <span>
                {t('Attempts')}: {job.attempts}
              </span>
              <span>
                {job.provider} / {job.model}
              </span>
              <span>{job.prompt_version}</span>
            </div>
            {job.last_error && (
              <p className='text-destructive mt-2'>{job.last_error}</p>
            )}
            {job.response_payload && (
              <pre className='bg-muted mt-2 max-h-64 overflow-auto rounded p-3 font-mono text-xs leading-5 break-words whitespace-pre-wrap'>
                {formatModerationPayload(job.response_payload)}
              </pre>
            )}
          </div>
        ))}
      </div>
      <div className='space-y-2'>
        <h4 className='font-semibold'>{t('Violations')}</h4>
        {props.detail.violations.length === 0 && (
          <p className='text-muted-foreground text-sm'>{t('No violations')}</p>
        )}
        {props.detail.violations.map((violation) => (
          <div
            key={violation.id}
            className='flex flex-col gap-2 rounded-lg border p-3 text-sm md:flex-row md:items-center md:justify-between'
          >
            <div>
              <p>
                {violation.actor} · {violation.severity} ·{' '}
                {violation.reason_code}
              </p>
              <p className='text-muted-foreground'>
                {violation.categories} ·{' '}
                {Math.round(violation.confidence * 100)}%
              </p>
            </div>
            {violation.status === 'active' && (
              <Button
                type='button'
                size='sm'
                variant='outline'
                onClick={() =>
                  resolveMutation.mutate({
                    violationID: violation.id,
                    reason: actionReason,
                  })
                }
                disabled={resolveMutation.isPending}
              >
                {t('Resolve as false positive')}
              </Button>
            )}
          </div>
        ))}
      </div>
      <div className='space-y-2'>
        <h4 className='font-semibold'>{t('Notifications')}</h4>
        {props.detail.notifications.length === 0 && (
          <p className='text-muted-foreground text-sm'>{t('No data')}</p>
        )}
        {props.detail.notifications.map((notification) => (
          <div key={notification.id} className='rounded-lg border p-3 text-sm'>
            <div className='flex flex-wrap gap-3'>
              <span>{notification.alert_type}</span>
              <span>{statusLabel(notification.status, t)}</span>
              <span>
                {t('Email')}: {notification.recipient}
              </span>
              <span>
                {t('Attempts')}: {notification.attempts}
              </span>
            </div>
            {notification.last_error && (
              <p className='text-destructive mt-1 break-words whitespace-pre-wrap'>
                {t('Error')}: {notification.last_error}
              </p>
            )}
          </div>
        ))}
      </div>
      <div className='space-y-2'>
        <h4 className='font-semibold'>{t('Actions')}</h4>
        {props.detail.actions.length === 0 && (
          <p className='text-muted-foreground text-sm'>{t('No data')}</p>
        )}
        {props.detail.actions.map((action) => (
          <div key={action.id} className='rounded-lg border p-3 text-sm'>
            <div className='flex flex-wrap gap-3'>
              <span>{action.action}</span>
              <span>
                {t('Admin')} #{action.admin_id}
              </span>
              <span>{displayTime(action.created_at)}</span>
            </div>
            {action.reason && (
              <p className='text-muted-foreground mt-1 break-words whitespace-pre-wrap'>
                {t('Reason')}: {action.reason}
              </p>
            )}
          </div>
        ))}
      </div>
    </div>
  )
}

type ModerationFilterState = {
  userId: string
  conversationId: string
  status: string
  startTime?: Date
  endTime?: Date
}

const initialFilters: ModerationFilterState = {
  userId: '',
  conversationId: '',
  status: 'all',
  startTime: undefined,
  endTime: undefined,
}

export function ContentModerationRecordsSection() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
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
      conversation_id?: string
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
    if (appliedFilters.conversationId.trim()) {
      params.conversation_id = appliedFilters.conversationId.trim()
    }
    if (appliedFilters.startTime) {
      params.start_timestamp = Math.floor(
        appliedFilters.startTime.getTime() / 1000
      )
    }
    if (appliedFilters.endTime) {
      params.end_timestamp = Math.floor(appliedFilters.endTime.getTime() / 1000)
    }
    return params
  }, [appliedFilters, page, pageSize])

  const query = useQuery({
    queryKey: ['moderation-conversations', queryParams],
    queryFn: () => listContentModerationConversations(queryParams),
  })
  const detailQuery = useQuery({
    queryKey: ['moderation-conversation', selectedID],
    queryFn: () => getContentModerationConversation(selectedID as number),
    enabled: selectedID !== null,
  })

  const hasActiveFilters =
    appliedFilters.userId.trim() !== '' ||
    appliedFilters.conversationId.trim() !== '' ||
    appliedFilters.status !== 'all' ||
    appliedFilters.startTime !== undefined ||
    appliedFilters.endTime !== undefined

  const handleSearch = useCallback(() => {
    setAppliedFilters(draftFilters)
    setPage(1)
  }, [draftFilters])

  const handleReset = useCallback(() => {
    setDraftFilters(initialFilters)
    setAppliedFilters(initialFilters)
    setPage(1)
  }, [])

  const handleRefresh = useCallback(() => {
    queryClient.invalidateQueries({ queryKey: ['moderation-conversations'] })
  }, [queryClient])

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent<HTMLInputElement>) => {
      if (e.key === 'Enter') {
        handleSearch()
      }
    },
    [handleSearch]
  )

  const handleRemoveFilter = useCallback(
    (key: keyof ModerationFilterState | 'timeRange') => {
      if (key === 'timeRange') {
        setDraftFilters((prev) => ({
          ...prev,
          startTime: undefined,
          endTime: undefined,
        }))
        setAppliedFilters((prev) => ({
          ...prev,
          startTime: undefined,
          endTime: undefined,
        }))
      } else {
        const defaultVal = key === 'status' ? 'all' : ''
        setDraftFilters((prev) => ({ ...prev, [key]: defaultVal }))
        setAppliedFilters((prev) => ({ ...prev, [key]: defaultVal }))
      }
      setPage(1)
    },
    []
  )

  const total = query.data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / pageSize))

  return (
    <SettingsSection title={t('Moderation Records')}>
      <div className='flex items-center justify-between gap-3'>
        <p className='text-muted-foreground text-sm'>
          {t(
            'Review retained moderation timelines here. Marking a result as a false positive keeps the original record but excludes it from the future violation count; it does not automatically restore an account.'
          )}
        </p>
      </div>

      {/* Quick Filter Bar */}
      <div className='bg-card/50 space-y-3 rounded-xl border p-3.5'>
        <div className='grid grid-cols-1 gap-2.5 sm:grid-cols-2 lg:grid-cols-4'>
          <CompactDateTimeRangePicker
            start={draftFilters.startTime}
            end={draftFilters.endTime}
            onChange={({ start, end }) =>
              setDraftFilters((prev) => ({
                ...prev,
                startTime: start,
                endTime: end,
              }))
            }
          />

          <Select
            value={draftFilters.status}
            onValueChange={(val) =>
              setDraftFilters((prev) => ({ ...prev, status: val ?? 'all' }))
            }
          >
            <SelectTrigger className='h-8 text-sm'>
              <SelectValue placeholder={t('All Status')} />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                <SelectItem value='all'>{t('All Status')}</SelectItem>
                <SelectItem value='active'>{t('Active')}</SelectItem>
                <SelectItem value='blocked'>{t('Blocked')}</SelectItem>
                <SelectItem value='resolved'>{t('Resolved')}</SelectItem>
              </SelectGroup>
            </SelectContent>
          </Select>

          <Input
            type='number'
            min={1}
            placeholder={t('User ID')}
            value={draftFilters.userId}
            onChange={(e) =>
              setDraftFilters((prev) => ({ ...prev, userId: e.target.value }))
            }
            onKeyDown={handleKeyDown}
            className='h-8 text-sm'
          />

          <Input
            placeholder={t('Conversation ID')}
            value={draftFilters.conversationId}
            onChange={(e) =>
              setDraftFilters((prev) => ({
                ...prev,
                conversationId: e.target.value,
              }))
            }
            onKeyDown={handleKeyDown}
            className='h-8 text-sm'
          />
        </div>

        {/* Filter actions and stats row */}
        <div className='flex flex-wrap items-center justify-between gap-2 border-t pt-1'>
          <div className='flex flex-wrap items-center gap-2'>
            <Button
              type='button'
              size='sm'
              onClick={handleSearch}
              disabled={query.isFetching}
              className='h-8 px-3'
            >
              <Search className='mr-1.5 size-3.5' />
              <span>{t('Search')}</span>
            </Button>
            <Button
              type='button'
              variant='outline'
              size='sm'
              onClick={handleReset}
              disabled={!hasActiveFilters}
              className='h-8 px-3'
            >
              <RotateCcw className='mr-1.5 size-3.5' />
              <span>{t('Reset')}</span>
            </Button>
            <Button
              type='button'
              variant='outline'
              size='sm'
              onClick={handleRefresh}
              disabled={query.isFetching}
              className='h-8 px-3'
            >
              <RefreshCw
                className={cn(
                  'size-3.5 mr-1.5',
                  query.isFetching && 'animate-spin'
                )}
              />
              <span>{t('Refresh')}</span>
            </Button>
          </div>

          <div className='text-muted-foreground text-xs'>
            {t('Total Count')}: {total}
          </div>
        </div>

        {/* Active filter badges */}
        {hasActiveFilters && (
          <div className='flex flex-wrap items-center gap-1.5 pt-1'>
            {appliedFilters.status !== 'all' && (
              <Badge variant='secondary' className='gap-1 text-xs font-normal'>
                <span>
                  {t('Status')}: {statusLabel(appliedFilters.status, t)}
                </span>
                <X
                  className='hover:text-foreground size-3 cursor-pointer'
                  onClick={() => handleRemoveFilter('status')}
                />
              </Badge>
            )}
            {appliedFilters.userId.trim() !== '' && (
              <Badge variant='secondary' className='gap-1 text-xs font-normal'>
                <span>
                  {t('User')}: #{appliedFilters.userId}
                </span>
                <X
                  className='hover:text-foreground size-3 cursor-pointer'
                  onClick={() => handleRemoveFilter('userId')}
                />
              </Badge>
            )}
            {appliedFilters.conversationId.trim() !== '' && (
              <Badge variant='secondary' className='gap-1 text-xs font-normal'>
                <span>
                  {t('Conversation')}: {appliedFilters.conversationId}
                </span>
                <X
                  className='hover:text-foreground size-3 cursor-pointer'
                  onClick={() => handleRemoveFilter('conversationId')}
                />
              </Badge>
            )}
            {(appliedFilters.startTime || appliedFilters.endTime) && (
              <Badge variant='secondary' className='gap-1 text-xs font-normal'>
                <span>{t('Date Range')}</span>
                <X
                  className='hover:text-foreground size-3 cursor-pointer'
                  onClick={() => handleRemoveFilter('timeRange')}
                />
              </Badge>
            )}
            <Button
              type='button'
              variant='ghost'
              size='sm'
              onClick={handleReset}
              className='text-muted-foreground hover:text-foreground h-6 px-2 text-xs'
            >
              {t('Clear all')}
            </Button>
          </div>
        )}
      </div>

      {query.isLoading && (
        <p className='text-muted-foreground text-sm'>
          {t('Loading moderation records...')}
        </p>
      )}
      {!query.isLoading && query.data?.data.length === 0 && (
        <p className='text-muted-foreground text-sm'>
          {t('No moderation conversations found.')}
        </p>
      )}
      <div className='space-y-3'>
        {query.data?.data.map((conversation) => (
          <ConversationRow
            key={conversation.id}
            conversation={conversation}
            selected={conversation.id === selectedID}
            onSelect={() => setSelectedID(conversation.id)}
          />
        ))}
      </div>

      {/* Pagination controls */}
      {total > 0 && (
        <div className='flex flex-wrap items-center justify-between gap-3 pt-2 text-sm'>
          <div className='text-muted-foreground flex items-center gap-2 text-xs'>
            <span>{t('Rows per page')}</span>
            <Select
              value={String(pageSize)}
              onValueChange={(val) => {
                if (val) {
                  setPageSize(Number(val))
                  setPage(1)
                }
              }}
            >
              <SelectTrigger className='h-8 w-[72px] text-xs'>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value='10'>10</SelectItem>
                <SelectItem value='20'>20</SelectItem>
                <SelectItem value='50'>50</SelectItem>
              </SelectContent>
            </Select>
          </div>
          {totalPages > 1 && (
            <div className='flex items-center gap-2'>
              <span className='text-muted-foreground text-xs'>
                {page} / {totalPages}
              </span>
              <div className='flex items-center gap-1'>
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  className='h-8 px-2.5'
                  onClick={() => setPage((p) => Math.max(1, p - 1))}
                  disabled={page <= 1 || query.isFetching}
                >
                  <ChevronLeft className='size-4' />
                  <span className='sr-only'>{t('Go to previous page')}</span>
                </Button>
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  className='h-8 px-2.5'
                  onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
                  disabled={page >= totalPages || query.isFetching}
                >
                  <ChevronRight className='size-4' />
                  <span className='sr-only'>{t('Go to next page')}</span>
                </Button>
              </div>
            </div>
          )}
        </div>
      )}

      {detailQuery.isLoading && (
        <p className='text-muted-foreground text-sm'>
          {t('Loading conversation details...')}
        </p>
      )}
      {detailQuery.data?.data && (
        <ConversationDetail
          detail={detailQuery.data.data}
          onRefresh={() => query.refetch()}
          onClose={() => setSelectedID(null)}
        />
      )}
    </SettingsSection>
  )
}
