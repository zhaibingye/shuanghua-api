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
  ArrowLeft,
  Eye,
  EyeOff,
  FileText,
  Power,
  PowerOff,
  Save,
  Trash2,
} from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Dialog } from '@/components/dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { formatTimestampToDate } from '@/lib/format'

import {
  deleteContentModerationUserHistory,
  getContentModerationConversation,
  getContentModerationUser,
  updateContentModerationUser,
  updateContentModerationUserStatus,
} from '../api'
import type { ModerationUser } from '../types'
import { ConversationDetail } from './content-moderation-records-section'

const ENABLED_USER_STATUS = 1

function displayTime(timestamp: number) {
  return timestamp ? formatTimestampToDate(timestamp) : '—'
}

function statusLabel(status: string, t: (key: string) => string) {
  const labels: Record<string, string> = {
    active: 'Active',
    blocked: 'Blocked',
    resolved: 'Resolved',
  }
  return t(labels[status] ?? status)
}

type Props = {
  user: ModerationUser | null
  open: boolean
  onOpenChange: (open: boolean) => void
  onChanged: () => void
}

export function ContentModerationUserDetailDialog(props: Props) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [conversationMode, setConversationMode] = useState<
    'violations' | 'all'
  >('violations')
  const [selectedConversationID, setSelectedConversationID] = useState<
    number | null
  >(null)
  const [violationCount, setViolationCount] = useState(0)
  const [note, setNote] = useState('')
  const [statusDialogOpen, setStatusDialogOpen] = useState(false)
  const [statusReason, setStatusReason] = useState('')
  const [deleteDialogOpen, setDeleteDialogOpen] = useState(false)

  const detailQuery = useQuery({
    queryKey: ['moderation-user', props.user?.user_id, conversationMode],
    queryFn: () =>
      getContentModerationUser(props.user?.user_id ?? 0, conversationMode),
    enabled: props.open && props.user !== null,
  })
  const conversationQuery = useQuery({
    queryKey: ['moderation-conversation', selectedConversationID],
    queryFn: () =>
      getContentModerationConversation(selectedConversationID as number),
    enabled: selectedConversationID !== null,
  })

  const detail = detailQuery.data?.data
  const currentUser = detail?.user ?? props.user
  const isHistory = currentUser?.record_status === 'history'
  const isEnabled = currentUser?.account_status === ENABLED_USER_STATUS

  useEffect(() => {
    if (!currentUser) return
    setViolationCount(currentUser.violation_count)
    setNote(currentUser.note)
  }, [currentUser])

  useEffect(() => {
    if (!props.open) {
      setSelectedConversationID(null)
      setConversationMode('violations')
      setStatusDialogOpen(false)
      setDeleteDialogOpen(false)
      setStatusReason('')
    }
  }, [props.open])

  const invalidateUserQueries = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ['moderation-users'] }),
      queryClient.invalidateQueries({
        queryKey: ['moderation-user', props.user?.user_id],
      }),
      queryClient.invalidateQueries({ queryKey: ['moderation-conversations'] }),
    ])
    props.onChanged()
  }

  const updateMutation = useMutation({
    mutationFn: () =>
      updateContentModerationUser(currentUser?.user_id ?? 0, {
        violation_count: violationCount,
        note,
      }),
    onSuccess: async () => {
      toast.success(t('Moderation user record updated'))
      await invalidateUserQueries()
    },
    onError: () => toast.error(t('Failed to update moderation user record')),
  })

  const statusMutation = useMutation({
    mutationFn: () =>
      updateContentModerationUserStatus(
        currentUser?.user_id ?? 0,
        !isEnabled,
        statusReason
      ),
    onSuccess: async () => {
      setStatusDialogOpen(false)
      setStatusReason('')
      toast.success(t('Account status updated'))
      await invalidateUserQueries()
    },
    onError: () => toast.error(t('Failed to update account status')),
  })

  const deleteMutation = useMutation({
    mutationFn: () =>
      deleteContentModerationUserHistory(currentUser?.user_id ?? 0),
    onSuccess: async () => {
      setDeleteDialogOpen(false)
      toast.success(t('History note deleted'))
      await invalidateUserQueries()
      props.onOpenChange(false)
    },
    onError: () => toast.error(t('Failed to delete history note')),
  })

  const closeConversation = () => setSelectedConversationID(null)
  const refreshConversation = () => {
    if (selectedConversationID !== null) {
      void queryClient.invalidateQueries({
        queryKey: ['moderation-conversation', selectedConversationID],
      })
    }
    void invalidateUserQueries()
  }

  return (
    <>
      <Dialog
        open={props.open}
        onOpenChange={props.onOpenChange}
        title={
          selectedConversationID !== null
            ? t('Conversation details')
            : t('Violating user details')
        }
        description={
          selectedConversationID !== null
            ? t(
                'Review the complete moderation timeline for this conversation.'
              )
            : t(
                'Review the user record, recent violations and moderation notes.'
              )
        }
        contentHeight='min(760px, calc(100vh - 10rem))'
        contentClassName='sm:max-w-5xl'
        showCloseButton
      >
        {selectedConversationID !== null && conversationQuery.data?.data ? (
          <div className='space-y-3'>
            <Button
              type='button'
              variant='outline'
              size='sm'
              onClick={closeConversation}
            >
              <ArrowLeft data-icon='inline-start' />
              {t('Back to user record')}
            </Button>
            <ConversationDetail
              detail={conversationQuery.data.data}
              onRefresh={refreshConversation}
              onClose={closeConversation}
            />
          </div>
        ) : (
          <div className='space-y-5'>
            {detailQuery.isLoading && (
              <p className='text-muted-foreground text-sm'>
                {t('Loading violating user details...')}
              </p>
            )}
            {currentUser && (
              <>
                <div className='grid gap-3 sm:grid-cols-2 lg:grid-cols-4'>
                  <div className='rounded-lg border p-3'>
                    <p className='text-muted-foreground text-xs'>{t('User')}</p>
                    <p className='mt-1 font-medium'>
                      {currentUser.username || `#${currentUser.user_id}`}
                    </p>
                    <p className='text-muted-foreground text-xs'>
                      #{currentUser.user_id}
                    </p>
                  </div>
                  <div className='rounded-lg border p-3'>
                    <p className='text-muted-foreground text-xs'>
                      {t('Recent violations')}
                    </p>
                    <p className='mt-1 text-2xl font-semibold tabular-nums'>
                      {currentUser.violation_count}
                    </p>
                    <p className='text-muted-foreground text-xs'>
                      {t('Raw active records')}:{' '}
                      {currentUser.actual_violation_count}
                    </p>
                  </div>
                  <div className='rounded-lg border p-3'>
                    <p className='text-muted-foreground text-xs'>
                      {t('Highest recorded count')}
                    </p>
                    <p className='mt-1 text-2xl font-semibold tabular-nums'>
                      {currentUser.max_violation_count}
                    </p>
                    <p className='text-muted-foreground text-xs'>
                      {currentUser.last_violation_at
                        ? displayTime(currentUser.last_violation_at)
                        : t('No violation time recorded')}
                    </p>
                  </div>
                  <div className='rounded-lg border p-3'>
                    <p className='text-muted-foreground text-xs'>
                      {t('Account status')}
                    </p>
                    <div className='mt-2 flex items-center gap-2'>
                      <Badge variant={isEnabled ? 'secondary' : 'destructive'}>
                        {isEnabled ? t('Enabled') : t('Disabled')}
                      </Badge>
                      <Badge variant='outline'>
                        {isHistory ? t('History') : t('Active')}
                      </Badge>
                    </div>
                  </div>
                </div>

                <div className='flex flex-wrap items-center gap-2'>
                  <Button
                    type='button'
                    variant='outline'
                    onClick={() => setStatusDialogOpen(true)}
                  >
                    {isEnabled ? (
                      <PowerOff data-icon='inline-start' />
                    ) : (
                      <Power data-icon='inline-start' />
                    )}
                    {isEnabled ? t('Disable account') : t('Enable account')}
                  </Button>
                  {isHistory && (
                    <Button
                      type='button'
                      variant='outline'
                      className='text-destructive hover:text-destructive'
                      onClick={() => setDeleteDialogOpen(true)}
                    >
                      <Trash2 data-icon='inline-start' />
                      {t('Delete history note')}
                    </Button>
                  )}
                </div>

                <div className='rounded-xl border p-4'>
                  <div className='mb-3 flex flex-wrap items-center justify-between gap-2'>
                    <div>
                      <h4 className='font-semibold'>{t('User record')}</h4>
                      <p className='text-muted-foreground text-xs'>
                        {t(
                          'The count can be set directly for operator bookkeeping. Original violation records remain unchanged.'
                        )}
                      </p>
                    </div>
                    <Button
                      type='button'
                      size='sm'
                      onClick={() => updateMutation.mutate()}
                      disabled={updateMutation.isPending}
                    >
                      <Save data-icon='inline-start' />
                      {t('Save record')}
                    </Button>
                  </div>
                  <div className='grid gap-4 md:grid-cols-[180px_1fr]'>
                    <label className='space-y-2 text-sm'>
                      <span className='font-medium'>
                        {t('Recent violation count')}
                      </span>
                      <Input
                        type='number'
                        min={0}
                        max={1000000}
                        step={1}
                        value={violationCount}
                        onChange={(event) =>
                          setViolationCount(
                            Number.parseInt(event.target.value, 10) || 0
                          )
                        }
                      />
                    </label>
                    <label className='space-y-2 text-sm'>
                      <span className='font-medium'>{t('Admin note')}</span>
                      <Textarea
                        value={note}
                        maxLength={65535}
                        onChange={(event) => setNote(event.target.value)}
                        placeholder={t('Add an internal note for this user')}
                        className='min-h-24'
                      />
                    </label>
                  </div>
                </div>

                <div className='space-y-3'>
                  <div className='flex flex-wrap items-center justify-between gap-2'>
                    <div>
                      <h4 className='font-semibold'>
                        {t('Related conversations')}
                      </h4>
                      <p className='text-muted-foreground text-xs'>
                        {conversationMode === 'all'
                          ? t(
                              'Showing all retained conversations for this user.'
                            )
                          : t(
                              'Showing conversations that have a violation record.'
                            )}
                      </p>
                    </div>
                    <Button
                      type='button'
                      variant='outline'
                      size='sm'
                      onClick={() =>
                        setConversationMode((mode) =>
                          mode === 'all' ? 'violations' : 'all'
                        )
                      }
                    >
                      {conversationMode === 'all' ? (
                        <EyeOff data-icon='inline-start' />
                      ) : (
                        <Eye data-icon='inline-start' />
                      )}
                      {conversationMode === 'all'
                        ? t('Show violating conversations only')
                        : t('View all retained conversations')}
                    </Button>
                  </div>
                  {detail?.conversations.length === 0 && (
                    <p className='text-muted-foreground rounded-lg border p-4 text-sm'>
                      {t('No retained conversations found.')}
                    </p>
                  )}
                  <div className='grid gap-2'>
                    {detail?.conversations.map((conversation) => (
                      <button
                        key={conversation.id}
                        type='button'
                        className='hover:bg-muted/50 flex w-full items-center justify-between gap-3 rounded-lg border p-3 text-left transition-colors'
                        onClick={() =>
                          setSelectedConversationID(conversation.id)
                        }
                      >
                        <span className='min-w-0'>
                          <span className='flex flex-wrap items-center gap-2'>
                            <FileText className='size-4 shrink-0' />
                            <span className='font-medium'>
                              {t('Conversation')} #{conversation.id}
                            </span>
                            <Badge
                              variant={
                                conversation.status === 'blocked'
                                  ? 'destructive'
                                  : 'secondary'
                              }
                            >
                              {statusLabel(conversation.status, t)}
                            </Badge>
                          </span>
                          <span className='text-muted-foreground mt-1 block truncate text-xs'>
                            {conversation.conversation_id}
                          </span>
                        </span>
                        <span className='text-muted-foreground shrink-0 text-xs'>
                          {displayTime(conversation.last_activity_at)}
                        </span>
                      </button>
                    ))}
                  </div>
                </div>

                <div className='space-y-2'>
                  <h4 className='font-semibold'>{t('User violations')}</h4>
                  {detail?.violations.length === 0 && (
                    <p className='text-muted-foreground rounded-lg border p-4 text-sm'>
                      {t('No retained violations found.')}
                    </p>
                  )}
                  {detail?.violations.map((violation) => (
                    <div
                      key={violation.id}
                      className='flex flex-wrap items-center justify-between gap-2 rounded-lg border p-3 text-sm'
                    >
                      <div>
                        <p className='font-medium'>
                          {violation.severity} · {violation.reason_code}
                        </p>
                        <p className='text-muted-foreground text-xs'>
                          {violation.categories} ·{' '}
                          {Math.round(violation.confidence * 100)}% ·{' '}
                          {displayTime(violation.created_at)}
                        </p>
                      </div>
                      <Badge
                        variant={
                          violation.status === 'active'
                            ? 'destructive'
                            : 'secondary'
                        }
                      >
                        {violation.status === 'active'
                          ? t('Active')
                          : t('Resolved')}
                      </Badge>
                    </div>
                  ))}
                </div>
              </>
            )}
          </div>
        )}
      </Dialog>

      <ConfirmDialog
        open={statusDialogOpen}
        onOpenChange={setStatusDialogOpen}
        title={isEnabled ? t('Disable account') : t('Enable account')}
        desc={t(
          'This changes the account status immediately. Add a reason for the moderation action.'
        )}
        confirmText={isEnabled ? t('Disable account') : t('Enable account')}
        handleConfirm={() => statusMutation.mutate()}
        isLoading={statusMutation.isPending}
        disabled={statusReason.trim().length === 0}
        destructive={isEnabled}
      >
        <Textarea
          value={statusReason}
          maxLength={4096}
          onChange={(event) => setStatusReason(event.target.value)}
          placeholder={t('Reason for this account status change')}
          aria-label={t('Reason')}
        />
      </ConfirmDialog>

      <ConfirmDialog
        open={deleteDialogOpen}
        onOpenChange={setDeleteDialogOpen}
        title={t('Delete history note')}
        desc={t(
          'Delete this moderation history note? Original conversations and violation records will not be deleted.'
        )}
        confirmText={t('Delete')}
        handleConfirm={() => deleteMutation.mutate()}
        isLoading={deleteMutation.isPending}
        destructive
      />
    </>
  )
}
