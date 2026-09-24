// Zero-prompt Copilot ACP startup probe for a trusted, isolated Actions job.
// It prints only protocol phases, numeric RPC codes, and fixed OS error codes.
import { spawn } from 'node:child_process';
import { mkdtempSync } from 'node:fs';
import { createInterface } from 'node:readline';

const token = process.env.SOFA_MODEL_TOKEN;
if (!token) {
  console.log('sofa ACP preflight: missing model credential');
  process.exit(1);
}

const home = mkdtempSync('/tmp/sofa-agent-home-');
const packageEntry = process.env.SOFA_COPILOT_ENTRY;
const child = spawn(packageEntry ? 'node' : process.env.SOFA_COPILOT_PATH || '/copilot/copilot',
  packageEntry ? [packageEntry, '--acp', '--stdio'] : ['--acp', '--stdio'], {
  cwd: process.env.SOFA_WORKSPACE || '/workspace',
  env: {
    PATH: '/toolkit:/copilot:/usr/local/bin:/usr/bin:/bin',
    HOME: home,
    XDG_CONFIG_HOME: home,
    GITHUB_TOKEN: token,
  },
  stdio: ['pipe', 'pipe', 'pipe'],
});

let stage = 'initialize';
let finished = false;
const stderrCodes = new Set();
let stderrBytes = 0;
let lines = 0;
const osCodes = ['ENOSPC', 'EACCES', 'EPERM', 'EROFS', 'ENOMEM', 'ENOENT'];
const startupCategories = [
  ['Failed to extract bundled package', 'package extraction failed'],
  ['ERR_SYSTEM_ERROR', 'Node system error'],
  ['Cannot find module', 'module unavailable'],
  ['ERR_DLOPEN_FAILED', 'native module unavailable'],
  ['cannot open shared object file', 'shared library unavailable'],
  ['failed to map segment from shared object', 'shared library mapping failed'],
  ['Operation not permitted', 'operation not permitted'],
  ['invalid ELF', 'invalid executable format'],
  ['GLIBC_', 'glibc incompatible'],
];

function finish(detail, success = false) {
  if (finished) return;
  finished = true;
  clearTimeout(timer);
  const categories = [...stderrCodes].sort().join(', ');
  console.log(`sofa ACP preflight: ${stage}: ${detail}; stderr bytes ${stderrBytes}${categories ? `; child ${categories}` : ''}`);
  child.kill('SIGKILL');
  process.exitCode = success ? 0 : 1;
}

function send(id, method, params) {
  child.stdin.write(`${JSON.stringify({ jsonrpc: '2.0', id, method, params })}\n`);
}

child.stderr.on('data', (data) => {
  stderrBytes += data.length;
  const chunk = String(data);
  for (const code of osCodes) if (chunk.includes(code)) stderrCodes.add(code);
  for (const [pattern, category] of startupCategories) {
    if (chunk.includes(pattern)) stderrCodes.add(category);
  }
});
child.on('error', (error) => {
  const category = osCodes.includes(error.code) ? error.code : 'process start failed';
  finish(category);
});
child.on('exit', (code, signal) => {
  if (!finished) finish(`connection closed (exit ${Number.isInteger(code) ? code : signal ? 'signal' : 'unknown'})`);
});

const timer = setTimeout(() => finish('timeout'), 30_000);
const input = createInterface({ input: child.stdout });
input.on('line', (line) => {
  if (finished) return;
  lines += 1;
  if (lines > 1000 || line.length > 2 << 20) return finish('protocol limit');
  let response;
  try {
    response = JSON.parse(line);
  } catch {
    return finish('invalid JSON');
  }
  const id = stage === 'initialize' ? 1 : 2;
  if (response.id !== id) return;
  if (response.error) {
    const code = Number.isInteger(response.error.code) ? response.error.code : 'unknown';
    return finish(`RPC code ${code}`);
  }
  if (!response.result || typeof response.result !== 'object') return finish('invalid response');
  if (stage === 'initialize') {
    if (response.result.protocolVersion !== 1) return finish('protocol version mismatch');
    stage = 'session/new';
    send(2, 'session/new', { cwd: process.env.SOFA_WORKSPACE || '/workspace', mcpServers: [] });
  } else if (typeof response.result.sessionId === 'string' && response.result.sessionId.length > 0) {
    finish('accepted without a model prompt', true);
  } else {
    finish('invalid response');
  }
});

send(1, 'initialize', {
  protocolVersion: 1,
  clientInfo: { name: 'sofa-preflight', version: 'prototype' },
  clientCapabilities: { fs: { readTextFile: true, writeTextFile: true } },
});
