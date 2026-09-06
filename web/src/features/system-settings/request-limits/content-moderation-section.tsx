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
import { Copy, Eye, EyeOff, Loader2 } from 'lucide-react'
import { useCallback, useEffect, useState, type ChangeEvent } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import * as z from 'zod'

import { Button } from '@/components/ui/button'
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
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Separator } from '@/components/ui/separator'
import { Switch } from '@/components/ui/switch'
import {
  SecureVerificationDialog,
  useSecureVerification,
} from '@/features/auth/secure-verification'
import { useCopyToClipboard } from '@/hooks/use-copy-to-clipboard'

import {
  getContentModerationKey,
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
  channels: z.string().max(2048),
  user_whitelist: z.string().max(2048),
  violation_retention_days: z.number().int().min(1).max(365),
  base_url: z.string().max(2048),
  api_key: z.string().max(4096),
  model: z.string().max(128),
  preflight_enabled: z.boolean(),
  failure_mode: z.enum(['open', 'closed']),
  timeout_seconds: z.number().int().min(1).max(120),
  max_retries: z.number().int().min(1).max(5),
})

type ContentModerationFormValues = z.infer<typeof contentModerationSchema>

type ContentModerationSectionProps = {
  defaultValues?: Partial<ContentModerationFormValues>
}

const fallbackValues: ContentModerationFormValues = {
  enabled: false,
  channels: '',
  user_whitelist: '1',
  violation_retention_days: 7,
  base_url: '',
  api_key: '',
  model: 'omni-moderation-latest',
  preflight_enabled: true,
  failure_mode: 'closed',
  timeout_seconds: 30,
  max_retries: 3,
}

function numberField(field: { onChange: (value: number) => void }) {
  return (event: ChangeEvent<HTMLInputElement>) => {
    field.onChange(Number.parseInt(event.target.value, 10) || 0)
  }
}

export function ContentModerationSection(props: ContentModerationSectionProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const { copyToClipboard } = useCopyToClipboard()

  const [moderationKey, setModerationKey] = useState<string | null>(null)
  const [isKeyLoading, setIsKeyLoading] = useState(false)
  const [showInputKey, setShowInputKey] = useState(false)

  const {
    open: verificationOpen,
    methods: verificationMethods,
    state: verificationState,
    executeVerification,
    withVerification,
    cancel: cancelVerification,
    setCode: setVerificationCode,
    switchMethod: switchVerificationMethod,
  } = useSecureVerification()

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
      channels: data.channels ?? '',
      user_whitelist: data.user_whitelist ?? '1',
      violation_retention_days: data.violation_retention_days ?? 7,
      base_url: data.base_url ?? '',
      api_key: '',
      model: data.model || 'omni-moderation-latest',
      preflight_enabled: data.preflight_enabled ?? true,
      failure_mode: data.failure_mode ?? 'closed',
      timeout_seconds: data.timeout_seconds ?? 30,
      max_retries: data.max_retries ?? 3,
    })
    setModerationKey(null)
  }, [form, query.data])

  const fetchKey = useCallback(
    async (proofToken?: string) => {
      setIsKeyLoading(true)
      try {
        const res = await getContentModerationKey(proofToken)
        if (!res.success) {
          throw new Error(res.message || t('Failed to fetch moderation key'))
        }
        const keyValue = res.data?.key ?? ''
        setModerationKey(keyValue)
        toast.success(t('Moderation key unlocked'))
        return res
      } finally {
        setIsKeyLoading(false)
      }
    },
    [t]
  )

  const handleRevealKey = useCallback(async () => {
    try {
      await withVerification(fetchKey, {
        scope: 'channel.key.read',
        preferredMethod: 'passkey',
        title: t('Verify to view moderation key'),
        description: t(
          'Use Passkey or 2FA to confirm your identity before revealing this moderation key.'
        ),
      })
    } catch (error) {
      if (
        !(error instanceof Error) ||
        error.message !== 'Verification canceled'
      ) {
        toast.error(
          error instanceof Error
            ? error.message
            : t('Failed to fetch moderation key')
        )
      }
    }
  }, [fetchKey, t, withVerification])

  const onSubmit = async (values: ContentModerationFormValues) => {
    if (
      values.enabled &&
      values.api_key.trim() === '' &&
      !query.data?.data.api_key_configured
    ) {
      form.setError('api_key', {
        type: 'required',
        message: t(
          'A moderation API key is required when content moderation is enabled.'
        ),
      })
      return
    }
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

          {/* Section 1: Provider Settings */}
          <div data-settings-form-span='full' className='space-y-4'>
            <div className='flex flex-col gap-1'>
              <h4 className='text-sm font-medium'>{t('Provider Settings')}</h4>
              <p className='text-muted-foreground text-xs'>
                {t(
                  'OpenAI-compatible moderation service endpoint and credentials.'
                )}
              </p>
            </div>

            <FormField
              control={form.control}
              name='enabled'
              render={({ field }) => (
                <SettingsSwitchItem>
                  <SettingsSwitchContent>
                    <FormLabel>{t('Enable content moderation')}</FormLabel>
                    <FormDescription>
                      {t(
                        'Moderate chat requests and responses using the OpenAI Moderations API.'
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

            <div className='grid gap-4 sm:grid-cols-2'>
              <FormField
                control={form.control}
                name='model'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Moderation model')}</FormLabel>
                    <FormControl>
                      <Input placeholder='omni-moderation-latest' {...field} />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'OpenAI moderation model name (default: omni-moderation-latest).'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name='base_url'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Moderation API base URL')}</FormLabel>
                    <FormControl>
                      <Input
                        placeholder='https://api.openai.com/v1'
                        {...field}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Defaults to the official OpenAI URL (https://api.openai.com/v1) if left blank.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </div>

            <FormField
              control={form.control}
              name='api_key'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Moderation API key')}</FormLabel>
                  <div className='flex items-center gap-2'>
                    <FormControl>
                      <Input
                        type={showInputKey ? 'text' : 'password'}
                        autoComplete='new-password'
                        placeholder={
                          query.data?.data.api_key_configured
                            ? t('Leave empty to keep existing key')
                            : t('Enter the moderation API key')
                        }
                        {...field}
                      />
                    </FormControl>
                    <Button
                      type='button'
                      variant='outline'
                      size='icon'
                      onClick={() => setShowInputKey(!showInputKey)}
                      aria-label={showInputKey ? t('Hide') : t('Show')}
                      title={showInputKey ? t('Hide') : t('Show')}
                    >
                      {showInputKey ? (
                        <EyeOff className='h-4 w-4' />
                      ) : (
                        <Eye className='h-4 w-4' />
                      )}
                    </Button>
                  </div>
                  <FormDescription>
                    {query.data?.data.api_key_configured
                      ? t(
                          'A moderation API key is currently configured. Leave blank to keep current key.'
                        )
                      : t(
                          'Enter the moderation API key to enable content inspection.'
                        )}
                  </FormDescription>
                  <FormMessage />

                  {query.data?.data.api_key_configured && (
                    <div className='border-border/60 mt-3 flex flex-col gap-3 rounded-lg border border-dashed p-3'>
                      <div className='flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between'>
                        <div>
                          <p className='text-sm font-medium'>
                            {t('Current key')}
                          </p>
                          <p className='text-muted-foreground text-xs'>
                            {t(
                              'Verification required to reveal the saved key.'
                            )}
                          </p>
                        </div>
                        <div className='flex items-center gap-2'>
                          <Button
                            type='button'
                            variant='outline'
                            size='sm'
                            onClick={handleRevealKey}
                            disabled={isKeyLoading || verificationState.loading}
                          >
                            {isKeyLoading || verificationState.loading ? (
                              <Loader2 className='mr-2 h-4 w-4 animate-spin' />
                            ) : (
                              <Eye className='mr-2 h-4 w-4' />
                            )}
                            {t('Reveal key')}
                          </Button>
                          {moderationKey && (
                            <Button
                              type='button'
                              variant='ghost'
                              size='sm'
                              onClick={() => setModerationKey(null)}
                            >
                              <EyeOff className='mr-2 h-4 w-4' />
                              {t('Hide')}
                            </Button>
                          )}
                        </div>
                      </div>
                      {moderationKey && (
                        <div className='flex items-center gap-2'>
                          <Input
                            readOnly
                            value={moderationKey}
                            className='font-mono text-xs'
                          />
                          <Button
                            type='button'
                            variant='outline'
                            size='icon-sm'
                            onClick={() => {
                              copyToClipboard(moderationKey)
                              toast.success(t('Key copied to clipboard'))
                            }}
                            aria-label={t('Copy')}
                            title={t('Copy')}
                          >
                            <Copy className='h-4 w-4' />
                          </Button>
                        </div>
                      )}
                    </div>
                  )}
                </FormItem>
              )}
            />
          </div>

          <Separator data-settings-form-span='full' />

          {/* Section 2: Execution & Policy */}
          <div data-settings-form-span='full' className='space-y-4'>
            <div className='flex flex-col gap-1'>
              <h4 className='text-sm font-medium'>{t('Execution & Policy')}</h4>
              <p className='text-muted-foreground text-xs'>
                {t(
                  'Moderation enforcement, provider failure behavior, timeouts, and retention.'
                )}
              </p>
            </div>

            <FormField
              control={form.control}
              name='preflight_enabled'
              render={({ field }) => (
                <SettingsSwitchItem>
                  <SettingsSwitchContent>
                    <FormLabel>
                      {t('Block unsafe requests before upstream')}
                    </FormLabel>
                    <FormDescription>
                      {t(
                        'Use the OpenAI Moderations API synchronously before forwarding a request. Disable only for audit-only mode.'
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

            <FormField
              control={form.control}
              name='failure_mode'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Provider failure behavior')}</FormLabel>
                  <Select
                    items={[
                      { value: 'closed', label: t('Fail closed (block)') },
                      { value: 'open', label: t('Fail open (allow and log)') },
                    ]}
                    value={field.value}
                    onValueChange={field.onChange}
                  >
                    <FormControl>
                      <SelectTrigger className='w-full'>
                        <SelectValue />
                      </SelectTrigger>
                    </FormControl>
                    <SelectContent alignItemWithTrigger={false}>
                      <SelectItem value='closed'>
                        {t('Fail closed (block)')}
                      </SelectItem>
                      <SelectItem value='open'>
                        {t('Fail open (allow and log)')}
                      </SelectItem>
                    </SelectContent>
                  </Select>
                  <FormDescription>
                    {t(
                      'Fail closed is recommended for production enforcement.'
                    )}
                  </FormDescription>
                </FormItem>
              )}
            />

            <div className='grid gap-4 sm:grid-cols-3'>
              <FormField
                control={form.control}
                name='timeout_seconds'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Request timeout (seconds)')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={1}
                        max={120}
                        step={1}
                        {...field}
                        onChange={numberField(field)}
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='max_retries'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Retry count')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={1}
                        max={5}
                        step={1}
                        {...field}
                        onChange={numberField(field)}
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='violation_retention_days'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Violation retention (days)')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={1}
                        max={365}
                        step={1}
                        {...field}
                        onChange={numberField(field)}
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </div>
          </div>

          <Separator data-settings-form-span='full' />

          {/* Section 3: Scope & Whitelist */}
          <div data-settings-form-span='full' className='space-y-4'>
            <div className='flex flex-col gap-1'>
              <h4 className='text-sm font-medium'>{t('Scope & Whitelist')}</h4>
              <p className='text-muted-foreground text-xs'>
                {t(
                  'Target channel routing and user bypass rules for content moderation.'
                )}
              </p>
            </div>

            <FormField
              control={form.control}
              name='channels'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Channels to moderate')}</FormLabel>
                  <FormControl>
                    <Input
                      placeholder={t(
                        'e.g. 1, 2, 3 (only these channels will be reviewed)'
                      )}
                      {...field}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'Content moderation only runs for requests routed through these channel IDs. Separate IDs with commas or spaces. Leave empty to disable moderation.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='user_whitelist'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Users excluded from moderation')}</FormLabel>
                  <FormControl>
                    <Input
                      placeholder={t(
                        'e.g. 1, 2, 3 (these users will be skipped)'
                      )}
                      {...field}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'These user IDs completely bypass content moderation: their requests are not reviewed and no moderation conversation records are saved. Separate IDs with commas or spaces. The root administrator (ID: 1) is always excluded.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
          </div>

          {mutation.isError && (
            <p
              data-settings-form-span='full'
              className='text-destructive text-sm'
            >
              {t('Failed to save content moderation settings.')}
            </p>
          )}
        </SettingsForm>
      </Form>

      <SecureVerificationDialog
        open={verificationOpen}
        onOpenChange={(nextOpen) => {
          if (!nextOpen) cancelVerification()
        }}
        methods={verificationMethods}
        state={verificationState}
        onVerify={async (method, code) => {
          await executeVerification(method, code)
        }}
        onCancel={cancelVerification}
        onCodeChange={setVerificationCode}
        onMethodChange={switchVerificationMethod}
      />
    </SettingsSection>
  )
}
