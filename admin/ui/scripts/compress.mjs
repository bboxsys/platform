// Precompress dist assets so the Go SPA handler can serve .br/.gz variants.
// Usage: node scripts/compress.mjs [distDir]
import { promises as fs } from 'node:fs'
import path from 'node:path'
import zlib from 'node:zlib'
import { promisify } from 'node:util'

const gzip = promisify(zlib.gzip)
const brotli = promisify(zlib.brotliCompress)

const dist = process.argv[2] ?? 'dist'
const exts = new Set(['.js', '.css', '.html', '.svg', '.json', '.txt', '.map', '.wasm', '.xml'])
const MIN_SIZE = 1024

async function* walk(dir) {
  for (const entry of await fs.readdir(dir, { withFileTypes: true })) {
    const p = path.join(dir, entry.name)
    if (entry.isDirectory()) yield* walk(p)
    else yield p
  }
}

let files = 0
let before = 0
let gzTotal = 0
let brTotal = 0
for await (const file of walk(dist)) {
  const ext = path.extname(file)
  if (!exts.has(ext) || file.endsWith('.gz') || file.endsWith('.br')) continue
  const buf = await fs.readFile(file)
  if (buf.length < MIN_SIZE) continue
  const [gz, br] = await Promise.all([
    gzip(buf, { level: 9 }),
    brotli(buf, {
      params: {
        [zlib.constants.BROTLI_PARAM_QUALITY]: 11,
        [zlib.constants.BROTLI_PARAM_SIZE_HINT]: buf.length,
      },
    }),
  ])
  await Promise.all([fs.writeFile(`${file}.gz`, gz), fs.writeFile(`${file}.br`, br)])
  files++
  before += buf.length
  gzTotal += gz.length
  brTotal += br.length
}

const kb = n => `${(n / 1024).toFixed(0)} KB`
console.log(`compressed ${files} files: ${kb(before)} -> ${kb(gzTotal)} gzip / ${kb(brTotal)} brotli`)
