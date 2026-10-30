import express from 'express';
import path from 'node:path';
import crypto from 'node:crypto';
import { createServer as createViteServer } from 'vite';
import type { SwitchyardState, UsageCallRecord, MaskingRecord } from './src/types.js';

const PORT = 3000;
const HOST = '0.0.0.0';

// Deterministic hash placeholder generator for Switchyard privacy masking (preview, adapted from Magpie UI)
function makePlaceholder(kind: string, raw: string): string {
  const hash = crypto.createHash('sha256').update(raw).digest('hex').slice(0, 8);
  return `{{${kind}_${hash}}}`;
}

export function maskText(
  input: string,
  neverMask: string[] = []
): {
  masked: string;
  detections: { kind: MaskingRecord['kind']; original: string; placeholder: string; previewPrefix: string; length: number }[];
} {
  let masked = input;
  const detections: {
    kind: MaskingRecord['kind'];
    original: string;
    placeholder: string;
    previewPrefix: string;
    length: number;
  }[] = [];

  const rules: { kind: MaskingRecord['kind']; regex: RegExp }[] = [
    { kind: 'PRIVATE_KEY', regex: /-----BEGIN [A-Z ]+ PRIVATE KEY-----[\s\S]*?-----END [A-Z ]+ PRIVATE KEY-----/g },
    { kind: 'API_KEY', regex: /\b(?:sk-[a-zA-Z0-9_-]{12,}|ghp_[a-zA-Z0-9]{16,}|AIza[0-9A-Za-z-_]{20,}|xox[baprs]-[0-9A-Za-z-]{12,})\b/g },
    { kind: 'BEARER_TOKEN', regex: /\bBearer\s+([a-zA-Z0-9._-]{16,})\b/g },
    { kind: 'EMAIL', regex: /\b[A-Za-z0-9._%+-]+@(?!(?:example\.com|test|invalid)\b)[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b/g },
    { kind: 'IPV4', regex: /\b(?!127\.0\.0\.1\b|0\.0\.0\.0\b)(?:(?:25[0-5]|2[0-4]\d|[01]?\d\d?)\.){3}(?:25[0-5]|2[0-4]\d|[01]?\d\d?)\b/g },
  ];

  for (const rule of rules) {
    masked = masked.replace(rule.regex, (match) => {
      if (neverMask.some((n) => n && match.includes(n))) {
        return match;
      }
      const placeholder = makePlaceholder(rule.kind, match);
      detections.push({
        kind: rule.kind,
        original: match,
        placeholder,
        previewPrefix: match.slice(0, 6) + '…',
        length: match.length,
      });
      return placeholder;
    });
  }

  return { masked, detections };
}

// Initial in-memory state seeded from Magpie UI preview seed, adapted for Switchyard preview (not yet Railways live data)
const state: SwitchyardState = {
  version: 'switchyard-0.1.0-web',
  gatewayUrl: `http://127.0.0.1:${PORT}`,
  profiles: [
    {
      id: 'default',
      name: 'Default',
      description: 'Balanced daily setup across subscriptions and relays',
      agentModels: {
        claude: 'group/coding',
        codex: 'codex/gpt-5.5',
        gemini: 'deepseek/deepseek-flash',
        opencode: 'moonshot/kimi-k2.5',
        cursor: 'relay/claude-sonnet-4-6',
        zed: 'relay/claude-opus-4-7',
      },
    },
    {
      id: 'budget',
      name: 'Budget',
      description: 'Fast, low-cost models and cached endpoints first',
      agentModels: {
        claude: 'deepseek/deepseek-v4-pro',
        codex: 'acme/gpt-5.4-mini',
        gemini: 'deepseek/deepseek-flash',
        opencode: 'deepseek/deepseek-flash',
        cursor: 'acme/gpt-5.4-mini',
        zed: 'deepseek/deepseek-v4-pro',
      },
    },
    {
      id: 'focus',
      name: 'Focus (Deep Reasoning)',
      description: 'Frontier reasoning models with high effort across all agents',
      agentModels: {
        claude: 'relay/claude-opus-4-7',
        codex: 'codex/gpt-5.5',
        gemini: 'deepseek/deepseek-v4-pro',
        opencode: 'relay/claude-opus-4-7',
        cursor: 'relay/claude-opus-4-7',
        zed: 'codex/gpt-5.5',
      },
    },
  ],
  agents: [
    {
      id: 'claude',
      name: 'Claude Code',
      category: 'CLI',
      version: '1.0.48',
      installed: true,
      connected: true,
      model: 'group/coding',
      effort: 'high',
      configPath: '~/.claude/settings.json',
      protocol: 'Anthropic Messages (/v1/messages)',
      shortlist: ['group/coding', 'relay/claude-sonnet-4-6', 'relay/claude-opus-4-7', 'deepseek/deepseek-v4-pro'],
      activeSessions: 142,
    },
    {
      id: 'codex',
      name: 'Codex CLI',
      category: 'CLI',
      version: '0.28.0',
      installed: true,
      connected: true,
      model: 'codex/gpt-5.5',
      effort: 'high',
      configPath: '~/.codex/config.toml',
      protocol: 'OpenAI Responses (/v1/responses)',
      shortlist: ['codex/gpt-5.5', 'acme/gpt-5.5', 'relay/gpt-5.5', 'moonshot/kimi-k2.5'],
      activeSessions: 68,
    },
    {
      id: 'opencode',
      name: 'OpenCode',
      category: 'CLI',
      version: '0.9.12',
      installed: true,
      connected: true,
      model: 'moonshot/kimi-k2.5',
      effort: 'medium',
      configPath: '~/.config/opencode/config.json',
      protocol: 'OpenAI Chat (/v1/chat/completions)',
      shortlist: ['moonshot/kimi-k2.5', 'deepseek/deepseek-flash', 'acme/gpt-5.4-mini'],
      activeSessions: 31,
    },
    {
      id: 'gemini',
      name: 'Gemini CLI',
      category: 'CLI',
      version: '0.11.4',
      installed: true,
      connected: true,
      model: 'deepseek/deepseek-flash',
      effort: 'medium',
      configPath: '~/.gemini/settings.json',
      protocol: 'Gemini Stream (/v1beta/models)',
      shortlist: ['deepseek/deepseek-flash', 'acme/gpt-5.4-mini', 'zhipu/glm-5.3'],
      activeSessions: 19,
    },
    {
      id: 'cursor',
      name: 'Cursor CLI',
      category: 'IDE',
      version: '0.49.2',
      installed: true,
      connected: true,
      model: 'relay/claude-sonnet-4-6',
      effort: 'high',
      configPath: '~/.cursor/argv.json',
      protocol: 'OpenAI Chat (/v1/chat/completions)',
      shortlist: ['relay/claude-sonnet-4-6', 'deepseek/deepseek-v4-pro'],
      activeSessions: 12,
    },
    {
      id: 'zed',
      name: 'Zed',
      category: 'IDE',
      version: '0.183.1',
      installed: true,
      connected: true,
      model: 'relay/claude-opus-4-7',
      effort: 'high',
      configPath: '~/.config/zed/settings.json',
      protocol: 'Anthropic Messages (/v1/messages)',
      shortlist: ['relay/claude-opus-4-7', 'codex/gpt-5.5'],
      activeSessions: 9,
    },
  ],
  providers: [
    {
      id: 'relay',
      name: 'Sub2API Relay',
      kind: 'relay',
      protocol: 'Anthropic & OpenAI',
      baseUrl: 'https://relay.sub2api.internal/v1',
      routing: 'rotate',
      models: ['claude-sonnet-4-6', 'claude-opus-4-7', 'gpt-5.5'],
      keys: [
        { name: 'main', maskedKey: 'sk-seed-…0001', requestsToday: 84 },
        { name: 'backup', maskedKey: 'sk-seed-…0002', requestsToday: 41 },
        { name: 'team', maskedKey: 'sk-seed-…0003', off: true, requestsToday: 0 },
      ],
      balanceUSD: 287.4,
      windows: [
        { window: '5h', limit: 20, used: 13.4, remaining: 6.6, resetsIn: '3h 06m' },
        { window: '1d', limit: 60, used: 41.7, remaining: 18.3, resetsIn: '9h 00m' },
        { window: '7d', limit: 300, used: 118.2, remaining: 181.8, resetsIn: '3d 10h' },
      ],
    },
    {
      id: 'deepseek',
      name: 'DeepSeek Official',
      kind: 'api',
      protocol: 'OpenAI Chat',
      baseUrl: 'https://api.deepseek.com/v1',
      routing: 'order',
      models: ['deepseek-v4-pro', 'deepseek-flash'],
      keys: [{ name: 'default', maskedKey: 'sk-dseek…9f2a', requestsToday: 63 }],
      balanceUSD: 42.85,
    },
    {
      id: 'acme',
      name: 'Acme Cloud',
      kind: 'api',
      protocol: 'OpenAI Responses & Chat',
      baseUrl: 'https://api.acmecloud.dev/v1',
      routing: 'order',
      models: ['gpt-5.5', 'gpt-5.4-mini', 'gpt-5.3-codex'],
      keys: [{ name: 'prod-key', maskedKey: 'sk-acme-…0001', requestsToday: 52 }],
      balanceUSD: 119.1,
    },
    {
      id: 'moonshot',
      name: 'Kimi (Moonshot)',
      kind: 'api',
      protocol: 'OpenAI Chat',
      baseUrl: 'https://api.moonshot.cn/v1',
      routing: 'order',
      models: ['kimi-k2.5', 'kimi-k3'],
      keys: [{ name: 'cn-main', maskedKey: 'sk-kimi-…881c', requestsToday: 29 }],
      balanceUSD: 64.0,
    },
    {
      id: 'codex',
      name: 'Codex / ChatGPT Plan',
      kind: 'subscription',
      protocol: 'ChatGPT Backend Responses',
      baseUrl: 'https://chatgpt.com/backend-api/codex',
      routing: 'smart',
      models: ['gpt-5.5', 'gpt-5.4', 'gpt-5.3-codex', 'gpt-5.4-mini'],
      keys: [],
    },
    {
      id: 'cursor',
      name: 'Cursor Subscription',
      kind: 'subscription',
      protocol: 'OpenCode Auth Plugin Host',
      baseUrl: 'plugin://@magpie-community/opencode-cursor-auth',
      routing: 'smart',
      models: ['claude-sonnet-4-6', 'gpt-5.5'],
      keys: [],
      move: {
        state: 'plugin',
        pluginPkg: '@magpie-community/opencode-cursor-auth',
      },
    },
  ],
  accounts: [
    {
      id: 'acct-seed-maya',
      provider: 'codex',
      user: 'maya.chen@example.com',
      plan: 'Pro',
      active: true,
      credits: '1,250',
      windows: [
        { label: '5 hours', usedPercent: 37, resetsIn: '2h 12m', resetAt: new Date(Date.now() + 2.2 * 3600e3).toISOString() },
        { label: '7 days', usedPercent: 64, resetsIn: '2d 07h', resetAt: new Date(Date.now() + 55 * 3600e3).toISOString() },
      ],
    },
    {
      id: 'acct-seed-team',
      provider: 'codex',
      user: 'dev.team@example.com',
      plan: 'Plus',
      active: true,
      credits: null,
      windows: [
        { label: '5 hours', usedPercent: 91, resetsIn: '48m', resetAt: new Date(Date.now() + 0.8 * 3600e3).toISOString() },
        { label: '7 days', usedPercent: 83, resetsIn: '4d 14h', resetAt: new Date(Date.now() + 110 * 3600e3).toISOString() },
      ],
    },
    {
      id: 'acct-claude-max',
      provider: 'claude',
      user: 'maya.chen@work.io',
      plan: 'Max 20x',
      active: true,
      credits: null,
      windows: [
        { label: '5 hours', usedPercent: 22, resetsIn: '3h 40m', resetAt: new Date(Date.now() + 3.66 * 3600e3).toISOString() },
        { label: '7 days', usedPercent: 48, resetsIn: '5d 02h', resetAt: new Date(Date.now() + 122 * 3600e3).toISOString() },
      ],
    },
  ],
  groups: [
    {
      id: 'coding',
      name: 'Coding',
      routing: 'order',
      members: ['relay/claude-sonnet-4-6', 'acme/gpt-5.5', 'deepseek/deepseek-v4-pro'],
      rules: [
        { use: 'deepseek/deepseek-flash', agents: ['opencode'], intent: 'quick lookup & shell explain' },
      ],
    },
    {
      id: 'opus-anywhere',
      name: 'Opus anywhere',
      routing: 'smart',
      members: ['relay/claude-opus-4-7', 'codex/gpt-5.5', 'acme/gpt-5.5'],
      rules: [
        { use: 'relay/claude-sonnet-4-6', intent: 'formatting & lint fixes' },
      ],
    },
  ],
  usage: [
    {
      route_id: 1790000000108,
      t: new Date(Date.now() - 2 * 60e3).toISOString(),
      agent: 'claude',
      provider: 'relay',
      model: 'claude-sonnet-4-6',
      req: 'group/coding',
      served: 'claude-sonnet-4-6',
      in: 4820,
      out: 940,
      cache_read: 64200,
      reasoning: 310,
      effort: 'high',
      ms: 11800,
      ttft_ms: 890,
      tps: 86.2,
      costUSD: 0.042,
      status: 200,
      ep: '/v1/messages',
      session: 'a8f012bc-4012-4c9a-991b-7712309ab102',
      providerKeyName: 'main',
      maskedCount: 2,
    },
    {
      route_id: 1790000000107,
      t: new Date(Date.now() - 9 * 60e3).toISOString(),
      agent: 'codex',
      provider: 'codex',
      model: 'gpt-5.5',
      req: 'codex/gpt-5.5',
      served: 'gpt-5.5',
      in: 3190,
      out: 1420,
      cache_read: 28400,
      reasoning: 640,
      effort: 'high',
      ms: 16400,
      ttft_ms: 1520,
      tps: 95.4,
      costUSD: 0.0,
      status: 200,
      ep: '/v1/responses → /backend-api/codex/responses',
      session: '01a09812-772b-419a-8190-112233445566',
      providerAccount: 'maya.chen@example.com',
      maskedCount: 1,
    },
    {
      route_id: 1790000000106,
      t: new Date(Date.now() - 24 * 60e3).toISOString(),
      agent: 'opencode',
      provider: 'deepseek',
      model: 'deepseek-flash',
      req: 'group/coding',
      served: 'deepseek-flash',
      in: 1940,
      out: 520,
      cache_read: 12000,
      ms: 6800,
      ttft_ms: 410,
      tps: 81.3,
      costUSD: 0.003,
      status: 200,
      ep: '/v1/chat/completions',
      session: 'c91203ab-3120-4f11-8a92-881920391011',
    },
    {
      route_id: 1790000000105,
      t: new Date(Date.now() - 41 * 60e3).toISOString(),
      agent: 'claude',
      provider: 'relay',
      model: 'claude-opus-4-7',
      req: 'relay/claude-opus-4-7',
      served: 'claude-opus-4-7',
      in: 7650,
      out: 1890,
      cache_read: 81200,
      reasoning: 820,
      effort: 'high',
      ms: 49200,
      ttft_ms: 1680,
      tps: 39.8,
      costUSD: 0.148,
      status: 200,
      ep: '/v1/messages',
      session: 'f49102bc-991a-4c00-b219-441239812091',
      providerKeyName: 'backup',
      maskedCount: 3,
    },
    {
      route_id: 1790000000104,
      t: new Date(Date.now() - 68 * 60e3).toISOString(),
      agent: 'gemini',
      provider: 'acme',
      model: 'gpt-5.4-mini',
      req: 'acme/gpt-5.4-mini',
      served: 'gpt-5.4-mini',
      in: 2100,
      out: 610,
      cache_read: 0,
      ms: 3400,
      ttft_ms: 320,
      tps: 198.0,
      costUSD: 0.004,
      status: 200,
      ep: '/v1beta/models:streamGenerateContent → /v1/chat/completions',
      session: 'd1029384-5566-4778-9900-aabbccddeeff',
    },
    {
      route_id: 1790000000103,
      t: new Date(Date.now() - 95 * 60e3).toISOString(),
      agent: 'codex',
      provider: 'codex',
      model: 'gpt-5.5',
      req: 'codex/gpt-5.5',
      served: 'gpt-5.5',
      in: 5400,
      out: 0,
      cache_read: 0,
      ms: 420,
      ttft_ms: 0,
      tps: 0,
      costUSD: 0,
      status: 429,
      ep: '/v1/responses → /backend-api/codex/responses',
      session: '01a09812-772b-419a-8190-112233445566',
      providerAccount: 'dev.team@example.com',
      err: 'rate limited on dev.team@example.com (91% 5h window): automatically failed over to maya.chen@example.com',
    },
  ],
  sessions: [
    {
      id: 'a8f012bc-4012-4c9a-991b-7712309ab102',
      agent: 'claude',
      title: 'Fix the flaky login test & port CLI flags to new parser',
      cwd: '~/work/shop-api',
      model: 'claude-sonnet-4-6',
      turns: 24,
      tokens: 184200,
      costUSD: 0.64,
      updatedAt: '2m ago',
    },
    {
      id: '01a09812-772b-419a-8190-112233445566',
      agent: 'codex',
      title: 'Add pagination to the sessions list and write orders migration',
      cwd: '~/work/web-app',
      model: 'gpt-5.5',
      turns: 18,
      tokens: 129400,
      costUSD: 0.0,
      updatedAt: '9m ago',
    },
    {
      id: 'f49102bc-991a-4c00-b219-441239812091',
      agent: 'claude',
      title: 'Make the quota forecast chart readable in dark mode',
      cwd: '~/work/web-app',
      model: 'claude-opus-4-7',
      turns: 31,
      tokens: 291000,
      costUSD: 1.82,
      updatedAt: '41m ago',
    },
    {
      id: 'c91203ab-3120-4f11-8a92-881920391011',
      agent: 'opencode',
      title: 'Speed up the search endpoint & add retry middleware',
      cwd: '~/work/infra',
      model: 'deepseek-flash',
      turns: 11,
      tokens: 54300,
      costUSD: 0.05,
      updatedAt: '1h ago',
    },
    {
      id: 'e7712390-1122-4334-9556-778899001122',
      agent: 'claude',
      title: 'Refactor the config loader to preserve JSONC comments',
      cwd: '~/oss/parser',
      model: 'claude-sonnet-4-6',
      turns: 15,
      tokens: 112800,
      costUSD: 0.39,
      updatedAt: '3h ago',
    },
  ],
  skills: [
    {
      name: 'pdf',
      description: 'Read, fill and merge PDF files',
      agents: ['claude', 'codex'],
      source: { kind: 'github', repo: 'anthropics/skills' },
    },
    {
      name: 'webapp-testing',
      description: 'Test a local web app with Playwright',
      agents: ['claude', 'codex'],
      source: { kind: 'github', repo: 'anthropics/skills' },
    },
    {
      name: 'frontend-design',
      description: 'Build polished front-end interfaces',
      agents: ['claude', 'codex', 'opencode'],
      source: { kind: 'github', repo: 'vercel-labs/agent-skills' },
    },
    {
      name: 'react-best-practices',
      description: 'Review React code for common mistakes',
      agents: ['claude', 'codex'],
      source: { kind: 'github', repo: 'vercel-labs/agent-skills' },
    },
    {
      name: 'team-style',
      description: "Our team's code style and PR review checklist",
      agents: ['claude', 'codex', 'zed'],
      source: { kind: 'folder', dir: '~/work/skills/team-style' },
    },
  ],
  plugins: [
    {
      id: 'gateway',
      name: 'Gateway and Routing pages',
      packageName: 'builtin/gateway',
      kind: 'builtin',
      version: 'built-in',
      description: 'Live protocol translation stage, request inspection and routing mode controls.',
      on: true,
      essential: true,
    },
    {
      id: 'groups',
      name: 'Routing groups',
      packageName: 'builtin/groups',
      kind: 'builtin',
      version: 'built-in',
      description: 'Smart, order, rotate, usage and pace failover across accounts and models.',
      on: true,
    },
    {
      id: 'observability',
      name: 'Observability & Context',
      packageName: 'builtin/observability',
      kind: 'builtin',
      version: 'built-in',
      description: 'Usage ledger, context window breakdown, decode speed timing and OTLP export.',
      on: true,
    },
    {
      id: 'sessions',
      name: 'Sessions manager',
      packageName: 'builtin/sessions',
      kind: 'builtin',
      version: 'built-in',
      description: 'Browse, search, relocate and resume Claude Code, Codex and omp sessions.',
      on: true,
    },
    {
      id: 'warmup',
      name: 'Warm-up and check-in',
      packageName: 'builtin/warmup',
      kind: 'builtin',
      version: 'built-in',
      description: 'Starts Claude and ChatGPT 5-hour quota windows on your schedule.',
      on: true,
    },
    {
      id: 'cursor-auth',
      name: 'Cursor Subscription Auth',
      packageName: '@magpie-community/opencode-cursor-auth',
      kind: 'auth',
      version: '1.4.2',
      description: 'Serves retired built-in Cursor subscription sign-in, models, usage and gateway requests.',
      on: true,
      replacesBuiltin: 'cursor',
    },
    {
      id: 'kiro-auth',
      name: 'Kiro Subscription Auth',
      packageName: '@magpie-community/opencode-kiro-auth',
      kind: 'auth',
      version: '1.2.0',
      description: 'Serves retired built-in Kiro subscription accounts via the Bun plugin host.',
      on: true,
      replacesBuiltin: 'kiro',
    },
    {
      id: 'zed-auth',
      name: 'Zed Subscription Auth',
      packageName: '@magpie-community/opencode-zed-auth',
      kind: 'auth',
      version: '1.1.5',
      description: 'Serves retired built-in Zed subscription accounts in magpie.',
      on: true,
      replacesBuiltin: 'zed',
    },
    {
      id: 'model-map',
      name: 'Model Mapping Middleware',
      packageName: '@magpie-community/middleware-model-map',
      kind: 'middleware',
      version: '1.0.4',
      description: "New API's model_mapping: send a model under another name; replies keep the name asked for.",
      on: true,
    },
  ],
  masking: {
    total7d: 5812,
    byKind: {
      API_KEY: 3410,
      EMAIL: 1290,
      BEARER_TOKEN: 642,
      PRIVATE_KEY: 280,
      IPV4: 190,
    },
    records: [
      {
        id: 'msk-901',
        timestamp: '2m ago',
        agent: 'claude',
        provider: 'relay',
        kind: 'API_KEY',
        action: 'Mask',
        placeholder: '{{API_KEY_k3v9x2mq}}',
        previewPrefix: 'sk-proj…',
        length: 51,
      },
      {
        id: 'msk-900',
        timestamp: '9m ago',
        agent: 'codex',
        provider: 'codex',
        kind: 'EMAIL',
        action: 'Mask',
        placeholder: '{{EMAIL_81a2c90f}}',
        previewPrefix: 'ops-on…',
        length: 24,
      },
      {
        id: 'msk-899',
        timestamp: '41m ago',
        agent: 'claude',
        provider: 'relay',
        kind: 'PRIVATE_KEY',
        action: 'Mask',
        placeholder: '{{PRIVATE_KEY_4f91e0bc}}',
        previewPrefix: '-----B…',
        length: 1679,
      },
    ],
  },
  settings: {
    maskingEnabled: true,
    maskSecrets: true,
    maskPersonal: true,
    localModelFinder: false,
    neverMask: ['example.com', 'sk-seed-relay-0001'],
    redactRouteModel: 'Off',
    lanShare: false,
    browserAccess: 'local',
    discoverSubscriptions: true,
    keepAwake: true,
    activeProfile: 'default',
    gatewayAddr: `127.0.0.1:${PORT}`,
  },
};

async function startServer() {
  const app = express();
  app.use(express.json({ limit: '2mb' }));

  // Healthcheck endpoint (Dockerfile Go healthcheck pending; see RAILWAYS_ADAPTATION.md)
  app.get('/api/healthcheck', (_req, res) => {
    res.json({ status: 'ok', version: state.version, gateway: state.gatewayUrl });
  });

  // Core GUI state endpoint
  app.get('/api/state', (_req, res) => {
    res.json(state);
  });

  // Providers list endpoint
  app.get('/api/providers', (_req, res) => {
    res.json({
      providers: state.providers,
      accounts: state.accounts,
      onPlugins: state.providers.filter((p) => p.move?.state === 'plugin').map((p) => p.id),
    });
  });

  // Switch an agent's model or effort
  app.post('/api/agents/model', (req, res) => {
    const { agentId, model, effort } = req.body || {};
    const agent = state.agents.find((a) => a.id === agentId);
    if (!agent) {
      res.status(404).json({ error: `Unknown agent: ${agentId}` });
      return;
    }
    if (model) {
      agent.model = model;
      if (!agent.shortlist.includes(model)) {
        agent.shortlist.unshift(model);
      }
    }
    if (effort) {
      agent.effort = effort;
    }
    res.json(state);
  });

  // Apply a saved profile across all agents
  app.post('/api/profiles/apply', (req, res) => {
    const { profileId } = req.body || {};
    const profile = state.profiles.find((p) => p.id === profileId);
    if (!profile) {
      res.status(404).json({ error: `Unknown profile: ${profileId}` });
      return;
    }
    state.settings.activeProfile = profile.id;
    for (const agent of state.agents) {
      if (profile.agentModels[agent.id]) {
        agent.model = profile.agentModels[agent.id];
      }
    }
    res.json(state);
  });

  // Add a provider preset or key
  app.post('/api/providers', (req, res) => {
    const { id, name, baseUrl, key, keyName, models, routing, kind } = req.body || {};
    if (!id || !name) {
      res.status(400).json({ error: 'Provider id and name are required' });
      return;
    }
    const cleanId = String(id).toLowerCase().replace(/[^a-z0-9-_]/g, '');
    const existing = state.providers.find((p) => p.id === cleanId);
    const maskedKey = key ? `${String(key).slice(0, 6)}…${String(key).slice(-4)}` : 'sk-live-…99a1';
    if (existing) {
      existing.keys.push({
        name: keyName || `key-${existing.keys.length + 1}`,
        maskedKey,
        requestsToday: 0,
      });
    } else {
      state.providers.push({
        id: cleanId,
        name: String(name),
        kind: kind || 'api',
        protocol: 'OpenAI Chat & Responses',
        baseUrl: baseUrl || `https://api.${cleanId}.com/v1`,
        routing: routing || 'order',
        models: Array.isArray(models) && models.length ? models : [`${cleanId}-pro`, `${cleanId}-flash`],
        keys: [{ name: keyName || 'default', maskedKey, requestsToday: 0 }],
        balanceUSD: 50.0,
      });
    }
    res.json(state);
  });

  // Add or update a routing group
  app.post('/api/groups', (req, res) => {
    const { id, name, routing, members } = req.body || {};
    if (!name || !Array.isArray(members) || members.length === 0) {
      res.status(400).json({ error: 'Group name and at least one model member are required' });
      return;
    }
    const cleanId = (id || name).toLowerCase().replace(/[^a-z0-9-]+/g, '-');
    const existing = state.groups.find((g) => g.id === cleanId);
    if (existing) {
      existing.name = name;
      existing.routing = routing || existing.routing;
      existing.members = members;
    } else {
      state.groups.push({
        id: cleanId,
        name,
        routing: routing || 'smart',
        members,
        rules: [],
      });
    }
    res.json(state);
  });

  // Refresh subscription quota cards
  app.post('/api/usage/quotas/refresh', (req, res) => {
    const { accountId } = req.body || {};
    for (const acc of state.accounts) {
      if (!accountId || acc.id === accountId) {
        acc.windows = acc.windows.map((w) => ({
          ...w,
          usedPercent: Math.min(100, Math.max(5, w.usedPercent + (Math.random() > 0.5 ? 1 : -1))),
        }));
      }
    }
    res.json(state);
  });

  // Privacy masking live preview (/api/masking/preview)
  app.post('/api/masking/preview', (req, res) => {
    const { text } = req.body || {};
    const result = maskText(String(text || ''), state.settings.neverMask);
    res.json(result);
  });

  // Toggle plugin
  app.post('/api/plugins/builtin', (req, res) => {
    const { id, on } = req.body || {};
    const plugin = state.plugins.find((p) => p.id === id);
    if (!plugin) {
      res.status(404).json({ error: 'Plugin not found' });
      return;
    }
    if (plugin.essential && !on) {
      res.status(400).json({ error: 'Essential plugin cannot be turned off' });
      return;
    }
    plugin.on = Boolean(on);
    res.json(state);
  });

  // Update settings
  app.post('/api/settings', (req, res) => {
    Object.assign(state.settings, req.body || {});
    res.json(state);
  });

  // Unified Gateway simulator & OpenAI / Anthropic / Responses protocol endpoints
  const handleGatewayRequest = (protocolEndpoint: string) => (req: express.Request, res: express.Response) => {
    const body = req.body || {};
    const requestedModel: string = body.model || 'group/coding';
    const agentName: string = body.agent || (req.headers['x-switchyard-agent'] as string) || 'claude';
    const rawPrompt: string =
      body.prompt ||
      body.messages?.[body.messages.length - 1]?.content ||
      body.input ||
      'Check connection with sk-proj-9918273645109283abcd and ops@company.io';

    const { masked, detections } = state.settings.maskingEnabled
      ? maskText(typeof rawPrompt === 'string' ? rawPrompt : JSON.stringify(rawPrompt), state.settings.neverMask)
      : { masked: String(rawPrompt), detections: [] };

    // Resolve routing group if model starts with group/
    let resolvedSpec = requestedModel;
    let routingNote = 'Direct provider route';
    if (requestedModel.startsWith('group/')) {
      const groupId = requestedModel.slice('group/'.length);
      const group = state.groups.find((g) => g.id === groupId);
      if (group && group.members.length > 0) {
        const ruleMatch = group.rules.find((r) => r.agents?.includes(agentName));
        if (ruleMatch) {
          resolvedSpec = ruleMatch.use;
          routingNote = `Group "${group.name}" intent/agent rule → ${resolvedSpec}`;
        } else if (group.routing === 'rotate') {
          resolvedSpec = group.members[state.usage.length % group.members.length];
          routingNote = `Group "${group.name}" (rotate) → ${resolvedSpec}`;
        } else if (group.routing === 'smart') {
          resolvedSpec = group.members[0];
          routingNote = `Group "${group.name}" (smart: earliest reset window with quota) → ${resolvedSpec}`;
        } else {
          resolvedSpec = group.members[0];
          routingNote = `Group "${group.name}" (${group.routing}) → ${resolvedSpec}`;
        }
      }
    }

    const [providerId, modelName] = resolvedSpec.includes('/')
      ? resolvedSpec.split('/')
      : ['relay', resolvedSpec];

    const record: UsageCallRecord = {
      route_id: 1790000000000 + state.usage.length + 110,
      t: new Date().toISOString(),
      agent: agentName,
      provider: providerId,
      model: modelName,
      req: requestedModel,
      served: modelName,
      in: Math.max(120, Math.round(masked.length * 1.4)),
      out: 185,
      cache_read: 16400,
      reasoning: 64,
      effort: 'high',
      ms: 640,
      ttft_ms: 210,
      tps: 92.5,
      costUSD: providerId === 'codex' ? 0 : 0.008,
      status: 200,
      ep: protocolEndpoint,
      session: crypto.randomUUID(),
      maskedCount: detections.length,
    };

    state.usage.unshift(record);
    if (detections.length > 0) {
      state.masking.total7d += detections.length;
      for (const d of detections) {
        state.masking.byKind[d.kind] = (state.masking.byKind[d.kind] || 0) + 1;
        state.masking.records.unshift({
          id: `msk-${Date.now().toString().slice(-4)}`,
          timestamp: 'just now',
          agent: agentName,
          provider: providerId,
          kind: d.kind,
          action: 'Mask',
          placeholder: d.placeholder,
          previewPrefix: d.previewPrefix,
          length: d.length,
        });
      }
    }

    const vendorReplyMasked = `Routed [${requestedModel}] → [${providerId}/${modelName}] (${routingNote}). Upstream received masked prompt: "${masked}"`;
    // Unmask placeholders back for the local coding agent
    let unmaskedReply = vendorReplyMasked;
    for (const d of detections) {
      unmaskedReply = unmaskedReply.split(d.placeholder).join(d.original);
    }

    res.json({
      id: `switchyard_${record.route_id}`,
      object: 'chat.completion',
      model: `${providerId}/${modelName}`,
      routingNote,
      maskedPromptSentToVendor: masked,
      unmaskedReplyToAgent: unmaskedReply,
      detections,
      record,
      choices: [
        {
          index: 0,
          message: { role: 'assistant', content: unmaskedReply },
          finish_reason: 'stop',
        },
      ],
    });
  };

  app.post('/v1/chat/completions', handleGatewayRequest('/v1/chat/completions'));
  app.post('/v1/messages', handleGatewayRequest('/v1/messages'));
  app.post('/v1/responses', handleGatewayRequest('/v1/responses'));

  // Stub remaining unmigrated /api/* routes with 501 per migration specification
  app.use('/api', (req, res) => {
    res.status(501).json({
      error: 'Not yet migrated',
      path: req.originalUrl,
      method: req.method,
    });
  });

  if (process.env.NODE_ENV === 'production') {
    const distDir = path.resolve(process.cwd(), 'dist');
    app.use(express.static(distDir));
    app.use((_req, res) => {
      res.sendFile(path.join(distDir, 'index.html'));
    });
  } else {
    const vite = await createViteServer({
      server: { middlewareMode: true, hmr: false },
      appType: 'spa',
    });
    app.use(vite.middlewares);
  }

  app.listen(PORT, HOST, () => {
    console.log(`● switchyard web & gateway listening on http://${HOST}:${PORT}`);
  });
}

startServer();
