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
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, test, vi } from 'vitest'

import type {
  ModerationEvent,
  ModerationEventListResponse,
  ModerationUserListResponse,
} from '../../types'
import {
  ContentModerationRecordsSection,
  EventDetail,
} from '../content-moderation-records-section'
import { ContentModerationUsersSection } from '../content-moderation-users-section'

const mocks = vi.hoisted(() => ({
  deleteContentModerationUserHistory: vi.fn(),
  listContentModerationEvents: vi.fn(),
  getContentModerationUser: vi.fn(),
  listContentModerationUsers: vi.fn(),
  resolveContentModerationEvent: vi.fn(),
  restoreContentModerationUser: vi.fn(),
  updateContentModerationUser: vi.fn(),
  updateContentModerationUserStatus: vi.fn(),
}))

vi.mock('../../api', () => mocks)

function renderWithQueryClient(children: ReactNode) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  )
}

const emptyUsersResponse: ModerationUserListResponse = {
  success: true,
  data: [],
  total: 0,
}

const eventListResponse: ModerationEventListResponse = {
  success: true,
  data: [],
  total: 250,
}

const eventDetail: ModerationEvent = {
  id: 1,
  user_id: 2,
  request_id: 'request-1',
  model: 'gpt-4o-mini',
  relay_format: 'openai',
  source: 'preflight',
  actor: 'user',
  decision: 'block',
  severity: 'high',
  categories: '["violence"]',
  confidence: 0.91,
  reason_code: 'violence',
  user_excerpt: 'user instructions',
  assistant_excerpt: '',
  image_count: 0,
  status: 'active',
  created_at: 200,
  expires_at: 300,
}

describe('content moderation UI', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.listContentModerationUsers.mockResolvedValue(emptyUsersResponse)
    mocks.listContentModerationEvents.mockResolvedValue(eventListResponse)
  })

  test('uses the usage-log filter and pagination controls for moderation records', async () => {
    renderWithQueryClient(<ContentModerationRecordsSection />)

    await vi.waitFor(() =>
      expect(mocks.listContentModerationEvents).toHaveBeenCalled()
    )
    expect(
      await screen.findByText('No moderation events found.')
    ).toBeInTheDocument()
    expect(screen.getAllByText('250')).not.toHaveLength(0)
    const user = userEvent.setup()
    const page2Button = screen.getByRole('button', { name: 'Go to page 2' })
    expect(page2Button).toBeInTheDocument()
    await user.click(page2Button)
    await vi.waitFor(() =>
      expect(mocks.listContentModerationEvents).toHaveBeenLastCalledWith(
        expect.objectContaining({ limit: 20, offset: 20 })
      )
    )

    const pageSizeSelect = screen.getAllByRole('combobox').at(-1)
    if (!pageSizeSelect) throw new Error('page size selector is missing')
    await user.click(pageSizeSelect)
    expect(
      await screen.findByRole('option', { name: '100' })
    ).toBeInTheDocument()
    await user.click(screen.getByRole('option', { name: '100' }))

    await vi.waitFor(() =>
      expect(mocks.listContentModerationEvents).toHaveBeenLastCalledWith(
        expect.objectContaining({ limit: 100, offset: 0 })
      )
    )
  })

  test('does not render the removed violating-user notes banner', async () => {
    renderWithQueryClient(<ContentModerationUsersSection />)

    await vi.waitFor(() =>
      expect(mocks.listContentModerationUsers).toHaveBeenCalled()
    )
    expect(
      await screen.findByText('No active violating users found.')
    ).toBeInTheDocument()
    expect(screen.queryByText('Moderation user notes')).not.toBeInTheDocument()
  })

  test('renders moderation excerpts in a neutral project-style panel', () => {
    renderWithQueryClient(
      <EventDetail event={eventDetail} onRefresh={vi.fn()} onClose={vi.fn()} />
    )

    const userExcerpt = screen.getByText('user instructions')
    const panel = userExcerpt.closest('.bg-card')

    expect(panel).not.toBeNull()
    expect(panel).toHaveClass('bg-card')
    expect(panel).not.toHaveClass('border-sky-500/25')
  })
})
