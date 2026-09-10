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
import { describe, expect, test } from 'vitest'

import { channelSchema } from '../../types'
import {
  CHANNEL_FORM_DEFAULT_VALUES,
  channelFormSchema,
  buildModelPreviewRequest,
  transformChannelToFormDefaults,
  transformFormDataToCreatePayload,
  transformFormDataToUpdatePayload,
} from '../channel-form'

function savedChannel(type: number, settings: string) {
  return channelSchema.parse({
    id: 1,
    type,
    settings,
    name: 'OpenCode Go',
    key: '',
    status: 1,
    created_time: 0,
    test_time: 0,
    response_time: 0,
    balance_updated_time: 0,
    models: 'test-model',
  })
}

describe('OpenCode Go channel settings', () => {
  test.each([1, 14])(
    'type %i saves, reloads, and copies the enabled setting',
    (type) => {
      const form = channelFormSchema.parse({
        ...CHANNEL_FORM_DEFAULT_VALUES,
        name: 'OpenCode Go',
        models: 'test-model',
        type,
        opencode_go_compat: true,
        settings: '{"future_setting":"keep"}',
      })
      const created = transformFormDataToCreatePayload(form)
      expect(JSON.parse(created.channel.settings ?? '{}')).toMatchObject({
        opencode_go_compat: true,
        future_setting: 'keep',
      })
      expect(created.channel).not.toHaveProperty('opencode_go_compat')
      const reloaded = transformChannelToFormDefaults(
        savedChannel(type, created.channel.settings ?? '{}')
      )
      expect(reloaded.opencode_go_compat).toBe(true)
      const copied = transformFormDataToCreatePayload(reloaded)
      expect(
        JSON.parse(copied.channel.settings ?? '{}').opencode_go_compat
      ).toBe(true)
      const disabled = transformFormDataToUpdatePayload(
        { ...reloaded, opencode_go_compat: false },
        1
      )
      expect(JSON.parse(disabled.settings ?? '{}')).not.toHaveProperty(
        'opencode_go_compat'
      )
      expect(JSON.parse(disabled.settings ?? '{}').future_setting).toBe('keep')
    }
  )

  test('legacy channels default to disabled and do not add an enabled setting on save', () => {
    const defaults = transformChannelToFormDefaults(savedChannel(1, '{}'))
    expect(defaults.opencode_go_compat).toBe(false)
    const saved = transformFormDataToUpdatePayload(defaults, 1)
    expect(JSON.parse(saved.settings ?? '{}')).not.toHaveProperty(
      'opencode_go_compat'
    )
  })

  test('switching to an unsupported channel clears stale compatibility settings', () => {
    const defaults = transformChannelToFormDefaults(
      savedChannel(1, '{"opencode_go_compat":true}')
    )
    const saved = transformFormDataToUpdatePayload({ ...defaults, type: 3 }, 1)
    expect(JSON.parse(saved.settings ?? '{}')).not.toHaveProperty(
      'opencode_go_compat'
    )
    expect(
      transformChannelToFormDefaults(
        savedChannel(3, '{"opencode_go_compat":true}')
      ).opencode_go_compat
    ).toBe(false)
  })

  test('model discovery previews unsaved enable and disable changes while keeping saved keys server-side', () => {
    const form = {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      type: 1,
      key: 'create-key',
      opencode_go_compat: true,
    }
    expect(buildModelPreviewRequest(form)).toMatchObject({
      key: 'create-key',
      opencode_go_compat: true,
    })
    const edit = buildModelPreviewRequest(
      { ...form, opencode_go_compat: false },
      7
    )
    expect(edit).toMatchObject({ channel_id: 7, opencode_go_compat: false })
    expect(edit.key).toBeUndefined()
    expect(
      buildModelPreviewRequest({ ...form, type: 3 }).opencode_go_compat
    ).toBeUndefined()
  })

  test('string values are not treated as enabled booleans', () => {
    expect(
      transformChannelToFormDefaults(
        savedChannel(1, '{"opencode_go_compat":"true"}')
      ).opencode_go_compat
    ).toBe(false)
  })
})
