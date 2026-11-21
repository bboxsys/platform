// Minimal OpenAPI 3 model: only what the explorer renders. Specs are
// authored in-repo, so unsupported constructs are simply not used.

export interface OpenAPISpec {
  openapi: string
  info: { title: string; version: string; description?: string }
  servers?: { url: string }[]
  tags?: { name: string; description?: string }[]
  paths: Record<string, PathItem>
  components?: {
    schemas?: Record<string, Schema>
    securitySchemes?: Record<string, unknown>
    parameters?: Record<string, Parameter>
  }
}

export interface PathItem {
  [method: string]: Operation | undefined
}

export interface Operation {
  operationId?: string
  summary?: string
  description?: string
  tags?: string[]
  deprecated?: boolean
  security?: unknown[]
  parameters?: (Parameter | Ref)[]
  requestBody?: {
    description?: string
    required?: boolean
    content?: Record<string, { schema?: Schema }>
  }
  responses?: Record<string, { description?: string; content?: Record<string, { schema?: Schema }> }>
}

export interface Parameter {
  name: string
  in: 'query' | 'path' | 'header' | 'cookie'
  description?: string
  required?: boolean
  schema?: Schema
  example?: unknown
}

export interface Ref {
  $ref: string
}

export interface Schema {
  $ref?: string
  type?: string
  format?: string
  enum?: unknown[]
  items?: Schema
  properties?: Record<string, Schema>
  additionalProperties?: Schema | boolean
  required?: string[]
  description?: string
  example?: unknown
  default?: unknown
  nullable?: boolean
  oneOf?: Schema[]
  allOf?: Schema[]
}

export interface EndpointRow {
  id: string
  method: string
  path: string
  op: Operation
  tag: string
}

const METHODS = ['get', 'post', 'put', 'patch', 'delete', 'head', 'options']

export function listEndpoints(spec: OpenAPISpec): EndpointRow[] {
  const rows: EndpointRow[] = []
  for (const [path, item] of Object.entries(spec.paths ?? {})) {
    for (const method of METHODS) {
      const op = item[method]
      if (!op) continue
      rows.push({ id: `${method} ${path}`, method: method.toUpperCase(), path, op, tag: op.tags?.[0] ?? 'Other' })
    }
  }
  return rows
}

export function resolveRef<T>(spec: OpenAPISpec, ref: string): T | undefined {
  const parts = ref.replace(/^#\//, '').split('/')
  let cur: unknown = spec
  for (const p of parts) {
    if (cur && typeof cur === 'object') cur = (cur as Record<string, unknown>)[p]
    else return undefined
  }
  return cur as T
}

export function resolveSchema(spec: OpenAPISpec, schema?: Schema, depth = 0): Schema | undefined {
  if (!schema || depth > 12) return schema
  if (schema.$ref) {
    const target = resolveRef<Schema>(spec, schema.$ref)
    return resolveSchema(spec, target, depth + 1)
  }
  return schema
}

export function resolveParams(spec: OpenAPISpec, op: Operation): Parameter[] {
  return (op.parameters ?? []).map(p => ('$ref' in p ? resolveRef<Parameter>(spec, (p as Ref).$ref) : (p as Parameter)))
    .filter((p): p is Parameter => !!p)
}

// Builds an example value for a schema, honoring example/default/enum.
export function exampleFor(spec: OpenAPISpec, schema?: Schema, depth = 0): unknown {
  const s = resolveSchema(spec, schema, depth)
  if (!s || depth > 8) return null
  if (s.example !== undefined) return s.example
  if (s.default !== undefined) return s.default
  if (s.enum?.length) return s.enum[0]
  if (s.oneOf?.length) return exampleFor(spec, s.oneOf[0], depth + 1)
  if (s.allOf?.length) {
    const merged: Record<string, unknown> = {}
    for (const part of s.allOf) {
      const v = exampleFor(spec, part, depth + 1)
      if (v && typeof v === 'object') Object.assign(merged, v)
    }
    return merged
  }
  switch (s.type) {
    case 'string':
      if (s.format === 'date-time') return new Date().toISOString()
      if (s.format === 'date') return new Date().toISOString().slice(0, 10)
      return ''
    case 'integer':
    case 'number':
      return 0
    case 'boolean':
      return false
    case 'array':
      return [exampleFor(spec, s.items, depth + 1)]
    case 'object':
    default: {
      if (!s.properties) return {}
      const obj: Record<string, unknown> = {}
      for (const [k, v] of Object.entries(s.properties)) obj[k] = exampleFor(spec, v, depth + 1)
      return obj
    }
  }
}

// Short human label for a schema, e.g. "Waypoint[]" or "object".
export function schemaLabel(schema?: Schema): string {
  if (!schema) return ''
  if (schema.$ref) return schema.$ref.split('/').pop() ?? ''
  if (schema.type === 'array') return `${schemaLabel(schema.items)}[]`
  return schema.type ?? 'object'
}

export const METHOD_TONES: Record<string, string> = {
  GET: 'bg-sky-500/15 text-sky-700 dark:text-sky-400 border-sky-500/30',
  POST: 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-400 border-emerald-500/30',
  PUT: 'bg-amber-500/15 text-amber-700 dark:text-amber-400 border-amber-500/30',
  PATCH: 'bg-amber-500/15 text-amber-700 dark:text-amber-400 border-amber-500/30',
  DELETE: 'bg-red-500/15 text-red-700 dark:text-red-400 border-red-500/30',
}
