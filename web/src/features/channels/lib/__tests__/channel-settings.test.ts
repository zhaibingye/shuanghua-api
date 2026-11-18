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

import { CHANNEL_TYPE_TASK_PLUGIN } from '../../constants'
import { channelSchema } from '../../types'
import {
  CHANNEL_FORM_DEFAULT_VALUES,
  buildModelPreviewRequest,
  transformChannelToFormDefaults,
  transformFormDataToCreatePayload,
  transformFormDataToUpdatePayload,
} from '../channel-form'

describe('channel settings payload', () => {
  test('drops the retired flag when saving or copying a legacy channel', () => {
    const channel = channelSchema.parse({
      id: 1,
      type: 1,
      settings: '{"opencode_go_compat":true,"future_setting":"keep"}',
      name: 'Legacy channel',
      key: '',
      status: 1,
      created_time: 0,
      test_time: 0,
      response_time: 0,
      balance_updated_time: 0,
      models: 'test-model',
    })
    const form = transformChannelToFormDefaults(channel)

    const updated = transformFormDataToUpdatePayload(form, channel.id)
    const copied = transformFormDataToCreatePayload(form)

    for (const payload of [updated, copied.channel]) {
      const settings = JSON.parse(payload.settings ?? '{}')
      expect(settings).not.toHaveProperty('opencode_go_compat')
      expect(settings.future_setting).toBe('keep')
    }
  })
})

describe('model discovery preview', () => {
  test('uses the saved channel key for edits and the entered key for new channels', () => {
    const form = {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      type: 1,
      key: 'create-key',
    }
    expect(buildModelPreviewRequest(form).key).toBe('create-key')
    const edit = buildModelPreviewRequest(form, 7)
    expect(edit.channel_id).toBe(7)
    expect(edit.key).toBeUndefined()
  })

  test('includes the bound plugin only for plugin channels', () => {
    const form = {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      type: CHANNEL_TYPE_TASK_PLUGIN,
      task_plugin_key: 'vendor',
    }
    expect(buildModelPreviewRequest(form).task_plugin_key).toBe('vendor')
    expect(
      buildModelPreviewRequest({ ...form, type: 1 }).task_plugin_key
    ).toBeUndefined()
  })
})
