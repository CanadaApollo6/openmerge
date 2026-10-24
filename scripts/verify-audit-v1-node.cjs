#!/usr/bin/env node
// A second exact-number encoder. Only literal journals and frozen files are read.
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const dir = path.join(__dirname, '../internal/audit/testdata');
class Decimal { constructor(token) { this.token = token; } }
function number(token) {
  const match = /^(-?)(\d+)(?:\.(\d+))?(?:[eE]([+-]?\d+))?$/.exec(token);
  const fraction = match[3] || '';
  const digits = match[2] + fraction;
  const point = match[2].length + Number(match[4] || 0);
  let result = point <= 0 ? '0.' + '0'.repeat(-point) + digits
    : point >= digits.length ? digits + '0'.repeat(point - digits.length)
    : digits.slice(0, point) + '.' + digits.slice(point);
  result = result.replace(/^0+(?=\d)/, '');
  if (result.includes('.')) result = result.replace(/0+$/, '').replace(/\.$/, '');
  return /^0(?:\.0*)?$/.test(result) ? '0' : match[1] + result;
}
function parse(text) {
  return JSON.parse(text, (key, value, context) => typeof value === 'number' ? new Decimal(context.source) : value);
}
function encode(value) {
  if (value instanceof Decimal) return number(value.token);
  if (Array.isArray(value)) return '[' + value.map(encode).join(',') + ']';
  if (value && typeof value === 'object') return '{' + Object.keys(value)
    .sort((a,b) => Buffer.compare(Buffer.from(a),Buffer.from(b)))
    .map(key => encode(key) + ':' + encode(value[key])).join(',') + '}';
  return JSON.stringify(value).replace(/[\u0085\u2028\u2029]/g, c => '\\u' + c.charCodeAt(0).toString(16).padStart(4,'0'));
}
const sha = text => crypto.createHash('sha256').update(text).digest('hex');
let files = 0;
for (const name of fs.readdirSync(dir).filter(n => /-(?:events|selection|watermarks|dependency_boundaries)\.jsonl$|\-manifest\.json$/.test(n))) {
  const bytes = fs.readFileSync(path.join(dir,name),'utf8');
  for (const line of bytes.trimEnd().split('\n').filter(Boolean)) {
    if (encode(parse(line)) !== line) throw Error('second encoder mismatch: ' + name);
  }
  if (name.endsWith('-manifest.json')) {
    const manifest = parse(bytes);
    const expected = manifest.manifest_sha256;
    manifest.manifest_sha256 = '';
    if (sha(encode(manifest)) !== expected) throw Error('manifest self hash: ' + name);
    const prefix = name.slice(0,-'manifest.json'.length);
    for (const stream of ['events','selection','watermarks','dependency_boundaries']) {
      if (sha(fs.readFileSync(path.join(dir,prefix+manifest[stream]))) !== manifest[stream+'_sha256']) throw Error('stream hash: '+name);
    }
  }
  files++;
}
console.log(JSON.stringify({encoder:'Node exact decimal tokens',files,identical:true}));
