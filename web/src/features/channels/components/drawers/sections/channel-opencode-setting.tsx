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
import type { Control } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import {
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
} from '@/components/ui/form'
import { Switch } from '@/components/ui/switch'

import { OPENCODE_GO_COMPAT_TYPES } from '../../../constants'
import type { ChannelFormValues } from '../../../lib/channel-form'

type ChannelOpenCodeSettingProps = {
  control: Control<ChannelFormValues>
  channelType: number
  disabled: boolean
}

export function ChannelOpenCodeSetting(props: ChannelOpenCodeSettingProps) {
  const { t } = useTranslation()
  if (!OPENCODE_GO_COMPAT_TYPES.has(props.channelType)) return null

  return (
    <FormField
      control={props.control}
      name='opencode_go_compat'
      render={({ field }) => (
        <FormItem className='flex items-center justify-between gap-3 px-4 py-3'>
          <div className='min-w-0 space-y-0.5'>
            <FormLabel>{t('OpenCode Go compatibility')}</FormLabel>
            <FormDescription>
              {t(
                'Send X-Opencode-Session using the client conversation ID, or infer a stable ID from message history. Does not change channel or API key routing.'
              )}
            </FormDescription>
          </div>
          <FormControl>
            <Switch
              checked={field.value ?? false}
              onCheckedChange={field.onChange}
              disabled={props.disabled}
              onBlur={field.onBlur}
              ref={field.ref}
            />
          </FormControl>
        </FormItem>
      )}
    />
  )
}
