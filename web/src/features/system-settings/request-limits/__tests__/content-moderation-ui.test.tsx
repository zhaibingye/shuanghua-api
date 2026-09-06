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
  ModerationConversationDetail,
  ModerationConversationListResponse,
  ModerationUserListResponse,
} from '../../types'
import {
  ConversationDetail,
  ContentModerationRecordsSection,
} from '../content-moderation-records-section'
import { ContentModerationUsersSection } from '../content-moderation-users-section'

const mocks = vi.hoisted(() => ({
  deleteContentModerationUserHistory: vi.fn(),
  getContentModerationConversation: vi.fn(),
  listContentModerationConversations: vi.fn(),
  getContentModerationUser: vi.fn(),
  listContentModerationUsers: vi.fn(),
  resolveContentModerationViolation: vi.fn(),
  restoreContentModerationUser: vi.fn(),
  unblockContentModerationConversation: vi.fn(),
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

const conversationListResponse: ModerationConversationListResponse = {
  success: true,
  data: [],
  total: 250,
}

const conversationDetail: ModerationConversationDetail = {
  conversation: {
    id: 1,
    user_id: 2,
    conversation_id: 'conversation-1',
    status: 'active',
    first_activity_at: 100,
    last_activity_at: 200,
    expires_at: 300,
  },
  turns: [
    {
      id: 1,
      round_number: 1,
      request_id: 'request-1',
      system_prompt: 'system instructions',
      user_prompt: 'user instructions',
      assistant_reply: 'assistant response',
      response_status: 'success',
      relay_format: 'responses',
      model: 'moderation-model',
      review_required: true,
      created_at: 200,
    },
  ],
  jobs: [],
  violations: [],
  actions: [],
  notifications: [],
}

describe('content moderation UI', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.listContentModerationUsers.mockResolvedValue(emptyUsersResponse)
    mocks.listContentModerationConversations.mockResolvedValue(
      conversationListResponse
    )
  })

  test('uses the usage-log filter and pagination controls for moderation records', async () => {
    renderWithQueryClient(<ContentModerationRecordsSection />)

    await vi.waitFor(() =>
      expect(mocks.listContentModerationConversations).toHaveBeenCalled()
    )
    expect(
      await screen.findByText('No moderation conversations found.')
    ).toBeInTheDocument()
    expect(screen.getAllByText('250')).not.toHaveLength(0)
    const user = userEvent.setup()
    const page2Button = screen.getByRole('button', { name: 'Go to page 2' })
    expect(page2Button).toBeInTheDocument()
    await user.click(page2Button)
    await vi.waitFor(() =>
      expect(mocks.listContentModerationConversations).toHaveBeenLastCalledWith(
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
      expect(mocks.listContentModerationConversations).toHaveBeenLastCalledWith(
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

  test('renders moderation turn content in a neutral project-style panel', () => {
    renderWithQueryClient(
      <ConversationDetail
        detail={conversationDetail}
        onRefresh={vi.fn()}
        onClose={vi.fn()}
      />
    )

    const systemPrompt = screen.getByText('system instructions')
    const panel = systemPrompt.closest('.bg-card')

    expect(panel).not.toBeNull()
    expect(panel).toHaveClass('bg-card')
    expect(panel).not.toHaveClass('border-sky-500/25')
    expect(screen.getByText('user instructions')).toBeInTheDocument()
    expect(screen.getByText('assistant response')).toBeInTheDocument()
  })

  test('shows a safe marker when moderation content cannot be decrypted', () => {
    const unavailableDetail: ModerationConversationDetail = {
      ...conversationDetail,
      turns: conversationDetail.turns.map((turn) => ({
        ...turn,
        system_prompt: '',
        user_prompt: '',
        assistant_reply: '',
        content_unavailable: true,
      })),
    }
    renderWithQueryClient(
      <ConversationDetail
        detail={unavailableDetail}
        onRefresh={vi.fn()}
        onClose={vi.fn()}
      />
    )

    expect(screen.getAllByText('Content unavailable')).toHaveLength(3)
  })
})
