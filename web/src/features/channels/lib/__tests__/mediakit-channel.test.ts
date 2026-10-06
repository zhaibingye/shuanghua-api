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

import { CHANNEL_TYPE_TASK_PLUGIN, MEDIAKIT_PLUGIN_KEY } from '../../constants'
import {
  CHANNEL_FORM_DEFAULT_VALUES,
  channelFormSchema,
  transformFormDataToCreatePayload,
} from '../channel-form'
import { composeMediaKitKey, parseMediaKitKey } from '../mediakit-key'

function mediaKitForm() {
  return {
    ...CHANNEL_FORM_DEFAULT_VALUES,
    name: 'Seedance upscale',
    type: CHANNEL_TYPE_TASK_PLUGIN,
    task_plugin_key: MEDIAKIT_PLUGIN_KEY,
    base_url: 'https://ark.cn-beijing.volces.com',
    ark_api_key: 'ark-key',
    mediakit_api_key: 'mediakit-key',
    models: 'doubao-seedance-2-0-260128',
  }
}

describe('DoubaoVideoMediaKit channel', () => {
  test('preserves the explicit plugin binding in the stored settings', () => {
    const payload = transformFormDataToCreatePayload(mediaKitForm())
    expect(JSON.parse(payload.channel.setting ?? '{}').task_plugin_key).toBe(
      MEDIAKIT_PLUGIN_KEY
    )
  })

  test('requires a Base URL and rejects batch creation', () => {
    const blankResult = channelFormSchema.safeParse({
      ...mediaKitForm(),
      base_url: '  ',
    })
    expect(blankResult.success).toBe(false)

    const batchResult = channelFormSchema.safeParse({
      ...mediaKitForm(),
      multi_key_mode: 'batch',
    })
    expect(batchResult.success).toBe(false)

    expect(channelFormSchema.safeParse(mediaKitForm()).success).toBe(true)
  })

  test('composes the two keys into one stored credential', () => {
    const payload = transformFormDataToCreatePayload(mediaKitForm())
    expect(payload.mode).toBe('single')
    expect(payload.channel.key).toBe(
      composeMediaKitKey('ark-key', 'mediakit-key')
    )
    expect(parseMediaKitKey(payload.channel.key ?? '')).toEqual({
      ark_api_key: 'ark-key',
      mediakit_api_key: 'mediakit-key',
    })
  })
})
