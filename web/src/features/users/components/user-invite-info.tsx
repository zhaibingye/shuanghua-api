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
import { useTranslation } from 'react-i18next'

import { LongText } from '@/components/long-text'
import { Button } from '@/components/ui/button'
import { formatQuota } from '@/lib/format'

import type { User } from '../types'
import { useUsers } from './users-provider'

export function UserInviteInfo({ user }: { user: User }) {
  const { t } = useTranslation()
  const { setCurrentRow, setOpen } = useUsers()
  const count = user.aff_count || 0
  const revenue = user.aff_history_quota || 0
  const inviter = user.inviter_id || 0

  if (count === 0 && revenue === 0 && inviter === 0) {
    return <span className='text-muted-foreground text-sm'>—</span>
  }

  return (
    <div
      data-table-text='secondary'
      className='min-w-0 space-y-1 text-xs font-normal'
    >
      {(count > 0 || revenue !== 0) && (
        <LongText>
          <Button
            variant='link'
            className='h-auto p-0 text-xs'
            onClick={() => {
              setCurrentRow(user)
              setOpen('invitees')
            }}
          >
            {t('Invited {{count}} users', { count })}
          </Button>{' '}
          · {t('Earnings')}:{' '}
          <span className='tabular-nums'>{formatQuota(revenue)}</span>
        </LongText>
      )}
      {inviter > 0 && (
        <LongText className='text-muted-foreground'>
          {t('Inviter')} ID: {inviter}
        </LongText>
      )}
    </div>
  )
}
