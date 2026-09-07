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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, test, vi } from 'vitest'

import { SettingsPageProvider } from '../../components/settings-page-context'
import type { ContentModerationSettingsResponse } from '../../types'
import { ContentModerationSection } from '../content-moderation-section'

const mocks = vi.hoisted(() => ({
  getContentModerationSettings: vi.fn(),
  getContentModerationKey: vi.fn(),
  testContentModerationKeys: vi.fn(),
  updateContentModerationSettings: vi.fn(),
}))

vi.mock('../../api', () => mocks)

vi.mock('@/features/auth/secure-verification', () => ({
  SecureVerificationDialog: () => null,
  useSecureVerification: () => ({
    open: false,
    methods: { has2FA: false, hasPasskey: false, passkeySupported: false },
    state: { method: null, loading: false, code: '' },
    executeVerification: vi.fn(),
    withVerification: vi.fn((fn: (token?: string) => Promise<unknown>) => fn()),
    cancel: vi.fn(),
    setCode: vi.fn(),
    switchMethod: vi.fn(),
  }),
}))

const settingsResponse: ContentModerationSettingsResponse = {
  success: true,
  data: {
    enabled: false,
    channels: '',
    user_whitelist: '1',
    violation_retention_days: 7,
    base_url: '',
    model: 'omni-moderation-latest',
    timeout_seconds: 30,
    max_retries: 3,
    api_key_configured: false,
  },
}

function renderSettings(response = settingsResponse) {
  mocks.getContentModerationSettings.mockResolvedValue(response)
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  const actionsContainer = document.createElement('div')
  document.body.append(actionsContainer)
  const view = render(
    <QueryClientProvider client={queryClient}>
      <SettingsPageProvider actionsContainer={actionsContainer}>
        <ContentModerationSection />
      </SettingsPageProvider>
    </QueryClientProvider>
  )
  return { ...view, actionsContainer, queryClient }
}

describe('content moderation settings', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.getContentModerationSettings.mockResolvedValue(settingsResponse)
    mocks.updateContentModerationSettings.mockResolvedValue({ success: true })
  })

  test('renders the moderation model and base URL settings', async () => {
    const { actionsContainer } = renderSettings()

    const modelInput = await screen.findByLabelText('Moderation model')
    expect(modelInput).toHaveValue('omni-moderation-latest')

    const baseURLInput = await screen.findByLabelText('Moderation API base URL')
    expect(baseURLInput).toHaveAttribute(
      'placeholder',
      'https://api.openai.com/v1'
    )

    actionsContainer.remove()
  })

  test('shows a validation error before saving enabled moderation without an API key', async () => {
    const enabledResponse: ContentModerationSettingsResponse = {
      ...settingsResponse,
      data: { ...settingsResponse.data, enabled: true },
    }
    const { actionsContainer } = renderSettings(enabledResponse)

    await screen.findByLabelText('Moderation API keys')
    const saveButton = await screen.findByRole('button', {
      name: 'Save content moderation settings',
    })
    fireEvent.click(saveButton)

    expect(
      await screen.findByText(
        'A moderation API key is required when content moderation is enabled.'
      )
    ).toBeInTheDocument()
    expect(mocks.updateContentModerationSettings).not.toHaveBeenCalled()
    actionsContainer.remove()
  })

  test('reveals the configured API key on click', async () => {
    const configuredResponse: ContentModerationSettingsResponse = {
      ...settingsResponse,
      data: {
        ...settingsResponse.data,
        api_key_configured: true,
        api_key_count: 2,
      },
    }
    mocks.getContentModerationKey.mockResolvedValue({
      success: true,
      data: { key: 'sk-revealed-moderation-key' },
    })

    const { actionsContainer } = renderSettings(configuredResponse)

    const revealButton = await screen.findByRole('button', {
      name: 'Reveal keys',
    })
    fireEvent.click(revealButton)

    expect(
      await screen.findByDisplayValue('sk-revealed-moderation-key')
    ).toBeInTheDocument()
    actionsContainer.remove()
  })

  test('tests configured moderation keys', async () => {
    const configuredResponse: ContentModerationSettingsResponse = {
      ...settingsResponse,
      data: {
        ...settingsResponse.data,
        api_key_configured: true,
        api_key_count: 1,
      },
    }
    mocks.testContentModerationKeys.mockResolvedValue({
      success: true,
      data: {
        results: [
          {
            index: 0,
            key_preview: 'sk-t...lpha',
            ok: true,
            status: 200,
            latency_ms: 42,
          },
        ],
      },
    })

    const { actionsContainer } = renderSettings(configuredResponse)
    const testButton = await screen.findByRole('button', { name: 'Test keys' })
    fireEvent.click(testButton)

    expect(await screen.findByText('sk-t...lpha')).toBeInTheDocument()
    expect(mocks.testContentModerationKeys).toHaveBeenCalled()
    actionsContainer.remove()
  })

  test('allows clearing configured keys and submitting clear_api_key', async () => {
    const configuredResponse: ContentModerationSettingsResponse = {
      ...settingsResponse,
      data: {
        ...settingsResponse.data,
        api_key_configured: true,
        api_key_count: 1,
      },
    }
    vi.spyOn(window, 'confirm').mockReturnValue(true)

    const { actionsContainer } = renderSettings(configuredResponse)
    const clearButton = await screen.findByRole('button', { name: 'Clear keys' })
    fireEvent.click(clearButton)

    expect(await screen.findByText('Keys will be cleared on save')).toBeInTheDocument()

    const saveButton = await screen.findByRole('button', {
      name: 'Save content moderation settings',
    })
    fireEvent.click(saveButton)

    await waitFor(() => {
      expect(mocks.updateContentModerationSettings.mock.calls[0]?.[0]).toMatchObject({
        clear_api_key: true,
      })
    })
    actionsContainer.remove()
  })

  test('loads revealed keys into editor', async () => {
    const configuredResponse: ContentModerationSettingsResponse = {
      ...settingsResponse,
      data: {
        ...settingsResponse.data,
        api_key_configured: true,
        api_key_count: 1,
      },
    }
    mocks.getContentModerationKey.mockResolvedValue({
      success: true,
      data: { key: 'sk-line-1\nsk-line-2' },
    })

    const { actionsContainer } = renderSettings(configuredResponse)
    const revealButton = await screen.findByRole('button', { name: 'Reveal keys' })
    fireEvent.click(revealButton)

    const loadButton = await screen.findByRole('button', { name: 'Load keys to editor' })
    fireEvent.click(loadButton)

    const textarea = await screen.findByLabelText('Moderation API keys')
    expect(textarea).toHaveValue('sk-line-1\nsk-line-2')
    actionsContainer.remove()
  })
})
