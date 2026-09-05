/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.
*/
import assert from 'node:assert/strict'

import { describe, test } from 'vitest'

import {
  evaluateTaskUsageExamples,
  evaluateTaskVisualConfig,
  generateTaskExprFromConfig,
  tryParseTaskVisualConfig,
} from '../lib/task-expr'
import type { BillingUsageSchema } from '../types'

const doubaoMediaKitSchema: BillingUsageSchema = {
  enhancement_resolution: { enum: ['none', '720p', '1080p'] },
  enhancement_seconds: { type: 'number', unit: 'second' },
  resolution: { enum: ['480p', '720p', '1080p', '4k'] },
  tokens: { type: 'number', unit: 'token' },
  video_input: { enum: ['none', 'video'] },
}

const doubaoMediaKitConfig = {
  tiers: [
    {
      label: '1080p enhanced',
      conditions: [{ field: 'enhancement_resolution', value: '1080p' }],
      constant: 0,
      unitPrices: { tokens: 10, enhancement_seconds: 0.01 },
    },
    {
      label: '720p enhanced',
      conditions: [{ field: 'enhancement_resolution', value: '720p' }],
      constant: 0,
      unitPrices: { tokens: 10, enhancement_seconds: 0.005 },
    },
    {
      label: 'Ark',
      conditions: [],
      constant: 0,
      unitPrices: { tokens: 10, enhancement_seconds: 0 },
    },
  ],
}

describe('Doubao MediaKit task pricing contract', () => {
  test('round-trips separate token and enhancement-second prices', () => {
    const expression = generateTaskExprFromConfig(
      doubaoMediaKitConfig,
      doubaoMediaKitSchema
    )
    const parsed = tryParseTaskVisualConfig(expression, doubaoMediaKitSchema)

    assert.deepEqual(parsed, doubaoMediaKitConfig)
    assert.match(expression, /u\("tokens"\) \* 10 \/ 1000000/)
    assert.match(expression, /u\("enhancement_seconds"\) \* 0\.01/)
  })

  test('uses final resolution to price enhancement without charging ordinary Ark output', () => {
    const enhanced = evaluateTaskVisualConfig(
      doubaoMediaKitConfig,
      {
        enhancement_resolution: '1080p',
        enhancement_seconds: 4,
        tokens: 40000,
      },
      doubaoMediaKitSchema
    )
    const ordinary = evaluateTaskVisualConfig(
      doubaoMediaKitConfig,
      { enhancement_resolution: 'none', enhancement_seconds: 0, tokens: 40000 },
      doubaoMediaKitSchema
    )

    assert.equal(enhanced?.tier.label, '1080p enhanced')
    assert.equal(enhanced?.total, 0.44)
    assert.equal(ordinary?.tier.label, 'Ark')
    assert.equal(ordinary?.total, 0.4)
  })

  test('renders provider examples as USD task totals', () => {
    const expression = generateTaskExprFromConfig(
      doubaoMediaKitConfig,
      doubaoMediaKitSchema
    )
    const totals = evaluateTaskUsageExamples(expression, doubaoMediaKitSchema, [
      {
        label: 'ordinary',
        facts: {
          tokens: 40000,
          resolution: '480p',
          video_input: 'none',
          enhancement_seconds: 0,
          enhancement_resolution: 'none',
        },
      },
      {
        label: 'MediaKit',
        facts: {
          tokens: 40000,
          resolution: '480p',
          video_input: 'none',
          enhancement_seconds: 4,
          enhancement_resolution: '1080p',
        },
      },
    ])

    assert.deepEqual(totals, [
      { label: 'ordinary', total: 0.4 },
      { label: 'MediaKit', total: 0.44 },
    ])
  })
})
