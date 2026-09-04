/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ChevronLeft, ChevronRight, RefreshCw, Search } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { cn } from '@/lib/utils'

import { listContentModerationUsers } from '../api'
import { SettingsSection } from '../components/settings-section'
import type { ModerationUser } from '../types'
import { ContentModerationUserDetailDialog } from './content-moderation-user-detail-dialog'

const PAGE_SIZE = 20
const ENABLED_USER_STATUS = 1

type UserStatus = 'active' | 'history'

function displayTime(timestamp: number) {
  return timestamp ? new Date(timestamp * 1000).toLocaleString() : '—'
}

type UserRowProps = {
  user: ModerationUser
  onOpen: () => void
}

function UserRow(props: UserRowProps) {
  const { t } = useTranslation()
  const isEnabled = props.user.account_status === ENABLED_USER_STATUS
  return (
    <button
      type='button'
      className='hover:bg-muted/50 flex w-full flex-col gap-3 rounded-xl border p-4 text-left transition-colors md:flex-row md:items-center md:justify-between'
      onClick={props.onOpen}
    >
      <div className='min-w-0 space-y-1'>
        <div className='flex flex-wrap items-center gap-2'>
          <span className='font-medium'>
            {props.user.username || `#${props.user.user_id}`}
          </span>
          <Badge variant={isEnabled ? 'secondary' : 'destructive'}>
            {isEnabled ? t('Enabled') : t('Disabled')}
          </Badge>
          {props.user.record_status === 'history' && (
            <Badge variant='outline'>{t('History')}</Badge>
          )}
        </div>
        <p className='text-muted-foreground truncate text-sm'>
          #{props.user.user_id}
          {props.user.display_name &&
          props.user.display_name !== props.user.username
            ? ` · ${props.user.display_name}`
            : ''}
          {props.user.email ? ` · ${props.user.email}` : ''}
        </p>
        {props.user.note && (
          <p className='text-muted-foreground line-clamp-2 text-xs'>
            {props.user.note}
          </p>
        )}
      </div>
      <div className='flex shrink-0 items-center gap-5 text-sm md:text-right'>
        <div>
          <p className='text-muted-foreground text-xs'>
            {t('Recent violations')}
          </p>
          <p className='text-lg font-semibold tabular-nums'>
            {props.user.violation_count}
          </p>
        </div>
        <div>
          <p className='text-muted-foreground text-xs'>
            {t('Highest recorded count')}
          </p>
          <p className='font-medium tabular-nums'>
            {props.user.max_violation_count}
          </p>
        </div>
        <div className='hidden min-w-36 lg:block'>
          <p className='text-muted-foreground text-xs'>{t('Last violation')}</p>
          <p className='text-muted-foreground text-xs'>
            {displayTime(props.user.last_violation_at)}
          </p>
        </div>
      </div>
    </button>
  )
}

export function ContentModerationUsersSection() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [status, setStatus] = useState<UserStatus>('active')
  const [page, setPage] = useState(1)
  const [userIDInput, setUserIDInput] = useState('')
  const [userID, setUserID] = useState<number | undefined>()
  const [selectedUser, setSelectedUser] = useState<ModerationUser | null>(null)

  const query = useQuery({
    queryKey: ['moderation-users', status, userID, page],
    queryFn: () =>
      listContentModerationUsers({
        status,
        user_id: userID,
        limit: PAGE_SIZE,
        offset: (page - 1) * PAGE_SIZE,
      }),
  })

  const total = query.data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE))

  const handleSearch = () => {
    const parsed = Number.parseInt(userIDInput.trim(), 10)
    setUserID(Number.isFinite(parsed) && parsed > 0 ? parsed : undefined)
    setPage(1)
  }

  const handleStatusChange = (nextStatus: UserStatus) => {
    setStatus(nextStatus)
    setPage(1)
  }

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ['moderation-users'] })
  }

  return (
    <SettingsSection title={t('Violating Users')}>
      <div className='space-y-4'>
        <div className='flex flex-col gap-3 rounded-xl border p-3 sm:flex-row sm:items-center sm:justify-between'>
          <div className='bg-muted/50 flex flex-wrap gap-1 rounded-lg p-1'>
            <Button
              type='button'
              size='sm'
              variant={status === 'active' ? 'secondary' : 'ghost'}
              onClick={() => handleStatusChange('active')}
            >
              {t('Active violating users')}
            </Button>
            <Button
              type='button'
              size='sm'
              variant={status === 'history' ? 'secondary' : 'ghost'}
              onClick={() => handleStatusChange('history')}
            >
              {t('History notes')}
            </Button>
          </div>
          <div className='flex w-full gap-2 sm:max-w-xs'>
            <Input
              type='number'
              min={1}
              placeholder={t('Filter by user ID')}
              value={userIDInput}
              onChange={(event) => setUserIDInput(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === 'Enter') handleSearch()
              }}
              className='h-9'
            />
            <Button
              type='button'
              size='icon'
              variant='outline'
              onClick={handleSearch}
              aria-label={t('Search')}
              title={t('Search')}
            >
              <Search />
            </Button>
            <Button
              type='button'
              size='icon'
              variant='outline'
              onClick={refresh}
              disabled={query.isFetching}
              aria-label={t('Refresh')}
              title={t('Refresh')}
            >
              <RefreshCw className={cn(query.isFetching && 'animate-spin')} />
            </Button>
          </div>
        </div>

        {query.isLoading && (
          <p className='text-muted-foreground text-sm'>
            {t('Loading violating users...')}
          </p>
        )}
        {!query.isLoading && query.data?.data.length === 0 && (
          <div className='rounded-xl border border-dashed p-8 text-center'>
            <p className='font-medium'>
              {status === 'history'
                ? t('No history notes found.')
                : t('No active violating users found.')}
            </p>
            <p className='text-muted-foreground mt-1 text-sm'>
              {status === 'history'
                ? t('Users with a cleared count will appear here.')
                : t('Users with active user violations will appear here.')}
            </p>
          </div>
        )}
        <div className='space-y-3'>
          {query.data?.data.map((user) => (
            <UserRow
              key={`${user.user_id}-${user.record_status}`}
              user={user}
              onOpen={() => setSelectedUser(user)}
            />
          ))}
        </div>

        {total > 0 && (
          <div className='flex items-center justify-between gap-3 pt-2 text-sm'>
            <span className='text-muted-foreground text-xs'>
              {t('Total Count')}: {total}
            </span>
            {totalPages > 1 && (
              <div className='flex items-center gap-2'>
                <span className='text-muted-foreground text-xs'>
                  {page} / {totalPages}
                </span>
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  className='h-8 px-2.5'
                  onClick={() => setPage((current) => Math.max(1, current - 1))}
                  disabled={page <= 1 || query.isFetching}
                >
                  <ChevronLeft />
                  <span className='sr-only'>{t('Go to previous page')}</span>
                </Button>
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  className='h-8 px-2.5'
                  onClick={() =>
                    setPage((current) => Math.min(totalPages, current + 1))
                  }
                  disabled={page >= totalPages || query.isFetching}
                >
                  <ChevronRight />
                  <span className='sr-only'>{t('Go to next page')}</span>
                </Button>
              </div>
            )}
          </div>
        )}
      </div>

      <ContentModerationUserDetailDialog
        user={selectedUser}
        open={selectedUser !== null}
        onOpenChange={(open) => !open && setSelectedUser(null)}
        onChanged={refresh}
      />
    </SettingsSection>
  )
}
