import { describe, it, expect } from 'vitest'
import {
  listEndpoints, resolveParams, resolveSchema, exampleFor, schemaLabel,
  type OpenAPISpec,
} from './openapi'

const spec: OpenAPISpec = {
  openapi: '3.0.0',
  info: { title: 'Test API', version: '1.0.0' },
  paths: {
    '/waypoints': {
      get: {
        operationId: 'listWaypoints',
        summary: 'List waypoints',
        tags: ['Waypoints'],
        parameters: [
          { name: 'limit', in: 'query', schema: { type: 'integer' } },
          { $ref: '#/components/parameters/AccountId' },
        ],
        responses: {
          '200': {
            description: 'ok',
            content: { 'application/json': { schema: { type: 'array', items: { $ref: '#/components/schemas/Waypoint' } } } },
          },
        },
      },
      post: {
        summary: 'Create waypoint',
        // no tags -> should fall into "Other"
        requestBody: {
          content: { 'application/json': { schema: { $ref: '#/components/schemas/Waypoint' } } },
        },
        responses: { '201': { description: 'created' } },
      },
    },
  },
  components: {
    parameters: {
      AccountId: { name: 'accountId', in: 'header', required: true, schema: { type: 'string' } },
    },
    schemas: {
      Waypoint: {
        type: 'object',
        properties: {
          id: { type: 'string', example: 'wp-1' },
          name: { type: 'string' },
          lat: { type: 'number' },
          active: { type: 'boolean' },
          createdAt: { type: 'string', format: 'date-time' },
          kind: { type: 'string', enum: ['buoy', 'dock'] },
        },
      },
    },
  },
}

describe('listEndpoints', () => {
  it('lists every method under every path with an uppercased method and tag', () => {
    const rows = listEndpoints(spec)
    expect(rows).toHaveLength(2)
    const get = rows.find(r => r.method === 'GET')!
    expect(get.path).toBe('/waypoints')
    expect(get.tag).toBe('Waypoints')
    expect(get.id).toBe('get /waypoints')
    const post = rows.find(r => r.method === 'POST')!
    expect(post.tag).toBe('Other') // no tags -> Other
  })
})

describe('resolveParams', () => {
  it('resolves inline params and $ref params', () => {
    const get = spec.paths['/waypoints'].get!
    const params = resolveParams(spec, get)
    expect(params.map(p => p.name)).toEqual(['limit', 'accountId'])
    const accountId = params.find(p => p.name === 'accountId')!
    expect(accountId.in).toBe('header')
    expect(accountId.required).toBe(true)
  })
})

describe('resolveSchema', () => {
  it('follows a $ref to the target schema', () => {
    const resolved = resolveSchema(spec, { $ref: '#/components/schemas/Waypoint' })
    expect(resolved?.type).toBe('object')
    expect(resolved?.properties?.id).toBeDefined()
  })
})

describe('exampleFor', () => {
  it('builds an object example honoring example/enum/format', () => {
    const ex = exampleFor(spec, { $ref: '#/components/schemas/Waypoint' }) as Record<string, unknown>
    expect(ex.id).toBe('wp-1') // uses declared example
    expect(ex.name).toBe('') // string default
    expect(ex.lat).toBe(0) // number default
    expect(ex.active).toBe(false) // boolean default
    expect(ex.kind).toBe('buoy') // first enum value
    expect(typeof ex.createdAt).toBe('string')
    expect((ex.createdAt as string)).toMatch(/T.*Z$/) // ISO date-time
  })

  it('wraps array items', () => {
    const ex = exampleFor(spec, { type: 'array', items: { type: 'integer' } })
    expect(ex).toEqual([0])
  })
})

describe('schemaLabel', () => {
  it('labels refs by their name', () => {
    expect(schemaLabel({ $ref: '#/components/schemas/Waypoint' })).toBe('Waypoint')
  })

  it('labels arrays with a [] suffix', () => {
    expect(schemaLabel({ type: 'array', items: { $ref: '#/components/schemas/Waypoint' } })).toBe('Waypoint[]')
  })

  it('falls back to the type or object', () => {
    expect(schemaLabel({ type: 'string' })).toBe('string')
    expect(schemaLabel({})).toBe('object')
    expect(schemaLabel(undefined)).toBe('')
  })
})
