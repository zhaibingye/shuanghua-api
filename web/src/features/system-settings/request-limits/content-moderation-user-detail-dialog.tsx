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
import { ArrowLeft, Power, PowerOff, Save, Trash2 } from 'lucide-react'
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
  getContentModerationUser,
  updateContentModerationUser,
  updateContentModerationUserStatus,
} from '../api'
import type { ModerationUser } from '../types'
import { EventDetail } from './content-moderation-records-section'

const ENABLED_USER_STATUS = 1

function displayTime(timestamp: number) {
  return timestamp ? formatTimestampToDate(timestamp) : '—'
}

function statusLabel(status: string, t: (key: string) => string) {
  const labels: Record<string, string> = {
    active: 'Active',
    false_positive: 'False positive',
    reversed: 'Reversed',
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
  const [selectedEventID, setSelectedEventID] = useState<number | null>(null)
  const [violationCount, setViolationCount] = useState(0)
  const [note, setNote] = useState('')
  const [statusDialogOpen, setStatusDialogOpen] = useState(false)
  const [statusReason, setStatusReason] = useState('')
  const [deleteDialogOpen, setDeleteDialogOpen] = useState(false)

  const detailQuery = useQuery({
    queryKey: ['moderation-user', props.user?.user_id],
    queryFn: () => getContentModerationUser(props.user?.user_id ?? 0),
    enabled: props.open && props.user !== null,
  })

  const detail = detailQuery.data?.data
  const currentUser = detail?.user ?? props.user
  const isHistory = currentUser?.record_status === 'history'
  const isEnabled = currentUser?.account_status === ENABLED_USER_STATUS
  const selectedEvent = detail?.events.find(
    (event) => event.id === selectedEventID
  )

  useEffect(() => {
    if (!currentUser) return
    setViolationCount(currentUser.violation_count)
    setNote(currentUser.note)
  }, [currentUser])

  useEffect(() => {
    if (!props.open) {
      setSelectedEventID(null)
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
      queryClient.invalidateQueries({ queryKey: ['moderation-events'] }),
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
      toast.success(t('User moderation record deleted'))
      await invalidateUserQueries()
      props.onOpenChange(false)
    },
    onError: () => toast.error(t('Failed to delete history note')),
  })

  return (
    <>
      <Dialog
        open={props.open}
        onOpenChange={props.onOpenChange}
        title={
          selectedEvent
            ? t('Violation event details')
            : t('Violating user details')
        }
        description={
          selectedEvent
            ? t('Review the truncated evidence saved for this flagged request.')
            : t(
                'Review the user record, recent violations and moderation notes.'
              )
        }
        contentHeight='min(760px, calc(100vh - 10rem))'
        contentClassName='sm:max-w-5xl'
        showCloseButton
      >
        {selectedEvent ? (
          <div className='space-y-3'>
            <Button
              type='button'
              variant='outline'
              size='sm'
              onClick={() => setSelectedEventID(null)}
            >
              <ArrowLeft data-icon='inline-start' />
              {t('Back to user record')}
            </Button>
            <EventDetail
              event={selectedEvent}
              onRefresh={() => {
                void invalidateUserQueries()
              }}
              onClose={() => setSelectedEventID(null)}
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
                  <Button
                    type='button'
                    variant='outline'
                    className='text-destructive hover:text-destructive'
                    onClick={() => setDeleteDialogOpen(true)}
                  >
                    <Trash2 data-icon='inline-start' />
                    {t('Delete user record')}
                  </Button>
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
                  <div>
                    <h4 className='font-semibold'>
                      {t('Recent flagged events')}
                    </h4>
                    <p className='text-muted-foreground text-xs'>
                      {t(
                        'Only retained violation excerpts are listed. Clean traffic is not stored.'
                      )}
                    </p>
                  </div>
                  {detail?.events.length === 0 && (
                    <p className='text-muted-foreground rounded-lg border p-4 text-sm'>
                      {t('No retained violations found.')}
                    </p>
                  )}
                  <div className='grid gap-2'>
                    {detail?.events.map((event) => (
                      <button
                        key={event.id}
                        type='button'
                        className='hover:bg-muted/50 flex w-full items-center justify-between gap-3 rounded-lg border p-3 text-left transition-colors'
                        onClick={() => setSelectedEventID(event.id)}
                      >
                        <span className='min-w-0'>
                          <span className='flex flex-wrap items-center gap-2'>
                            <span className='font-medium'>
                              {event.severity ||
                                event.reason_code ||
                                t('Violation event')}
                            </span>
                            <Badge
                              variant={
                                event.status === 'active'
                                  ? 'destructive'
                                  : 'secondary'
                              }
                            >
                              {statusLabel(event.status, t)}
                            </Badge>
                          </span>
                          <span className='text-muted-foreground mt-1 block truncate text-xs'>
                            {event.user_excerpt || event.assistant_excerpt}
                          </span>
                        </span>
                        <span className='text-muted-foreground shrink-0 text-xs'>
                          {displayTime(event.created_at)}
                        </span>
                      </button>
                    ))}
                  </div>
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
        title={t('Delete user record')}
        desc={t(
          'Are you sure you want to delete this user moderation record? Associated violation events and notes will be cleared.'
        )}
        confirmText={t('Delete')}
        handleConfirm={() => deleteMutation.mutate()}
        isLoading={deleteMutation.isPending}
        destructive
      />
    </>
  )
}
