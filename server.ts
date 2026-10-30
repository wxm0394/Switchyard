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

// Initial in-memory state seeded with Railways-shaped preview data (aliases, routes, audit-style rows). Example values only; no real keys, no live gateway state.
const state: SwitchyardState = {
  version: 'switchyard-0.2.0-web',
  gatewayUrl: `http://127.0.0.1:${PORT}`,
  profiles: [
    {
      id: 'default',
      name: 'Default',
      description: 'Balanced alias setup: coder for coding, fast for quick turns, reasoning for heavy work',
      agentModels: { cline: 'coder', roo: 'coder', chat: 'fast', script: 'coder', other: 'fast' },
    },
    {
      id: 'budget',
      name: 'Budget',
      description: 'Fast alias first, reasoning only for heavy work (preview example)',
      agentModels: { cline: 'fast', roo: 'fast', chat: 'fast', script: 'fast', other: 'fast' },
    },
  ],
  agents: [
    { id: 'cline', name: 'Cline', category: 'IDE', version: 'preview', installed: true, connected: true, model: 'coder', effort: 'high', configPath: 'VS Code Cline settings (preview example)', protocol: 'OpenAI Chat (/v1/chat/completions)', shortlist: ['coder', 'fast', 'reasoning', 'vision', 'google/gemini-3-flash-preview', 'qwen3.6-27b'], activeSessions: 3 },
    { id: 'roo', name: 'Roo Code', category: 'IDE', version: 'preview', installed: true, connected: true, model: 'coder', effort: 'high', configPath: 'VS Code Roo settings (preview example)', protocol: 'OpenAI Chat (/v1/chat/completions)', shortlist: ['coder', 'fast', 'reasoning', 'vision'], activeSessions: 1 },
    { id: 'chat', name: 'Chat UI', category: 'Desktop', version: 'preview', installed: true, connected: true, model: 'fast', effort: 'medium', configPath: 'Chat UI custom endpoint (preview example)', protocol: 'OpenAI Chat (/v1/chat/completions)', shortlist: ['coder', 'fast', 'reasoning', 'vision'], activeSessions: 0 },
    { id: 'script', name: 'Batch script', category: 'CLI', version: 'preview', installed: true, connected: true, model: 'coder', effort: 'medium', configPath: 'Script base URL + client token (preview example)', protocol: 'OpenAI Chat (/v1/chat/completions)', shortlist: ['coder', 'fast', 'reasoning'], activeSessions: 0 },
    { id: 'other', name: 'Other coding agent', category: 'CLI', version: 'preview', installed: false, connected: false, model: 'fast', effort: 'medium', configPath: 'Agent custom endpoint (preview example)', protocol: 'OpenAI Chat (/v1/chat/completions)', shortlist: ['coder', 'fast'], activeSessions: 0 },
  ],
  providers: [
    { id: 'kaggle', name: 'Kaggle Gemini 3.6 Flash', kind: 'api', protocol: 'OpenAI Chat', baseUrl: 'Model Proxy (preview example, not a live endpoint)', routing: 'rotate', models: ['google/gemini-3-flash-preview'], upstreamModel: 'google/gemini-3-flash-preview', routeState: 'healthy', circuitState: 'closed', keyState: 'healthy', maxContext: 1000000, supportsVision: true, supportsTools: true, supportsJson: true, dailyBudget: '$10/day (example)', keys: [{ name: 'model-proxy', maskedKey: 'sk-example-…0001', requestsToday: 46 }], windows: [{ window: '1d', limit: 10, used: 6.2, remaining: 3.8, resetsIn: '~17:00 CST (example)' }] },
    { id: 'qwen-gpu', name: 'Qwen3.5-9B GPU', kind: 'api', protocol: 'OpenAI Chat', baseUrl: 'Kaggle GPU tunnel (preview example, not a live endpoint)', routing: 'order', models: ['qwen3.6-27b'], upstreamModel: 'qwen3.5-9b', routeState: 'healthy', circuitState: 'closed', keyState: 'healthy', maxContext: 131072, supportsVision: false, supportsTools: true, supportsJson: true, dailyBudget: 'GPU time (example, 30h/week in real system)', keys: [{ name: 'gpu-tunnel', maskedKey: 'sk-example-…0002', requestsToday: 1 }], windows: [{ window: '1w', limit: 30, used: 12, remaining: 18, resetsIn: 'week (example)' }] },
    { id: 'nvidia', name: 'NVIDIA Lightning', kind: 'api', protocol: 'OpenAI Chat', baseUrl: 'https://integrate.api.nvidia.com/v1', routing: 'order', models: ['nvidia-lightning'], upstreamModel: 'nvidia-lightning', routeState: 'healthy', circuitState: 'closed', keyState: 'healthy', maxContext: 131072, supportsVision: false, supportsTools: true, supportsJson: true, dailyBudget: 'example daily budget', keys: [{ name: 'default', maskedKey: 'sk-example-…0003', requestsToday: 12 }] },
    { id: 'secretary', name: 'Secretary route (private only)', kind: 'api', protocol: 'OpenAI Chat', baseUrl: 'https://openrouter.ai/api/v1', routing: 'order', models: ['openrouter-nemotron-lightning'], upstreamModel: 'openrouter-nemotron-lightning', routeState: 'healthy', circuitState: 'closed', keyState: 'healthy', maxContext: 131072, supportsVision: false, supportsTools: true, supportsJson: true, dailyBudget: 'example daily budget', keys: [{ name: 'default', maskedKey: 'sk-example-…0004', requestsToday: 0 }] },
    { id: 'zai', name: 'Z.ai GLM 4.7 Flash', kind: 'api', protocol: 'OpenAI Chat', baseUrl: 'https://open.bigmodel.cn/api/paas/v4', routing: 'order', models: ['zai-glm-4.7-flash'], upstreamModel: 'glm-4.7-flash', routeState: 'healthy', circuitState: 'closed', keyState: 'healthy', maxContext: 200000, supportsVision: false, supportsTools: true, supportsJson: true, dailyBudget: 'Free capacity, 1305 when busy (example)', keys: [{ name: 'default', maskedKey: 'sk-example-…0005', requestsToday: 3 }] },
    { id: 'cerebras', name: 'Cerebras GPT-OSS 120B', kind: 'api', protocol: 'OpenAI Chat', baseUrl: 'https://api.cerebras.ai/v1', routing: 'order', models: ['gpt-oss-120b'], upstreamModel: 'gpt-oss-120b', routeState: 'healthy', circuitState: 'closed', keyState: 'healthy', maxContext: 131072, supportsVision: false, supportsTools: true, supportsJson: true, dailyBudget: '1M tokens/day (example)', keys: [{ name: 'default', maskedKey: 'sk-example-…0006', requestsToday: 1 }], windows: [{ window: '1d', limit: 1000000, used: 120000, remaining: 880000, resetsIn: 'midnight UTC (example)' }] },
  ],
  accounts: [
    { id: 'b-kaggle', provider: 'kaggle', user: 'preview example', plan: '$10/day (example)', active: true, windows: [{ label: 'Today (UTC)', usedPercent: 62, resetsIn: '~17:00 CST (example)', resetAt: '17:00 CST (example)' }] },
    { id: 'b-cerebras', provider: 'cerebras', user: 'preview example', plan: '1M tokens/day (example)', active: true, windows: [{ label: 'Today (UTC)', usedPercent: 12, resetsIn: 'midnight UTC (example)', resetAt: '00:00 UTC (example)' }] },
    { id: 'b-llm7', provider: 'llm7', user: 'preview example', plan: '100K tokens/day (example)', active: true, windows: [{ label: 'Today (UTC)', usedPercent: 21, resetsIn: 'midnight UTC (example)', resetAt: '00:00 UTC (example)' }] },
    { id: 'b-sambanova', provider: 'sambanova', user: 'preview example', plan: '20 req/day/model (example)', active: true, windows: [{ label: 'Today (UTC)', usedPercent: 15, resetsIn: 'midnight UTC (example)', resetAt: '00:00 UTC (example)' }] },
  ],
  groups: [
    { id: 'coder', name: 'coder', routing: 'rotate', members: ['google/gemini-3-flash-preview', 'qwen3.6-27b', 'nvidia-lightning', 'zai-glm-4.7-flash', 'gpt-oss-120b'], rules: [{ use: 'google/gemini-3-flash-preview', intent: 'Weighted rotation 4/3/2/1, quota-aware, size split (small: whole pool; large: high-capacity only, Groq excluded for large first-pick), Kaggle 60% cap: above 60% Kaggle leaves first-pick, stays in failover.' }] },
    { id: 'fast', name: 'fast', routing: 'rotate', members: ['google/gemini-3-flash-preview', 'qwen3.6-27b', 'nvidia-lightning', 'zai-glm-4.7-flash', 'gpt-oss-120b'], rules: [{ use: 'google/gemini-3-flash-preview', intent: 'Weighted rotation 4/3/2/1, quota-aware, size split.' }] },
    { id: 'reasoning', name: 'reasoning', routing: 'rotate', members: ['google/gemini-3-flash-preview', 'nvidia-lightning', 'zai-glm-4.7-flash', 'qwen3.6-27b', 'gpt-oss-120b'], rules: [{ use: 'google/gemini-3-flash-preview', intent: 'Weighted rotation 4/3/2/1, quota-aware, size split. Qwen sits near the tail in this alias (preview example).' }] },
    { id: 'vision', name: 'vision', routing: 'order', members: ['google/gemini-3-flash-preview', 'laguna-xs'], rules: [{ use: 'google/gemini-3-flash-preview', intent: 'Image input only, not image generation. First available, then failover.' }] },
  ],
  usage: [
    { route_id: 1790000000111, t: new Date().toISOString(), agent: 'cline', provider: 'kaggle', model: 'google/gemini-3-flash-preview', req: 'coder', served: 'google/gemini-3-flash-preview', in: 68234, out: 1240, cache_read: 0, billedTokens: 69474, validator: 'pass', ms: 3200, ttft_ms: 210, tps: 92.5, costUSD: 0, status: 200, ep: '/v1/chat/completions', session: '11111111-2222-3333-4444-555555555501', maskedCount: 0 },
    { route_id: 1790000000112, t: new Date().toISOString(), agent: 'roo', provider: 'qwen-gpu', model: 'qwen3.5-9b', req: 'coder', served: 'qwen3.5-9b', in: 2684, out: 176, cache_read: 0, billedTokens: 2860, validator: 'pass', ms: 78128, ttft_ms: 210, tps: 92.5, costUSD: 0, status: 200, ep: '/v1/chat/completions', session: '11111111-2222-3333-4444-555555555502', maskedCount: 0 },
    { route_id: 1790000000113, t: new Date().toISOString(), agent: 'cline', provider: 'nvidia', model: 'nvidia-lightning', req: 'fast', served: 'nvidia-lightning', in: 1940, out: 520, cache_read: 0, billedTokens: 2460, validator: 'pass', ms: 1400, ttft_ms: 210, tps: 92.5, costUSD: 0, status: 200, ep: '/v1/chat/completions', session: '11111111-2222-3333-4444-555555555503', maskedCount: 0 },
    { route_id: 1790000000114, t: new Date().toISOString(), agent: 'cline', provider: 'qwen-gpu', model: 'qwen3.5-9b', req: 'coder', served: 'qwen3.5-9b', in: 0, out: 0, cache_read: 0, billedTokens: 0, validator: 'server_error', ms: 79, ttft_ms: 0, tps: 0, costUSD: 0, status: 502, ep: '/v1/chat/completions', session: '11111111-2222-3333-4444-555555555504', maskedCount: 0, err: 'preview example: upstream 530 (tunnel offline example), not a live failure' },
  ],
  sessions: [
    { id: '11111111-2222-3333-4444-555555555501', agent: 'cline', title: 'coder -> google/gemini-3-flash-preview (preview audit row)', cwd: '~/work/example (preview)', model: 'google/gemini-3-flash-preview', turns: 1, tokens: 69474, billedTokens: 69474, updatedAt: new Date().toISOString() },
    { id: '11111111-2222-3333-4444-555555555502', agent: 'roo', title: 'coder -> qwen3.5-9b (preview audit row)', cwd: '~/work/example (preview)', model: 'qwen3.5-9b', turns: 1, tokens: 2860, billedTokens: 2860, updatedAt: new Date().toISOString() },
    { id: '11111111-2222-3333-4444-555555555503', agent: 'cline', title: 'fast -> nvidia-lightning (preview audit row)', cwd: '~/work/example (preview)', model: 'nvidia-lightning', turns: 1, tokens: 2460, billedTokens: 2460, updatedAt: new Date().toISOString() },
  ],
  skills: [
    { name: 'openrouter', description: 'Provider name -> base-URL preset: https://openrouter.ai/api/v1 (preview example).', agents: ['cline', 'roo'], source: { kind: 'folder', dir: 'presets (preview)' } },
    { name: 'zhipu', description: 'Provider name -> base-URL preset: https://open.bigmodel.cn/api/paas/v4 (preview example).', agents: ['cline'], source: { kind: 'folder', dir: 'presets (preview)' } },
    { name: 'nvidia', description: 'Provider name -> base-URL preset: https://integrate.api.nvidia.com/v1 (preview example).', agents: ['cline'], source: { kind: 'folder', dir: 'presets (preview)' } },
    { name: 'cerebras', description: 'Provider name -> base-URL preset: https://api.cerebras.ai/v1 (preview example).', agents: ['cline'], source: { kind: 'folder', dir: 'presets (preview)' } },
    { name: 'kaggle-template', description: 'Kaggle route templates only: Model Proxy + Kaggle GPU (preview example). No other vendor route templates.', agents: ['cline', 'roo'], source: { kind: 'folder', dir: 'presets (preview)' } },
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
        agent: 'cline',
        provider: 'kaggle',
        kind: 'API_KEY',
        action: 'Mask',
        placeholder: '{{API_KEY_k3v9x2mq}}',
        previewPrefix: 'sk-proj…',
        length: 51,
      },
      {
        id: 'msk-900',
        timestamp: '9m ago',
        agent: 'roo',
        provider: 'kaggle',
        kind: 'EMAIL',
        action: 'Mask',
        placeholder: '{{EMAIL_81a2c90f}}',
        previewPrefix: 'ops-on…',
        length: 24,
      },
      {
        id: 'msk-899',
        timestamp: '41m ago',
        agent: 'cline',
        provider: 'kaggle',
        kind: 'PRIVATE_KEY',
        action: 'Mask',
        placeholder: '{{PRIVATE_KEY_4f91e0bc}}',
        previewPrefix: '-----B…',
        length: 1679,
      },
    ],
  },
  settings: {
    privacyMode: 'off',
  secretaryRoute: 'OpenRouter Nemotron Lightning (example, allowSensitive + privateOnly)',
  kaggleTimerStatus: 'Preview example: Model Proxy key refreshes before 1200s expiry; real timer status comes from the Go backend.',
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
        protocol: 'OpenAI Chat (/v1/chat/completions)',
        baseUrl: baseUrl || `https://api.${cleanId}.com/v1`,
        routing: routing || 'order',
        models: Array.isArray(models) && models.length ? models : [`${cleanId}-pro`, `${cleanId}-flash`],
        keys: [{ name: keyName || 'default', maskedKey, requestsToday: 0 }],
        dailyBudget: 'example daily budget',
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
        routing: routing || 'rotate',
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

  // Update settings
  app.post('/api/settings', (req, res) => {
    Object.assign(state.settings, req.body || {});
    res.json(state);
  });

  // Railways preview gateway simulator: OpenAI Chat only
  const handleGatewayRequest = (protocolEndpoint: string) => (req: express.Request, res: express.Response) => {
    const body = req.body || {};
    const requestedModel: string = body.model || 'coder';
    const agentName: string = body.agent || (req.headers['x-switchyard-agent'] as string) || 'cline';
    const rawPrompt: string =
      body.prompt ||
      body.messages?.[body.messages.length - 1]?.content ||
      body.input ||
      'Check connection with sk-proj-9918273645109283abcd and ops@company.io';

    const { masked, detections } = state.settings.maskingEnabled
      ? maskText(typeof rawPrompt === 'string' ? rawPrompt : JSON.stringify(rawPrompt), state.settings.neverMask)
      : { masked: String(rawPrompt), detections: [] };

    // Resolve alias (coder/fast/reasoning/vision) or direct-pin by exact modelId (preview round-robin)
    let resolvedSpec = requestedModel;
    let routingNote = 'Direct-pin by exact modelId (preview)';
    const alias = state.groups.find((g) => g.id === requestedModel || g.id === requestedModel.replace('group/', ''));
    if (alias && alias.members.length > 0) {
      resolvedSpec = alias.members[state.usage.length % alias.members.length];
      routingNote = `Alias "${alias.id}" (${alias.routing}, preview round-robin) -> ${resolvedSpec}. Real Go gateway uses smooth WRR 4/3/2/1, quota-aware, size split, Kaggle 60% cap.`;
    }

    const [providerId, modelName] = resolvedSpec.includes('/')
      ? resolvedSpec.split('/')
      : ['kaggle', resolvedSpec];

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
      cache_read: 0,
      reasoning: 64,
      effort: 'high',
      ms: 640,
      ttft_ms: 210,
      tps: 92.5,
      costUSD: 0,
      status: 200,
      ep: protocolEndpoint,
      session: crypto.randomUUID(),
      maskedCount: detections.length,
    };

    record.billedTokens = record.in + record.out;
    record.validator = 'pass';
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

    const vendorReplyMasked = `Preview routed [${requestedModel}] → [${providerId}/${modelName}] (${routingNote}). Preview masking only; real Railways forwards verbatim.`;
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
