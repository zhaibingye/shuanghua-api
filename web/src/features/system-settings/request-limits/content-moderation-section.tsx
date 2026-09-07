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
import {
  CheckCircle2,
  Copy,
  Eye,
  EyeOff,
  Loader2,
  Pencil,
  RotateCcw,
  Trash2,
  XCircle,
} from 'lucide-react'
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
import { Textarea } from '@/components/ui/textarea'
import {
  SecureVerificationDialog,
  useSecureVerification,
} from '@/features/auth/secure-verification'
import { useCopyToClipboard } from '@/hooks/use-copy-to-clipboard'

import {
  getContentModerationKey,
  getContentModerationSettings,
  testContentModerationKeys,
  updateContentModerationSettings,
} from '../api'
import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import type { ModerationKeyTestResult } from '../types'

const contentModerationSchema = z.object({
  enabled: z.boolean(),
  channels: z.string().max(2048),
  user_whitelist: z.string().max(2048),
  violation_retention_days: z.number().int().min(1).max(365),
  base_url: z.string().max(2048),
  api_key: z.string().max(32768),
  model: z.string().max(128),
  preflight_enabled: z.boolean(),
  postflight_enabled: z.boolean(),
  failure_mode: z.enum(['open', 'closed']),
  timeout_seconds: z.number().int().min(1).max(120),
  max_retries: z.number().int().min(1).max(5),
  auto_disable_violations: z.number().int().min(0).max(1000),
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
  postflight_enabled: false,
  failure_mode: 'closed',
  timeout_seconds: 30,
  max_retries: 3,
  auto_disable_violations: 0,
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
  const [isTestingKeys, setIsTestingKeys] = useState(false)
  const [isMarkedForClear, setIsMarkedForClear] = useState(false)
  const [keyTestResults, setKeyTestResults] = useState<
    ModerationKeyTestResult[] | null
  >(null)

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
      postflight_enabled: data.postflight_enabled ?? false,
      failure_mode: data.failure_mode ?? 'closed',
      timeout_seconds: data.timeout_seconds ?? 30,
      max_retries: data.max_retries ?? 3,
      auto_disable_violations: data.auto_disable_violations ?? 0,
    })
    setModerationKey(null)
    setKeyTestResults(null)
    setIsMarkedForClear(false)
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

  const handleTestKeys = useCallback(async () => {
    const values = form.getValues()
    const hasDraftKeys = values.api_key.trim() !== ''
    const hasSavedKeys =
      Boolean(query.data?.data.api_key_configured) && !isMarkedForClear
    if (!hasDraftKeys && !hasSavedKeys) {
      toast.error(t('No moderation API keys to test'))
      return
    }
    setIsTestingKeys(true)
    setKeyTestResults(null)
    try {
      const res = await testContentModerationKeys({
        base_url: values.base_url,
        model: values.model,
        api_key: values.api_key,
      })
      if (!res.success) {
        throw new Error(res.message || t('Failed to test moderation keys'))
      }
      const results = res.data?.results ?? []
      setKeyTestResults(results)
      const passed = results.filter((result) => result.ok).length
      if (results.length > 0 && passed === results.length) {
        toast.success(t('All moderation keys passed'))
      } else {
        toast.error(
          t('{{passed}} of {{total}} keys passed', {
            passed,
            total: results.length,
          })
        )
      }
    } catch (error) {
      toast.error(
        error instanceof Error
          ? error.message
          : t('Failed to test moderation keys')
      )
    } finally {
      setIsTestingKeys(false)
    }
  }, [form, isMarkedForClear, query.data?.data.api_key_configured, t])

  const onSubmit = async (values: ContentModerationFormValues) => {
    const isClearing = isMarkedForClear && values.api_key.trim() === ''
    const hasKey =
      values.api_key.trim() !== '' ||
      (Boolean(query.data?.data.api_key_configured) && !isClearing)
    if (values.enabled && !hasKey) {
      form.setError('api_key', {
        type: 'required',
        message: t(
          'A moderation API key is required when content moderation is enabled.'
        ),
      })
      return
    }
    await mutation.mutateAsync({
      ...values,
      clear_api_key: isClearing,
    })
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
              render={({ field }) => {
                const apiKeyCount = query.data?.data.api_key_count ?? 0
                const apiKeyConfigured = Boolean(
                  query.data?.data.api_key_configured
                )
                const effectivelyConfigured =
                  apiKeyConfigured && !isMarkedForClear
                let keysDescription = t(
                  'Enter one moderation API key per line. Requests rotate across keys to spread rate limits.'
                )
                if (effectivelyConfigured && apiKeyCount > 1) {
                  keysDescription = t(
                    '{{count}} keys currently configured. Leave blank to keep current keys.',
                    { count: apiKeyCount }
                  )
                } else if (effectivelyConfigured) {
                  keysDescription = t(
                    'A moderation API key is currently configured. Leave blank to keep current key.'
                  )
                }
                return (
                  <FormItem>
                    <div className='flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between'>
                      <FormLabel>{t('Moderation API keys')}</FormLabel>
                      <div className='flex items-center gap-2'>
                        {effectivelyConfigured && (
                          <Button
                            type='button'
                            variant='outline'
                            size='sm'
                            className='text-destructive hover:text-destructive'
                            onClick={() => {
                              if (
                                window.confirm(
                                  t(
                                    'Are you sure you want to clear the configured moderation API keys?'
                                  )
                                )
                              ) {
                                setIsMarkedForClear(true)
                                form.setValue('api_key', '')
                                setModerationKey(null)
                                setKeyTestResults(null)
                                toast.info(
                                  t(
                                    'Moderation API keys cleared. Click Save to apply changes.'
                                  )
                                )
                              }
                            }}
                          >
                            <Trash2 className='mr-2 h-4 w-4' />
                            {t('Clear keys')}
                          </Button>
                        )}
                        {isMarkedForClear && (
                          <div className='flex items-center gap-1.5'>
                            <span className='text-destructive text-xs font-medium'>
                              {t('Keys will be cleared on save')}
                            </span>
                            <Button
                              type='button'
                              variant='ghost'
                              size='sm'
                              onClick={() => {
                                setIsMarkedForClear(false)
                              }}
                            >
                              <RotateCcw className='mr-1.5 h-3.5 w-3.5' />
                              {t('Undo clear')}
                            </Button>
                          </div>
                        )}
                        <Button
                          type='button'
                          variant='outline'
                          size='sm'
                          onClick={handleTestKeys}
                          disabled={
                            isTestingKeys ||
                            (field.value.trim() === '' && !effectivelyConfigured)
                          }
                        >
                          {isTestingKeys ? (
                            <Loader2 className='mr-2 h-4 w-4 animate-spin' />
                          ) : null}
                          {t('Test keys')}
                        </Button>
                      </div>
                    </div>
                    <FormControl>
                      <Textarea
                        autoComplete='off'
                        spellCheck={false}
                        rows={4}
                        className='font-mono text-xs'
                        placeholder={
                          effectivelyConfigured
                            ? t('Leave empty to keep existing keys')
                            : t('One API key per line')
                        }
                        {...field}
                        onChange={(e) => {
                          field.onChange(e)
                          if (e.target.value.trim() !== '' && isMarkedForClear) {
                            setIsMarkedForClear(false)
                          }
                        }}
                      />
                    </FormControl>
                    <FormDescription>{keysDescription}</FormDescription>
                    <FormMessage />

                    {keyTestResults && keyTestResults.length > 0 && (
                      <ul className='border-border/60 mt-3 space-y-2 rounded-lg border p-3'>
                        {keyTestResults.map((result) => (
                          <li
                            key={`${result.index}-${result.key_preview}`}
                            className='flex items-start gap-2 text-sm'
                          >
                            {result.ok ? (
                              <CheckCircle2 className='mt-0.5 h-4 w-4 shrink-0 text-green-600' />
                            ) : (
                              <XCircle className='text-destructive mt-0.5 h-4 w-4 shrink-0' />
                            )}
                            <div className='min-w-0 flex-1'>
                              <p className='font-mono text-xs'>
                                {result.key_preview}
                                <span className='text-muted-foreground ml-2'>
                                  {result.latency_ms}ms
                                </span>
                              </p>
                              {result.error ? (
                                <p className='text-destructive mt-1 text-xs break-all'>
                                  {result.error}
                                </p>
                              ) : null}
                            </div>
                          </li>
                        ))}
                      </ul>
                    )}

                    {apiKeyConfigured && (
                      <div className='border-border/60 mt-3 flex flex-col gap-3 rounded-lg border border-dashed p-3'>
                        <div className='flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between'>
                          <div>
                            <p className='text-sm font-medium'>
                              {t('Current keys')}
                            </p>
                            <p className='text-muted-foreground text-xs'>
                              {t(
                                'Verification required to reveal the saved keys.'
                              )}
                            </p>
                          </div>
                          <div className='flex items-center gap-2'>
                            <Button
                              type='button'
                              variant='outline'
                              size='sm'
                              onClick={handleRevealKey}
                              disabled={
                                isKeyLoading || verificationState.loading
                              }
                            >
                              {isKeyLoading || verificationState.loading ? (
                                <Loader2 className='mr-2 h-4 w-4 animate-spin' />
                              ) : (
                                <Eye className='mr-2 h-4 w-4' />
                              )}
                              {t('Reveal keys')}
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
                          <div className='flex items-start gap-2'>
                            <Textarea
                              readOnly
                              value={moderationKey}
                              rows={Math.min(
                                8,
                                Math.max(2, moderationKey.split('\n').length)
                              )}
                              className='font-mono text-xs'
                            />
                            <Button
                              type='button'
                              variant='outline'
                              size='icon-sm'
                              onClick={() => {
                                form.setValue('api_key', moderationKey)
                                setIsMarkedForClear(false)
                                toast.success(t('Keys loaded into editor'))
                              }}
                              aria-label={t('Load keys to editor')}
                              title={t('Load keys to editor')}
                            >
                              <Pencil className='h-4 w-4' />
                            </Button>
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
                )
              }}
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
                        'Use the OpenAI Moderations API on the latest user message before forwarding a request. Disable only for audit-only mode.'
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
              name='postflight_enabled'
              render={({ field }) => (
                <SettingsSwitchItem>
                  <SettingsSwitchContent>
                    <FormLabel>
                      {t('Review assistant replies after the response')}
                    </FormLabel>
                    <FormDescription>
                      {t(
                        'Optional. Only flagged assistant output is stored as a truncated excerpt. This does not block the current request.'
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

            <div className='grid gap-4 sm:grid-cols-2 lg:grid-cols-4'>
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
                    <FormDescription>
                      {t(
                        'Retries switch to the next key when the provider rate-limits or returns a server error.'
                      )}
                    </FormDescription>
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
              <FormField
                control={form.control}
                name='auto_disable_violations'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>
                      {t('Auto-disable after N violations')}
                    </FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={0}
                        max={1000}
                        step={1}
                        {...field}
                        onChange={numberField(field)}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        '0 keeps auto-disable off. Repeat user violations within the retention window can disable the account and its tokens.'
                      )}
                    </FormDescription>
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
                      'These user IDs completely bypass content moderation: their requests are not reviewed and no violation events are saved. Separate IDs with commas or spaces. The root administrator (ID: 1) is always excluded.'
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
