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
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useForm } from 'react-hook-form'
import { describe, expect, test, vi } from 'vitest'

import { Form } from '@/components/ui/form'

import {
  CHANNEL_FORM_DEFAULT_VALUES,
  transformFormDataToUpdatePayload,
  type ChannelFormValues,
} from '../../../../lib/channel-form'
import { ChannelOpenCodeSetting } from '../channel-opencode-setting'

function Harness(props: {
  type: number
  enabled?: boolean
  disabled?: boolean
  onSave?: (settings: string) => void
}) {
  const form = useForm<ChannelFormValues>({
    defaultValues: {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      type: props.type,
      opencode_go_compat: props.enabled ?? false,
    },
  })
  return (
    <Form {...form}>
      <form
        onSubmit={form.handleSubmit((values) => {
          props.onSave?.(
            transformFormDataToUpdatePayload(values, 1).settings ?? '{}'
          )
        })}
      >
        <ChannelOpenCodeSetting
          control={form.control}
          channelType={props.type}
          disabled={props.disabled ?? false}
        />
        <button type='submit'>Save</button>
      </form>
    </Form>
  )
}

describe('OpenCode Go compatibility switch', () => {
  test.each([1, 14])(
    'type %i supports keyboard toggling and saves the selected value',
    async (type) => {
      const user = userEvent.setup()
      const onSave = vi.fn()
      render(<Harness type={type} onSave={onSave} />)
      const toggle = screen.getByRole('switch', {
        name: 'OpenCode Go compatibility',
      })
      expect(toggle).not.toBeChecked()
      expect(toggle).toHaveAccessibleDescription(/X-Opencode-Session/)
      await user.tab()
      expect(toggle).toHaveFocus()
      await user.keyboard(' ')
      expect(toggle).toBeChecked()
      await user.click(screen.getByRole('button', { name: 'Save' }))
      await waitFor(() => expect(onSave).toHaveBeenCalled())
      expect(JSON.parse(onSave.mock.calls[0][0]).opencode_go_compat).toBe(true)
    }
  )

  test('a saved enabled setting is visible but cannot be changed without permission', async () => {
    const user = userEvent.setup()
    render(<Harness type={1} enabled disabled />)
    const toggle = screen.getByRole('switch', {
      name: 'OpenCode Go compatibility',
    })
    expect(toggle).toBeChecked()
    expect(toggle).toHaveAttribute('aria-disabled', 'true')
    await user.click(toggle)
    expect(toggle).toBeChecked()
    await user.tab()
    expect(screen.getByRole('button', { name: 'Save' })).toHaveFocus()
  })

  test('changing to an unsupported channel hides the switch', () => {
    const view = render(<Harness type={1} enabled />)
    expect(screen.getByRole('switch')).toBeChecked()
    view.rerender(<Harness type={3} enabled />)
    expect(screen.queryByRole('switch')).not.toBeInTheDocument()
    view.rerender(<Harness type={14} enabled />)
    expect(
      screen.getByRole('switch', { name: 'OpenCode Go compatibility' })
    ).toBeChecked()
  })
})
