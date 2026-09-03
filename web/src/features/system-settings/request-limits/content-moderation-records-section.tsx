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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'

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
}

function ConversationDetail(props: ConversationDetailProps) {
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
    onSuccess: () => {
      setActionReason('')
      toast.success(t('Account restored'))
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
      await queryClient.invalidateQueries({
        queryKey: ['moderation-conversation', props.detail.conversation.id],
      })
    },
    onError: () => toast.error(t('Failed to update moderation record')),
  })

  return (
    <div className='bg-muted/20 space-y-5 rounded-xl border p-4'>
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <div>
          <h4 className='font-semibold'>{t('Timeline')}</h4>
          <p className='text-muted-foreground text-sm'>
            {t('Conversation')} #{props.detail.conversation.id} ·{' '}
            {statusLabel(props.detail.conversation.status, t)}
          </p>
        </div>
        <div className='flex flex-wrap gap-2'>
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
            <span>{statusLabel(turn.response_status, t)}</span>
            <span>{turn.model}</span>
            <span>{displayTime(turn.created_at)}</span>
          </div>
          <div className='space-y-2 text-sm'>
            <div>
              <p className='font-medium'>{t('System prompt')}</p>
              <p className='break-words whitespace-pre-wrap'>
                {turn.system_prompt || '—'}
              </p>
            </div>
            <div>
              <p className='font-medium'>{t('User prompt')}</p>
              <p className='break-words whitespace-pre-wrap'>
                {turn.user_prompt || '—'}
              </p>
            </div>
            <div>
              <p className='font-medium'>{t('Assistant response')}</p>
              <p className='break-words whitespace-pre-wrap'>
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
              <pre className='bg-muted mt-2 max-h-48 overflow-auto rounded p-2 text-xs break-words whitespace-pre-wrap'>
                {job.response_payload}
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

export function ContentModerationRecordsSection() {
  const { t } = useTranslation()
  const [selectedID, setSelectedID] = useState<number | null>(null)
  const query = useQuery({
    queryKey: ['moderation-conversations'],
    queryFn: () => listContentModerationConversations({ limit: 50 }),
  })
  const detailQuery = useQuery({
    queryKey: ['moderation-conversation', selectedID],
    queryFn: () => getContentModerationConversation(selectedID as number),
    enabled: selectedID !== null,
  })

  return (
    <SettingsSection title={t('Moderation Records')}>
      <div className='flex items-center justify-between gap-3'>
        <p className='text-muted-foreground text-sm'>
          {t(
            'Administrators can inspect the retained timeline and resolve false positives.'
          )}
        </p>
        <Button
          type='button'
          variant='outline'
          onClick={() => query.refetch()}
          disabled={query.isFetching}
        >
          {t('Refresh')}
        </Button>
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
      {detailQuery.isLoading && (
        <p className='text-muted-foreground text-sm'>
          {t('Loading conversation details...')}
        </p>
      )}
      {detailQuery.data?.data && (
        <ConversationDetail
          detail={detailQuery.data.data}
          onRefresh={() => query.refetch()}
        />
      )}
    </SettingsSection>
  )
}
