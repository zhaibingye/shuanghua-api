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
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, type ChangeEvent } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import * as z from 'zod'

import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'

import {
  getContentModerationSettings,
  updateContentModerationSettings,
} from '../api'
import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'

const contentModerationSchema = z.object({
  enabled: z.boolean(),
  provider: z.enum(['responses', 'gemini']),
  base_url: z.string().max(2048),
  api_key: z.string().max(4096),
  model: z.string().max(128),
  timeout_seconds: z.number().int().min(1).max(120),
  max_retries: z.number().int().min(1).max(5),
  normal_sample_rate: z.number().int().min(0).max(100),
  elevated_sample_rate: z.number().int().min(0).max(100),
  prompt_version: z.string().min(1).max(32),
})

type ContentModerationFormValues = z.infer<typeof contentModerationSchema>

type ContentModerationSectionProps = {
  defaultValues?: Partial<ContentModerationFormValues>
}

const fallbackValues: ContentModerationFormValues = {
  enabled: false,
  provider: 'responses',
  base_url: '',
  api_key: '',
  model: '',
  timeout_seconds: 30,
  max_retries: 3,
  normal_sample_rate: 10,
  elevated_sample_rate: 50,
  prompt_version: 'v1',
}

function numberField(field: { onChange: (value: number) => void }) {
  return (event: ChangeEvent<HTMLInputElement>) => {
    field.onChange(Number.parseInt(event.target.value, 10) || 0)
  }
}

export function ContentModerationSection(props: ContentModerationSectionProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const query = useQuery({
    queryKey: ['content-moderation-settings'],
    queryFn: getContentModerationSettings,
  })
  const mutation = useMutation({
    mutationFn: updateContentModerationSettings,
    onSuccess: async () => {
      toast.success(t('Setting updated successfully'))
      await queryClient.invalidateQueries({
        queryKey: ['content-moderation-settings'],
      })
    },
  })
  const form = useForm<ContentModerationFormValues>({
    resolver: zodResolver(contentModerationSchema),
    defaultValues: { ...fallbackValues, ...props.defaultValues },
  })

  useEffect(() => {
    const data = query.data?.data
    if (!data) return
    form.reset({
      enabled: data.enabled,
      provider: data.provider,
      base_url: data.base_url,
      api_key: '',
      model: data.model,
      timeout_seconds: data.timeout_seconds,
      max_retries: data.max_retries,
      normal_sample_rate: data.normal_sample_rate,
      elevated_sample_rate: data.elevated_sample_rate,
      prompt_version: data.prompt_version,
    })
  }, [form, query.data])

  const onSubmit = async (values: ContentModerationFormValues) => {
    await mutation.mutateAsync(values)
  }

  if (query.isLoading) {
    return (
      <SettingsSection title={t('Content Moderation')}>
        <p className='text-muted-foreground text-sm'>
          {t('Loading content moderation settings...')}
        </p>
      </SettingsSection>
    )
  }

  return (
    <SettingsSection title={t('Content Moderation')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)}>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={mutation.isPending}
            saveLabel='Save content moderation settings'
          />
          <FormField
            control={form.control}
            name='enabled'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>{t('Enable content moderation')}</FormLabel>
                  <FormDescription>
                    {t(
                      'Review the first three conversation rounds and use local risk signals plus sampling for later rounds.'
                    )}
                  </FormDescription>
                </SettingsSwitchContent>
                <FormControl>
                  <Switch
                    checked={field.value}
                    onCheckedChange={field.onChange}
                  />
                </FormControl>
              </SettingsSwitchItem>
            )}
          />
          <div className='grid gap-4 md:grid-cols-2'>
            <FormField
              control={form.control}
              name='provider'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Moderation API format')}</FormLabel>
                  <Select value={field.value} onValueChange={field.onChange}>
                    <FormControl>
                      <SelectTrigger>
                        <SelectValue />
                      </SelectTrigger>
                    </FormControl>
                    <SelectContent>
                      <SelectGroup>
                        <SelectItem value='responses'>
                          {t('OpenAI Responses')}
                        </SelectItem>
                        <SelectItem value='gemini'>Gemini</SelectItem>
                      </SelectGroup>
                    </SelectContent>
                  </Select>
                  <FormDescription>
                    {t(
                      'This configuration is independent from relay channels.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='model'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Moderation model')}</FormLabel>
                  <FormControl>
                    <Input placeholder='gpt-4.1-mini' {...field} />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
          </div>
          <FormField
            control={form.control}
            name='base_url'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Moderation API URL')}</FormLabel>
                <FormControl>
                  <Input
                    placeholder='https://api.openai.com/v1/responses'
                    {...field}
                  />
                </FormControl>
                <FormDescription>
                  {t('Leave blank to use the provider default endpoint.')}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name='api_key'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Moderation API key')}</FormLabel>
                <FormControl>
                  <Input
                    type='password'
                    autoComplete='new-password'
                    placeholder={
                      query.data?.data.api_key_configured
                        ? t('Configured; leave blank to keep the current key')
                        : t('Enter the moderation API key')
                    }
                    {...field}
                  />
                </FormControl>
                <FormDescription>
                  {t('The key is never returned to the browser after saving.')}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
          <div className='grid gap-4 md:grid-cols-4'>
            {(
              [
                ['timeout_seconds', 'Timeout seconds', 1, 120],
                ['max_retries', 'Maximum retries', 1, 5],
                ['normal_sample_rate', 'Normal sample rate', 0, 100],
                ['elevated_sample_rate', 'Elevated sample rate', 0, 100],
              ] as const
            ).map(([name, label, min, max]) => (
              <FormField
                key={name}
                control={form.control}
                name={name}
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t(label)}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={min}
                        max={max}
                        step={1}
                        {...field}
                        onChange={numberField(field)}
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
            ))}
          </div>
          <FormField
            control={form.control}
            name='prompt_version'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Policy prompt version')}</FormLabel>
                <FormControl>
                  <Input placeholder='v1' {...field} />
                </FormControl>
                <FormDescription>
                  {t('Change this when the fixed moderation policy changes.')}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
          {mutation.isError && (
            <p className='text-destructive text-sm'>
              {t('Failed to save content moderation settings.')}
            </p>
          )}
          {query.data?.data.api_key_configured && (
            <p className='text-muted-foreground text-sm'>
              {t('A moderation API key is currently configured.')}
            </p>
          )}
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
