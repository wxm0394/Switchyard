import React, { useEffect, useMemo, useState } from 'react';
import {
  Activity,
  ArrowRight,
  BookOpen,
  Boxes,
  Check,
  ChevronDown,
  Copy,
  Cpu,
  EyeOff,
  FolderGit2,
  GitBranch,
  KeyRound,
  Layers,
  Lock,
  PanelTop,
  Play,
  Plus,
  RefreshCw,
  Search,
  Server,
  Settings as SettingsIcon,
  ShieldCheck,
  Sparkles,
  Terminal,
  Zap,
} from 'lucide-react';
import type { MagpieState, ViewId } from './types';

const NAV_SECTIONS: { group: string; items: { id: ViewId; label: string; icon: React.FC<{ className?: string }> }[] }[] = [
  {
    group: 'Work',
    items: [
      { id: 'agents', label: 'Agents', icon: Terminal },
      { id: 'sessions', label: 'Sessions', icon: FolderGit2 },
    ],
  },
  {
    group: 'Models',
    items: [
      { id: 'providers', label: 'Providers', icon: KeyRound },
      { id: 'routing', label: 'Routing', icon: GitBranch },
    ],
  },
  {
    group: 'Insights',
    items: [
      { id: 'gateway', label: 'Gateway', icon: Server },
      { id: 'usage', label: 'Usage', icon: Activity },
    ],
  },
  {
    group: 'Extend',
    items: [
      { id: 'library', label: 'Library', icon: BookOpen },
      { id: 'plugins', label: 'Plugins', icon: Boxes },
    ],
  },
];

export default function App() {
  const [state, setState] = useState<MagpieState | null>(null);
  const [view, setView] = useState<ViewId>('agents');
  const [panelMode, setPanelMode] = useState(false);
  const [copiedText, setCopiedText] = useState<string | null>(null);

  // Agent picker popover state
  const [openPickerAgent, setOpenPickerAgent] = useState<string | null>(null);
  const [modelSearch, setModelSearch] = useState('');

  // Add Provider modal state
  const [showAddProvider, setShowAddProvider] = useState(false);
  const [newProvPreset, setNewProvPreset] = useState('openrouter');
  const [newProvName, setNewProvName] = useState('OpenRouter');
  const [newProvUrl, setNewProvUrl] = useState('https://openrouter.ai/api/v1');
  const [newProvKey, setNewProvKey] = useState('');
  const [newProvModels, setNewProvModels] = useState('anthropic/claude-sonnet-4.6, deepseek/deepseek-v4-pro');

  // Add Group state
  const [showAddGroup, setShowAddGroup] = useState(false);
  const [newGroupName, setNewGroupName] = useState('');
  const [newGroupRouting, setNewGroupRouting] = useState<'smart' | 'order' | 'rotate' | 'usage' | 'pace'>('smart');
  const [newGroupMembers, setNewGroupMembers] = useState('relay/claude-sonnet-4-6, codex/gpt-5.5');

  // Gateway playground state
  const [gwAgent, setGwAgent] = useState('claude');
  const [gwEndpoint, setGwEndpoint] = useState<'/v1/messages' | '/v1/chat/completions' | '/v1/responses'>('/v1/messages');
  const [gwModel, setGwModel] = useState('group/coding');
  const [gwPrompt, setGwPrompt] = useState(
    'Debug why token sk-proj-8819203948109283abcdforprod fails for user dev.ops@acme-internal.io on 10.24.18.92'
  );
  const [gwLoading, setGwLoading] = useState(false);
  const [gwResult, setGwResult] = useState<{
    model: string;
    routingNote: string;
    maskedPromptSentToVendor: string;
    unmaskedReplyToAgent: string;
    detections: { kind: string; placeholder: string; previewPrefix: string; length: number }[];
  } | null>(null);

  // Settings live masking preview state
  const [maskTryInput, setMaskTryInput] = useState(
    'export OPENAI_API_KEY=sk-live-51928374659182736455\nContact: security-team@corp-domain.org'
  );
  const [maskTryOutput, setMaskTryOutput] = useState<{
    masked: string;
    detections: { kind: string; placeholder: string; previewPrefix: string; length: number }[];
  } | null>(null);

  const [refreshingQuota, setRefreshingQuota] = useState<string | null>(null);
  const [usageTab, setUsageTab] = useState<'allowances' | 'ledger' | 'masking'>('allowances');

  useEffect(() => {
    fetch('/api/state')
      .then((r) => r.json())
      .then((data: MagpieState) => setState(data))
      .catch((err) => console.error('Failed to load magpie state:', err));
  }, []);

  useEffect(() => {
    const timer = setTimeout(() => {
      fetch('/api/masking/preview', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ text: maskTryInput }),
      })
        .then((r) => r.json())
        .then((d) => setMaskTryOutput(d))
        .catch(() => {});
    }, 150);
    return () => clearTimeout(timer);
  }, [maskTryInput, state?.settings.neverMask]);

  const allModelSpecs = useMemo(() => {
    if (!state) return [];
    const specs: { spec: string; label: string; category: string }[] = [];
    for (const g of state.groups) {
      specs.push({
        spec: `group/${g.id}`,
        label: `group/${g.id} (${g.routing} · ${g.members.length} models)`,
        category: 'Routing Groups',
      });
    }
    for (const p of state.providers) {
      for (const m of p.models) {
        specs.push({
          spec: `${p.id}/${m}`,
          label: `${p.id}/${m}`,
          category: p.name,
        });
      }
    }
    return specs;
  }, [state]);

  const copyText = (text: string, label: string) => {
    navigator.clipboard?.writeText(text);
    setCopiedText(label);
    setTimeout(() => setCopiedText(null), 1800);
  };

  const updateAgentModel = async (agentId: string, model?: string, effort?: string) => {
    const res = await fetch('/api/agents/model', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ agentId, model, effort }),
    });
    if (res.ok) {
      setState(await res.json());
      setOpenPickerAgent(null);
    }
  };

  const applyProfile = async (profileId: string) => {
    const res = await fetch('/api/profiles/apply', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ profileId }),
    });
    if (res.ok) setState(await res.json());
  };

  const handleAddProvider = async (e: React.FormEvent) => {
    e.preventDefault();
    const res = await fetch('/api/providers', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        id: newProvPreset,
        name: newProvName,
        baseUrl: newProvUrl,
        key: newProvKey,
        models: newProvModels
          .split(',')
          .map((s) => s.trim())
          .filter(Boolean),
      }),
    });
    if (res.ok) {
      setState(await res.json());
      setShowAddProvider(false);
      setNewProvKey('');
    }
  };

  const handleAddGroup = async (e: React.FormEvent) => {
    e.preventDefault();
    const members = newGroupMembers
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean);
    const res = await fetch('/api/groups', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: newGroupName,
        routing: newGroupRouting,
        members,
      }),
    });
    if (res.ok) {
      setState(await res.json());
      setShowAddGroup(false);
      setNewGroupName('');
    }
  };

  const refreshQuota = async (accountId?: string) => {
    setRefreshingQuota(accountId || 'all');
    const res = await fetch('/api/usage/quotas/refresh', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ accountId }),
    });
    if (res.ok) setState(await res.json());
    setTimeout(() => setRefreshingQuota(null), 400);
  };

  const sendGatewayTest = async (e: React.FormEvent) => {
    e.preventDefault();
    setGwLoading(true);
    try {
      const res = await fetch(gwEndpoint, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          agent: gwAgent,
          model: gwModel,
          prompt: gwPrompt,
        }),
      });
      const data = await res.json();
      setGwResult(data);
      const stateRes = await fetch('/api/state');
      if (stateRes.ok) setState(await stateRes.json());
    } finally {
      setGwLoading(false);
    }
  };

  const updateSettings = async (patch: Partial<MagpieState['settings']>) => {
    const res = await fetch('/api/settings', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(patch),
    });
    if (res.ok) setState(await res.json());
  };

  const togglePlugin = async (id: string, on: boolean) => {
    const res = await fetch('/api/plugins/builtin', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id, on }),
    });
    if (res.ok) setState(await res.json());
  };

  if (!state) {
    return (
      <div className="min-h-screen bg-[#0d0e11] text-[#e6e8ee] flex items-center justify-center">
        <div className="flex items-center gap-3 text-sm text-zinc-400">
          <div className="w-2.5 h-2.5 rounded-full bg-emerald-400 animate-pulse" />
          Starting magpie local gateway…
        </div>
      </div>
    );
  }

  // Quick Menu Bar Panel Mode (?mode=panel)
  if (panelMode) {
    return (
      <div className="min-h-screen bg-[#090a0d] text-[#e6e8ee] flex flex-col items-center justify-center p-4">
        <div className="w-full max-w-[420px] bg-[#13151a] border border-zinc-800/90 rounded-2xl shadow-2xl overflow-hidden">
          <div className="px-4 py-3 border-b border-zinc-800/80 flex items-center justify-between bg-[#16181f]">
            <div className="flex items-center gap-2">
              <span className="w-2 h-2 rounded-full bg-emerald-400" />
              <span className="font-semibold text-sm tracking-tight">magpie</span>
              <span className="text-xs text-zinc-500 font-mono">{state.settings.gatewayAddr}</span>
            </div>
            <button
              onClick={() => setPanelMode(false)}
              className="text-xs px-2.5 py-1 rounded-md bg-zinc-800 hover:bg-zinc-700 text-zinc-200 transition cursor-pointer"
            >
              Open Window
            </button>
          </div>

          <div className="p-3 space-y-3">
            <div className="flex items-center justify-between px-1">
              <span className="text-[11px] font-medium uppercase tracking-wider text-zinc-500">Profile</span>
              <div className="flex gap-1">
                {state.profiles.map((p) => (
                  <button
                    key={p.id}
                    onClick={() => applyProfile(p.id)}
                    className={`text-xs px-2 py-0.5 rounded transition cursor-pointer ${
                      state.settings.activeProfile === p.id
                        ? 'bg-sky-500/20 text-sky-300 border border-sky-500/40'
                        : 'text-zinc-400 hover:bg-zinc-800'
                    }`}
                  >
                    {p.name}
                  </button>
                ))}
              </div>
            </div>

            <div className="space-y-1.5">
              <div className="text-[11px] font-medium uppercase tracking-wider text-zinc-500 px-1">Wired Agents</div>
              {state.agents.map((a) => (
                <div
                  key={a.id}
                  className="flex items-center justify-between px-2.5 py-2 rounded-lg bg-[#181b22] border border-zinc-800/60"
                >
                  <div>
                    <div className="text-xs font-medium text-zinc-200">{a.name}</div>
                    <div className="text-[11px] text-zinc-500 font-mono">{a.configPath}</div>
                  </div>
                  <select
                    value={a.model}
                    onChange={(e) => updateAgentModel(a.id, e.target.value)}
                    className="bg-zinc-900 border border-zinc-700/80 rounded-md px-2 py-1 text-xs font-mono text-sky-300 focus:outline-none"
                  >
                    {allModelSpecs.map((m) => (
                      <option key={m.spec} value={m.spec}>
                        {m.spec}
                      </option>
                    ))}
                  </select>
                </div>
              ))}
            </div>

            <div className="space-y-1.5 pt-1">
              <div className="flex items-center justify-between px-1">
                <span className="text-[11px] font-medium uppercase tracking-wider text-zinc-500">Allowances</span>
                <button
                  onClick={() => refreshQuota()}
                  className="text-[11px] text-zinc-400 hover:text-zinc-200 flex items-center gap-1 cursor-pointer"
                >
                  <RefreshCw className={`w-3 h-3 ${refreshingQuota ? 'animate-spin' : ''}`} />
                  Refresh
                </button>
              </div>
              {state.accounts.map((acc) => (
                <div
                  key={acc.id}
                  className="px-2.5 py-2 rounded-lg bg-[#181b22] border border-zinc-800/60 space-y-1.5"
                >
                  <div className="flex items-center justify-between text-xs">
                    <span className="font-medium text-zinc-200">
                      {acc.provider} · <span className="text-zinc-400 font-normal">{acc.user}</span>
                    </span>
                    <span className="text-[11px] px-1.5 py-0.5 rounded bg-zinc-800 text-zinc-300">{acc.plan}</span>
                  </div>
                  <div className="grid grid-cols-2 gap-2">
                    {acc.windows.map((w) => (
                      <div key={w.label} className="space-y-1">
                        <div className="flex justify-between text-[11px] text-zinc-400">
                          <span>{w.label}</span>
                          <span>
                            {w.usedPercent}% · {w.resetsIn}
                          </span>
                        </div>
                        <div className="h-1.5 rounded-full bg-zinc-800 overflow-hidden">
                          <div
                            className={`h-full rounded-full ${
                              w.usedPercent > 85 ? 'bg-amber-400' : 'bg-sky-400'
                            }`}
                            style={{ width: `${w.usedPercent}%` }}
                          />
                        </div>
                      </div>
                    ))}
                  </div>
                </div>
              ))}
            </div>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="min-h-screen bg-[#0d0e11] text-[#e6e8ee] flex">
      {/* Sidebar (#side) */}
      <aside
        id="side"
        className="w-60 shrink-0 bg-[#111318] border-r border-zinc-800/80 flex flex-col justify-between select-none"
      >
        <div className="p-4 space-y-5">
          {/* Brand header */}
          <div className="flex items-center justify-between px-2">
            <div className="flex items-center gap-2.5">
              <div className="w-7 h-7 rounded-lg bg-gradient-to-br from-sky-400 to-indigo-600 flex items-center justify-center text-zinc-950 font-bold text-sm shadow">
                M
              </div>
              <div>
                <div className="font-semibold text-sm leading-none tracking-tight">magpie</div>
                <div className="text-[11px] text-zinc-500 mt-0.5">Every agent&apos;s model</div>
              </div>
            </div>
            <button
              onClick={() => setPanelMode(true)}
              title="Switch to Menu Bar Quick Panel"
              className="p-1.5 rounded-md text-zinc-400 hover:text-zinc-200 hover:bg-zinc-800/70 transition cursor-pointer"
            >
              <PanelTop className="w-4 h-4" />
            </button>
          </div>

          {/* Grouped navigation (#nav) */}
          <nav id="nav" className="space-y-4">
            {NAV_SECTIONS.map((sec) => (
              <div key={sec.group} className="space-y-1">
                <div className="px-2.5 text-[11px] font-semibold uppercase tracking-wider text-zinc-500">
                  {sec.group}
                </div>
                {sec.items.map((item) => {
                  const Icon = item.icon;
                  const active = view === item.id;
                  return (
                    <button
                      key={item.id}
                      data-view={item.id}
                      onClick={() => setView(item.id)}
                      className={`w-full flex items-center gap-2.5 px-2.5 py-1.5 rounded-lg text-sm font-medium transition cursor-pointer ${
                        active
                          ? 'bg-sky-500/15 text-sky-300 border border-sky-500/30'
                          : 'text-zinc-400 hover:text-zinc-200 hover:bg-zinc-800/50 border border-transparent'
                      }`}
                    >
                      <Icon className="w-4 h-4 shrink-0" />
                      <span>{item.label}</span>
                    </button>
                  );
                })}
              </div>
            ))}
          </nav>
        </div>

        {/* Sidebar Foot (.side-foot) */}
        <div className="p-3 border-t border-zinc-800/80 space-y-2">
          <button
            id="prefs"
            data-view="settings"
            onClick={() => setView('settings')}
            className={`w-full flex items-center justify-between px-2.5 py-1.5 rounded-lg text-sm font-medium transition cursor-pointer ${
              view === 'settings'
                ? 'bg-sky-500/15 text-sky-300 border border-sky-500/30'
                : 'text-zinc-400 hover:text-zinc-200 hover:bg-zinc-800/50 border border-transparent'
            }`}
          >
            <span className="flex items-center gap-2.5">
              <SettingsIcon className="w-4 h-4" />
              Settings
            </span>
            {state.settings.maskingEnabled && (
              <span className="text-[10px] px-1.5 py-0.5 rounded bg-emerald-500/15 text-emerald-300 border border-emerald-500/30">
                Masking On
              </span>
            )}
          </button>

          <div className="px-2.5 py-2 rounded-lg bg-[#161920] border border-zinc-800/70 flex items-center justify-between">
            <div className="flex items-center gap-2 min-w-0">
              <span className="w-2 h-2 rounded-full bg-emerald-400 shrink-0" />
              <div className="truncate">
                <div className="text-[10px] uppercase tracking-wider text-zinc-500">Local Gateway</div>
                <div className="text-xs font-mono text-zinc-300 truncate">{state.settings.gatewayAddr}</div>
              </div>
            </div>
            <button
              onClick={() => copyText(state.gatewayUrl, 'gw-addr')}
              className="p-1 rounded hover:bg-zinc-800 text-zinc-400 hover:text-zinc-200 cursor-pointer"
              title="Copy Gateway URL"
            >
              {copiedText === 'gw-addr' ? <Check className="w-3.5 h-3.5 text-emerald-400" /> : <Copy className="w-3.5 h-3.5" />}
            </button>
          </div>
        </div>
      </aside>

      {/* Main Content Area */}
      <main className="flex-1 min-w-0 flex flex-col">
        {/* Top Breadcrumb & Context Header */}
        <header className="h-14 border-b border-zinc-800/80 px-6 flex items-center justify-between bg-[#0f1116]/80 backdrop-blur sticky top-0 z-20">
          <div className="flex items-center gap-2 text-sm">
            <span className="text-zinc-500">magpie</span>
            <span className="text-zinc-600">/</span>
            <span className="font-semibold text-zinc-100 capitalize">{view}</span>
          </div>

          <div className="flex items-center gap-3">
            {/* Profile Switcher */}
            <div className="flex items-center gap-1.5 bg-[#161920] border border-zinc-800 rounded-lg p-1">
              <span className="text-xs text-zinc-400 px-2">Profile:</span>
              {state.profiles.map((prof) => (
                <button
                  key={prof.id}
                  onClick={() => applyProfile(prof.id)}
                  className={`text-xs px-2.5 py-1 rounded-md font-medium transition cursor-pointer ${
                    state.settings.activeProfile === prof.id
                      ? 'bg-sky-500 text-zinc-950 shadow-sm'
                      : 'text-zinc-400 hover:text-zinc-200'
                  }`}
                >
                  {prof.name}
                </button>
              ))}
            </div>

            <button
              onClick={() => setView('gateway')}
              className="text-xs px-3 py-1.5 rounded-lg bg-emerald-500/15 text-emerald-300 border border-emerald-500/30 hover:bg-emerald-500/25 transition flex items-center gap-1.5 cursor-pointer"
            >
              <Play className="w-3.5 h-3.5" />
              Test Gateway Turn
            </button>
          </div>
        </header>

        {/* Page Body */}
        <div className="p-6 max-w-6xl w-full mx-auto space-y-6">
          {/* ==================== AGENTS VIEW ==================== */}
          {view === 'agents' && (
            <div className="space-y-6">
              <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-4 bg-[#13161d] border border-zinc-800/80 rounded-xl p-4">
                <div>
                  <h1 className="text-base font-semibold text-zinc-100">
                    Every agent&apos;s model. One place.
                  </h1>
                  <p className="text-xs text-zinc-400 mt-0.5">
                    Pick any model or routing group for any installed coding agent. magpie rewrites only the model key in the agent&apos;s own config file, preserving comments and formatting.
                  </p>
                </div>
                <div className="flex items-center gap-2 shrink-0">
                  <span className="text-xs px-2.5 py-1 rounded-md bg-zinc-800/80 text-zinc-300 font-mono">
                    {state.agents.length} installed agents
                  </span>
                </div>
              </div>

              <div className="space-y-2.5">
                {state.agents.map((agent) => {
                  const isOpen = openPickerAgent === agent.id;
                  const filteredSpecs = allModelSpecs.filter(
                    (s) =>
                      !modelSearch ||
                      s.spec.toLowerCase().includes(modelSearch.toLowerCase()) ||
                      s.category.toLowerCase().includes(modelSearch.toLowerCase())
                  );

                  return (
                    <div
                      key={agent.id}
                      className="bg-[#13161d] border border-zinc-800/80 hover:border-zinc-700/80 rounded-xl p-4 transition"
                    >
                      <div className="flex flex-col md:flex-row md:items-center justify-between gap-4">
                        <div className="space-y-1">
                          <div className="flex items-center gap-2.5">
                            <span className="w-2 h-2 rounded-full bg-emerald-400" />
                            <span className="font-semibold text-sm text-zinc-100">{agent.name}</span>
                            <span className="text-[11px] px-2 py-0.5 rounded bg-zinc-800 text-zinc-400 font-mono">
                              v{agent.version}
                            </span>
                            <span className="text-[11px] px-2 py-0.5 rounded bg-zinc-800/60 text-zinc-400">
                              {agent.category}
                            </span>
                          </div>
                          <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-zinc-400">
                            <span className="font-mono text-zinc-500">{agent.configPath}</span>
                            <span>·</span>
                            <span>{agent.protocol}</span>
                            <span>·</span>
                            <span>{agent.activeSessions} sessions</span>
                          </div>
                        </div>

                        <div className="flex flex-wrap items-center gap-2.5 relative">
                          {/* Reasoning Effort Selector */}
                          <div className="flex items-center bg-[#0e1015] border border-zinc-800 rounded-lg p-0.5">
                            {(['low', 'medium', 'high', 'xhigh'] as const).map((eff) => (
                              <button
                                key={eff}
                                onClick={() => updateAgentModel(agent.id, undefined, eff)}
                                className={`text-[11px] px-2 py-1 rounded-md font-mono transition cursor-pointer ${
                                  agent.effort === eff
                                    ? 'bg-zinc-800 text-sky-300 font-medium'
                                    : 'text-zinc-500 hover:text-zinc-300'
                                }`}
                              >
                                {eff}
                              </button>
                            ))}
                          </div>

                          {/* Model Picker Button */}
                          <button
                            onClick={() => {
                              setOpenPickerAgent(isOpen ? null : agent.id);
                              setModelSearch('');
                            }}
                            className="flex items-center justify-between gap-2.5 px-3 py-1.5 rounded-lg bg-[#191d26] hover:bg-[#1f2430] border border-sky-500/30 text-sky-300 text-xs font-mono min-w-[230px] cursor-pointer"
                          >
                            <span className="truncate">{agent.model}</span>
                            <ChevronDown className="w-3.5 h-3.5 shrink-0 text-zinc-400" />
                          </button>

                          {/* Searchable Model Picker Popover */}
                          {isOpen && (
                            <div className="absolute right-0 top-11 w-80 bg-[#161922] border border-zinc-700 rounded-xl shadow-2xl p-2 z-30 space-y-2">
                              <div className="relative">
                                <Search className="w-3.5 h-3.5 text-zinc-500 absolute left-2.5 top-2.5" />
                                <input
                                  type="text"
                                  value={modelSearch}
                                  onChange={(e) => setModelSearch(e.target.value)}
                                  placeholder="Search provider/model or group/…"
                                  autoFocus
                                  className="w-full bg-[#0e1015] border border-zinc-800 rounded-lg pl-8 pr-3 py-1.5 text-xs text-zinc-200 focus:outline-none focus:border-sky-500"
                                />
                              </div>
                              <div className="max-h-60 overflow-y-auto space-y-1 pr-1">
                                {filteredSpecs.map((item) => (
                                  <button
                                    key={item.spec}
                                    onClick={() => updateAgentModel(agent.id, item.spec)}
                                    className={`w-full text-left px-2.5 py-1.5 rounded-lg text-xs font-mono flex items-center justify-between transition cursor-pointer ${
                                      agent.model === item.spec
                                        ? 'bg-sky-500/20 text-sky-300'
                                        : 'text-zinc-300 hover:bg-zinc-800/80'
                                    }`}
                                  >
                                    <span className="truncate">{item.spec}</span>
                                    <span className="text-[10px] text-zinc-500 font-sans ml-2 shrink-0">
                                      {item.category}
                                    </span>
                                  </button>
                                ))}
                              </div>
                            </div>
                          )}
                        </div>
                      </div>

                      {/* Agent Shortlist Pills */}
                      <div className="mt-3 pt-2.5 border-t border-zinc-800/50 flex items-center gap-1.5 flex-wrap">
                        <span className="text-[11px] text-zinc-500 mr-1">Shortlist:</span>
                        {agent.shortlist.map((spec) => (
                          <button
                            key={spec}
                            onClick={() => updateAgentModel(agent.id, spec)}
                            className={`text-[11px] font-mono px-2 py-0.5 rounded-md transition cursor-pointer ${
                              agent.model === spec
                                ? 'bg-sky-500/15 text-sky-300 border border-sky-500/30'
                                : 'bg-zinc-800/50 text-zinc-400 hover:text-zinc-200'
                            }`}
                          >
                            {spec}
                          </button>
                        ))}
                      </div>
                    </div>
                  );
                })}
              </div>
            </div>
          )}

          {/* ==================== PROVIDERS VIEW ==================== */}
          {view === 'providers' && (
            <div className="space-y-6">
              <div className="flex items-center justify-between">
                <div>
                  <h1 className="text-base font-semibold text-zinc-100">Providers &amp; Subscriptions</h1>
                  <p className="text-xs text-zinc-400 mt-0.5">
                    API keys, Sub2API relays, local servers, and signed-in agent subscriptions served to every agent.
                  </p>
                </div>
                <button
                  onClick={() => setShowAddProvider(!showAddProvider)}
                  className="text-xs px-3 py-1.5 rounded-lg bg-sky-500 hover:bg-sky-400 text-zinc-950 font-semibold flex items-center gap-1.5 transition cursor-pointer"
                >
                  <Plus className="w-3.5 h-3.5" />
                  Add provider
                </button>
              </div>

              {showAddProvider && (
                <form
                  onSubmit={handleAddProvider}
                  className="bg-[#151821] border border-sky-500/40 rounded-xl p-4 space-y-4"
                >
                  <div className="flex items-center justify-between">
                    <span className="text-sm font-semibold text-zinc-100">Add Provider Preset or Custom Endpoint</span>
                    <button
                      type="button"
                      onClick={() => setShowAddProvider(false)}
                      className="text-xs text-zinc-400 hover:text-zinc-200 cursor-pointer"
                    >
                      Cancel
                    </button>
                  </div>
                  <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                    <div>
                      <label className="block text-xs text-zinc-400 mb-1">Preset / ID</label>
                      <input
                        value={newProvPreset}
                        onChange={(e) => setNewProvPreset(e.target.value)}
                        className="w-full bg-[#0e1015] border border-zinc-800 rounded-lg px-3 py-1.5 text-xs font-mono text-zinc-200"
                        required
                      />
                    </div>
                    <div>
                      <label className="block text-xs text-zinc-400 mb-1">Display Name</label>
                      <input
                        value={newProvName}
                        onChange={(e) => setNewProvName(e.target.value)}
                        className="w-full bg-[#0e1015] border border-zinc-800 rounded-lg px-3 py-1.5 text-xs text-zinc-200"
                        required
                      />
                    </div>
                    <div>
                      <label className="block text-xs text-zinc-400 mb-1">Base URL</label>
                      <input
                        value={newProvUrl}
                        onChange={(e) => setNewProvUrl(e.target.value)}
                        className="w-full bg-[#0e1015] border border-zinc-800 rounded-lg px-3 py-1.5 text-xs font-mono text-zinc-200"
                      />
                    </div>
                    <div>
                      <label className="block text-xs text-zinc-400 mb-1">API Key</label>
                      <input
                        type="password"
                        value={newProvKey}
                        onChange={(e) => setNewProvKey(e.target.value)}
                        placeholder="sk-…"
                        className="w-full bg-[#0e1015] border border-zinc-800 rounded-lg px-3 py-1.5 text-xs font-mono text-zinc-200"
                      />
                    </div>
                    <div className="md:col-span-2">
                      <label className="block text-xs text-zinc-400 mb-1">Models (comma-separated)</label>
                      <input
                        value={newProvModels}
                        onChange={(e) => setNewProvModels(e.target.value)}
                        className="w-full bg-[#0e1015] border border-zinc-800 rounded-lg px-3 py-1.5 text-xs font-mono text-zinc-200"
                      />
                    </div>
                  </div>
                  <div className="flex justify-end">
                    <button
                      type="submit"
                      className="text-xs px-4 py-1.5 rounded-lg bg-sky-500 text-zinc-950 font-semibold cursor-pointer"
                    >
                      Save Provider
                    </button>
                  </div>
                </form>
              )}

              {/* Subscription Accounts Section */}
              <div className="space-y-3">
                <div className="flex items-center justify-between">
                  <h2 className="text-xs font-semibold uppercase tracking-wider text-zinc-400">
                    Signed-In Subscription Accounts (Failover Pool)
                  </h2>
                  <button
                    onClick={() => refreshQuota()}
                    className="text-xs text-sky-400 hover:text-sky-300 flex items-center gap-1.5 cursor-pointer"
                  >
                    <RefreshCw className={`w-3.5 h-3.5 ${refreshingQuota ? 'animate-spin' : ''}`} />
                    Refresh allowances
                  </button>
                </div>
                <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
                  {state.accounts.map((acc) => (
                    <div
                      key={acc.id}
                      className="bg-[#13161d] border border-zinc-800/80 rounded-xl p-4 space-y-3"
                    >
                      <div className="flex items-center justify-between">
                        <div>
                          <div className="text-xs font-semibold text-zinc-200 uppercase tracking-wide">
                            {acc.provider}
                          </div>
                          <div className="text-xs text-zinc-400 font-mono">{acc.user}</div>
                        </div>
                        <span className="text-[11px] px-2 py-0.5 rounded bg-sky-500/15 text-sky-300 border border-sky-500/30 font-medium">
                          {acc.plan}
                        </span>
                      </div>

                      <div className="space-y-2">
                        {acc.windows.map((w) => (
                          <div key={w.label} className="space-y-1">
                            <div className="flex justify-between text-xs">
                              <span className="text-zinc-400">{w.label} window</span>
                              <span className="font-mono text-zinc-300">
                                {w.usedPercent}% used · resets in {w.resetsIn}
                              </span>
                            </div>
                            <div className="h-1.5 rounded-full bg-zinc-800 overflow-hidden">
                              <div
                                className={`h-full rounded-full transition-all ${
                                  w.usedPercent >= 85 ? 'bg-amber-400' : 'bg-emerald-400'
                                }`}
                                style={{ width: `${w.usedPercent}%` }}
                              />
                            </div>
                          </div>
                        ))}
                      </div>
                      {acc.credits && (
                        <div className="text-[11px] text-zinc-400 pt-1 border-t border-zinc-800/60">
                          Credits balance: <span className="text-zinc-200 font-mono">{acc.credits}</span>
                        </div>
                      )}
                    </div>
                  ))}
                </div>
              </div>

              {/* Providers List */}
              <div className="space-y-3">
                <h2 className="text-xs font-semibold uppercase tracking-wider text-zinc-400">
                  Configured Providers ({state.providers.length})
                </h2>
                <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                  {state.providers.map((p) => (
                    <div
                      key={p.id}
                      className="bg-[#13161d] border border-zinc-800/80 rounded-xl p-4 space-y-3 flex flex-col justify-between"
                    >
                      <div className="space-y-2">
                        <div className="flex items-start justify-between gap-2">
                          <div>
                            <div className="flex items-center gap-2">
                              <span className="font-semibold text-sm text-zinc-100">{p.name}</span>
                              <span className="text-[11px] font-mono px-1.5 py-0.5 rounded bg-zinc-800 text-zinc-400">
                                {p.id}
                              </span>
                              <span className="text-[11px] px-1.5 py-0.5 rounded bg-zinc-800/60 text-zinc-400">
                                {p.routing}
                              </span>
                            </div>
                            <div className="text-xs font-mono text-zinc-500 mt-0.5">{p.baseUrl}</div>
                          </div>
                          {p.balanceUSD !== undefined && (
                            <div className="text-right">
                              <div className="text-[10px] uppercase text-zinc-500">Balance</div>
                              <div className="text-sm font-mono font-semibold text-emerald-400">
                                ${p.balanceUSD.toFixed(2)}
                              </div>
                            </div>
                          )}
                        </div>

                        {p.move?.state === 'plugin' && (
                          <div className="text-[11px] px-2.5 py-1.5 rounded-lg bg-indigo-500/10 border border-indigo-500/30 text-indigo-300 flex items-center justify-between">
                            <span>Served by OpenCode plugin:</span>
                            <span className="font-mono">{p.move.pluginPkg}</span>
                          </div>
                        )}

                        {p.windows && (
                          <div className="grid grid-cols-3 gap-2 pt-1">
                            {p.windows.map((w) => (
                              <div key={w.window} className="bg-[#0e1015] border border-zinc-800/80 rounded-lg p-2">
                                <div className="text-[10px] text-zinc-500 uppercase">{w.window} limit</div>
                                <div className="text-xs font-mono text-zinc-200 mt-0.5">
                                  ${w.used.toFixed(1)} / ${w.limit}
                                </div>
                                <div className="text-[10px] text-zinc-500">resets {w.resetsIn}</div>
                              </div>
                            ))}
                          </div>
                        )}

                        <div className="flex flex-wrap gap-1.5 pt-1">
                          {p.models.map((m) => (
                            <span
                              key={m}
                              className="text-[11px] font-mono px-2 py-0.5 rounded bg-zinc-800/80 text-zinc-300"
                            >
                              {p.id}/{m}
                            </span>
                          ))}
                        </div>
                      </div>

                      {p.keys.length > 0 && (
                        <div className="pt-2.5 border-t border-zinc-800/60 flex items-center justify-between text-xs text-zinc-400">
                          <div className="flex items-center gap-2 flex-wrap">
                            {p.keys.map((k) => (
                              <span
                                key={k.name}
                                className={`text-[11px] font-mono px-2 py-0.5 rounded ${
                                  k.off
                                    ? 'bg-zinc-900 text-zinc-600 line-through'
                                    : 'bg-zinc-800/70 text-zinc-300'
                                }`}
                              >
                                {k.name}: {k.maskedKey}
                              </span>
                            ))}
                          </div>
                        </div>
                      )}
                    </div>
                  ))}
                </div>
              </div>
            </div>
          )}

          {/* ==================== ROUTING VIEW ==================== */}
          {view === 'routing' && (
            <div className="space-y-6">
              <div className="flex items-center justify-between">
                <div>
                  <h1 className="text-base font-semibold text-zinc-100">Routing Groups &amp; Live Stage</h1>
                  <p className="text-xs text-zinc-400 mt-0.5">
                    Combine models across subscriptions and API keys into a single virtual model such as <code className="text-sky-300">group/coding</code>.
                  </p>
                </div>
                <button
                  onClick={() => setShowAddGroup(!showAddGroup)}
                  className="text-xs px-3 py-1.5 rounded-lg bg-sky-500 hover:bg-sky-400 text-zinc-950 font-semibold flex items-center gap-1.5 transition cursor-pointer"
                >
                  <Plus className="w-3.5 h-3.5" />
                  New routing group
                </button>
              </div>

              {showAddGroup && (
                <form
                  onSubmit={handleAddGroup}
                  className="bg-[#151821] border border-sky-500/40 rounded-xl p-4 space-y-3"
                >
                  <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
                    <div>
                      <label className="block text-xs text-zinc-400 mb-1">Group Name</label>
                      <input
                        value={newGroupName}
                        onChange={(e) => setNewGroupName(e.target.value)}
                        placeholder="daily-coding"
                        className="w-full bg-[#0e1015] border border-zinc-800 rounded-lg px-3 py-1.5 text-xs text-zinc-200"
                        required
                      />
                    </div>
                    <div>
                      <label className="block text-xs text-zinc-400 mb-1">Routing Policy</label>
                      <select
                        value={newGroupRouting}
                        onChange={(e) => setNewGroupRouting(e.target.value as any)}
                        className="w-full bg-[#0e1015] border border-zinc-800 rounded-lg px-3 py-1.5 text-xs text-zinc-200"
                      >
                        <option value="smart">smart (soonest reset with quota)</option>
                        <option value="order">order (first available, then failover)</option>
                        <option value="rotate">rotate (round-robin turns)</option>
                        <option value="usage">usage (least-used first)</option>
                        <option value="pace">pace (most weekly quota per hour left)</option>
                      </select>
                    </div>
                    <div>
                      <label className="block text-xs text-zinc-400 mb-1">Member Models (comma-separated)</label>
                      <input
                        value={newGroupMembers}
                        onChange={(e) => setNewGroupMembers(e.target.value)}
                        className="w-full bg-[#0e1015] border border-zinc-800 rounded-lg px-3 py-1.5 text-xs font-mono text-zinc-200"
                        required
                      />
                    </div>
                  </div>
                  <div className="flex justify-end gap-2">
                    <button
                      type="button"
                      onClick={() => setShowAddGroup(false)}
                      className="text-xs px-3 py-1.5 rounded-lg bg-zinc-800 text-zinc-300 cursor-pointer"
                    >
                      Cancel
                    </button>
                    <button
                      type="submit"
                      className="text-xs px-4 py-1.5 rounded-lg bg-sky-500 text-zinc-950 font-semibold cursor-pointer"
                    >
                      Create Group
                    </button>
                  </div>
                </form>
              )}

              {/* Routing Groups Cards */}
              <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                {state.groups.map((g) => (
                  <div
                    key={g.id}
                    className="bg-[#13161d] border border-zinc-800/80 rounded-xl p-4 space-y-3"
                  >
                    <div className="flex items-center justify-between">
                      <div className="flex items-center gap-2">
                        <span className="font-semibold text-sm text-zinc-100">{g.name}</span>
                        <span className="text-xs font-mono text-sky-300 bg-sky-500/10 border border-sky-500/30 px-2 py-0.5 rounded">
                          group/{g.id}
                        </span>
                      </div>
                      <span className="text-xs px-2 py-0.5 rounded bg-zinc-800 text-zinc-300 font-mono">
                        mode: {g.routing}
                      </span>
                    </div>

                    <div className="space-y-1.5">
                      <div className="text-[11px] text-zinc-500 uppercase tracking-wider">Failover Sequence</div>
                      <div className="flex items-center flex-wrap gap-1.5">
                        {g.members.map((m, idx) => (
                          <React.Fragment key={m}>
                            <span className="text-xs font-mono px-2.5 py-1 rounded-lg bg-[#0e1015] border border-zinc-800 text-zinc-200">
                              {idx + 1}. {m}
                            </span>
                            {idx < g.members.length - 1 && <ArrowRight className="w-3.5 h-3.5 text-zinc-600" />}
                          </React.Fragment>
                        ))}
                      </div>
                    </div>

                    {g.rules.length > 0 && (
                      <div className="pt-2 border-t border-zinc-800/60 space-y-1">
                        <div className="text-[11px] text-zinc-500">Intent &amp; Agent Rules</div>
                        {g.rules.map((r, i) => (
                          <div key={i} className="text-xs text-zinc-400 flex items-center gap-1.5">
                            <Sparkles className="w-3.5 h-3.5 text-amber-400 shrink-0" />
                            <span>
                              Route <span className="text-zinc-200">{r.intent || r.agents?.join(', ')}</span> →{' '}
                              <code className="text-sky-300 font-mono">{r.use}</code>
                            </span>
                          </div>
                        ))}
                      </div>
                    )}
                  </div>
                ))}
              </div>

              {/* Recent Routed Decisions */}
              <div className="bg-[#13161d] border border-zinc-800/80 rounded-xl p-4 space-y-3">
                <h2 className="text-xs font-semibold uppercase tracking-wider text-zinc-400">
                  Recent Gateway Routing Decisions
                </h2>
                <div className="space-y-2">
                  {state.usage.slice(0, 6).map((u) => (
                    <div
                      key={u.route_id}
                      className="flex flex-col sm:flex-row sm:items-center justify-between gap-2 px-3 py-2 rounded-lg bg-[#0e1015] border border-zinc-800/70 text-xs"
                    >
                      <div className="flex items-center gap-2 flex-wrap">
                        <span
                          className={`w-2 h-2 rounded-full ${
                            u.status === 200 ? 'bg-emerald-400' : 'bg-amber-400'
                          }`}
                        />
                        <span className="font-semibold text-zinc-200">{u.agent}</span>
                        <span className="text-zinc-500">asked</span>
                        <code className="font-mono text-sky-300">{u.req}</code>
                        <ArrowRight className="w-3.5 h-3.5 text-zinc-600" />
                        <code className="font-mono text-emerald-300">
                          {u.provider}/{u.served}
                        </code>
                        {u.providerAccount && (
                          <span className="text-[11px] text-zinc-400 font-mono">({u.providerAccount})</span>
                        )}
                        {u.providerKeyName && (
                          <span className="text-[11px] text-zinc-400 font-mono">[key: {u.providerKeyName}]</span>
                        )}
                      </div>
                      <div className="flex items-center gap-3 text-zinc-400 font-mono text-[11px]">
                        <span>{u.ep}</span>
                        {u.tps > 0 && <span>{u.tps.toFixed(1)} tok/s</span>}
                        <span className={u.status === 200 ? 'text-emerald-400' : 'text-amber-400'}>
                          HTTP {u.status}
                        </span>
                      </div>
                    </div>
                  ))}
                </div>
              </div>
            </div>
          )}

          {/* ==================== GATEWAY VIEW ==================== */}
          {view === 'gateway' && (
            <div className="space-y-6">
              <div className="bg-[#13161d] border border-zinc-800/80 rounded-xl p-4 space-y-3">
                <div className="flex items-center justify-between">
                  <div>
                    <h1 className="text-base font-semibold text-zinc-100">
                      Multi-Protocol Local Gateway ({state.gatewayUrl})
                    </h1>
                    <p className="text-xs text-zinc-400 mt-0.5">
                      Speaks OpenAI Chat, OpenAI Responses, Anthropic Messages, and Google Gemini, translating streaming, tool calls, and reasoning levels between any agent and provider.
                    </p>
                  </div>
                  <button
                    onClick={() =>
                      copyText(
                        `export OPENAI_BASE_URL=${state.gatewayUrl}/v1 OPENAI_API_KEY=magpie\nexport ANTHROPIC_BASE_URL=${state.gatewayUrl} ANTHROPIC_API_KEY=magpie\nexport GOOGLE_GEMINI_BASE_URL=${state.gatewayUrl} GEMINI_API_KEY=magpie`,
                        'env-export'
                      )
                    }
                    className="text-xs px-3 py-1.5 rounded-lg bg-zinc-800 hover:bg-zinc-700 text-zinc-200 flex items-center gap-1.5 cursor-pointer shrink-0"
                  >
                    {copiedText === 'env-export' ? (
                      <>
                        <Check className="w-3.5 h-3.5 text-emerald-400" /> Copied env exports
                      </>
                    ) : (
                      <>
                        <Copy className="w-3.5 h-3.5" /> Copy shell exports
                      </>
                    )}
                  </button>
                </div>

                <pre className="bg-[#0b0d12] border border-zinc-800/80 rounded-lg p-3 text-xs font-mono text-zinc-300 overflow-x-auto">
                  {`export OPENAI_BASE_URL=${state.gatewayUrl}/v1     OPENAI_API_KEY=magpie
export ANTHROPIC_BASE_URL=${state.gatewayUrl}     ANTHROPIC_API_KEY=magpie
export GOOGLE_GEMINI_BASE_URL=${state.gatewayUrl} GEMINI_API_KEY=magpie`}
                </pre>
              </div>

              {/* Interactive Gateway Turn Simulator */}
              <div className="bg-[#13161d] border border-zinc-800/80 rounded-xl p-5 space-y-4">
                <div className="flex items-center justify-between">
                  <div>
                    <h2 className="text-sm font-semibold text-zinc-100 flex items-center gap-2">
                      <Zap className="w-4 h-4 text-sky-400" />
                      Interactive Gateway Turn &amp; Privacy Masking Simulator
                    </h2>
                    <p className="text-xs text-zinc-400 mt-0.5">
                      Send a real HTTP POST to <code className="text-sky-300">{gwEndpoint}</code> on this server to test group routing and client-side secret masking.
                    </p>
                  </div>
                </div>

                <form onSubmit={sendGatewayTest} className="space-y-3">
                  <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
                    <div>
                      <label className="block text-xs text-zinc-400 mb-1">Calling Agent</label>
                      <select
                        value={gwAgent}
                        onChange={(e) => setGwAgent(e.target.value)}
                        className="w-full bg-[#0e1015] border border-zinc-800 rounded-lg px-3 py-1.5 text-xs text-zinc-200"
                      >
                        {state.agents.map((a) => (
                          <option key={a.id} value={a.id}>
                            {a.name} ({a.id})
                          </option>
                        ))}
                      </select>
                    </div>
                    <div>
                      <label className="block text-xs text-zinc-400 mb-1">Gateway Endpoint</label>
                      <select
                        value={gwEndpoint}
                        onChange={(e) => setGwEndpoint(e.target.value as any)}
                        className="w-full bg-[#0e1015] border border-zinc-800 rounded-lg px-3 py-1.5 text-xs font-mono text-zinc-200"
                      >
                        <option value="/v1/messages">POST /v1/messages (Anthropic)</option>
                        <option value="/v1/responses">POST /v1/responses (OpenAI Responses)</option>
                        <option value="/v1/chat/completions">POST /v1/chat/completions (OpenAI Chat)</option>
                      </select>
                    </div>
                    <div>
                      <label className="block text-xs text-zinc-400 mb-1">Requested Model or Group</label>
                      <select
                        value={gwModel}
                        onChange={(e) => setGwModel(e.target.value)}
                        className="w-full bg-[#0e1015] border border-zinc-800 rounded-lg px-3 py-1.5 text-xs font-mono text-sky-300"
                      >
                        {allModelSpecs.map((m) => (
                          <option key={m.spec} value={m.spec}>
                            {m.spec}
                          </option>
                        ))}
                      </select>
                    </div>
                  </div>

                  <div>
                    <label className="block text-xs text-zinc-400 mb-1">
                      Agent Prompt (include keys, emails, or IPs to test automatic placeholder masking)
                    </label>
                    <textarea
                      rows={2}
                      value={gwPrompt}
                      onChange={(e) => setGwPrompt(e.target.value)}
                      className="w-full bg-[#0e1015] border border-zinc-800 rounded-lg p-3 text-xs font-mono text-zinc-200 focus:outline-none focus:border-sky-500"
                    />
                  </div>

                  <div className="flex justify-end">
                    <button
                      type="submit"
                      disabled={gwLoading}
                      className="text-xs px-4 py-2 rounded-lg bg-sky-500 hover:bg-sky-400 text-zinc-950 font-semibold flex items-center gap-1.5 cursor-pointer"
                    >
                      <Play className="w-3.5 h-3.5" />
                      {gwLoading ? 'Routing through gateway…' : 'Send Request Through Gateway'}
                    </button>
                  </div>
                </form>

                {gwResult && (
                  <div className="mt-4 pt-4 border-t border-zinc-800 space-y-3">
                    <div className="flex flex-wrap items-center justify-between gap-2 text-xs">
                      <span className="text-emerald-400 font-medium">
                        ● Routed to <code className="font-mono">{gwResult.model}</code> — {gwResult.routingNote}
                      </span>
                      <span className="text-zinc-400">
                        {gwResult.detections.length} secret(s) masked before leaving computer
                      </span>
                    </div>
                    <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                      <div className="bg-[#0b0d12] border border-zinc-800 rounded-lg p-3 space-y-1">
                        <div className="text-[11px] font-semibold uppercase text-amber-400">
                          What Upstream Vendor Saw (Masked)
                        </div>
                        <div className="text-xs font-mono text-zinc-300 break-all">
                          {gwResult.maskedPromptSentToVendor}
                        </div>
                      </div>
                      <div className="bg-[#0b0d12] border border-zinc-800 rounded-lg p-3 space-y-1">
                        <div className="text-[11px] font-semibold uppercase text-emerald-400">
                          What Local Agent Received Back (Restored)
                        </div>
                        <div className="text-xs font-mono text-zinc-300 break-all">
                          {gwResult.unmaskedReplyToAgent}
                        </div>
                      </div>
                    </div>
                  </div>
                )}
              </div>
            </div>
          )}

          {/* ==================== USAGE VIEW ==================== */}
          {view === 'usage' && (
            <div className="space-y-6">
              <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-3">
                <div>
                  <h1 className="text-base font-semibold text-zinc-100">Usage, Quotas &amp; Masking Insights</h1>
                  <p className="text-xs text-zinc-400 mt-0.5">
                    Real-time token ledger, cache hit rates, subscription reset forecasts, and privacy masking audit log.
                  </p>
                </div>
                <div className="flex bg-[#14171f] border border-zinc-800 rounded-lg p-1 self-start">
                  {(
                    [
                      ['allowances', 'Allowances & Quotas'],
                      ['ledger', 'Request Ledger'],
                      ['masking', `Masking (${state.masking.total7d.toLocaleString()})`],
                    ] as const
                  ).map(([id, label]) => (
                    <button
                      key={id}
                      onClick={() => setUsageTab(id)}
                      className={`text-xs px-3 py-1 rounded-md font-medium transition cursor-pointer ${
                        usageTab === id ? 'bg-sky-500 text-zinc-950' : 'text-zinc-400 hover:text-zinc-200'
                      }`}
                    >
                      {label}
                    </button>
                  ))}
                </div>
              </div>

              {/* KPI Summary Row */}
              <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
                <div className="bg-[#13161d] border border-zinc-800/80 rounded-xl p-4">
                  <div className="text-[11px] uppercase tracking-wider text-zinc-500">30d Requests</div>
                  <div className="text-xl font-semibold text-zinc-100 mt-1 font-mono">1,428</div>
                  <div className="text-[11px] text-emerald-400 mt-0.5">99.1% served after failover</div>
                </div>
                <div className="bg-[#13161d] border border-zinc-800/80 rounded-xl p-4">
                  <div className="text-[11px] uppercase tracking-wider text-zinc-500">Prompt Cache Hit Rate</div>
                  <div className="text-xl font-semibold text-sky-400 mt-1 font-mono">84.6%</div>
                  <div className="text-[11px] text-zinc-400 mt-0.5">Session affinity preserved</div>
                </div>
                <div className="bg-[#13161d] border border-zinc-800/80 rounded-xl p-4">
                  <div className="text-[11px] uppercase tracking-wider text-zinc-500">Relay Balance Left</div>
                  <div className="text-xl font-semibold text-emerald-400 mt-1 font-mono">$287.40</div>
                  <div className="text-[11px] text-zinc-400 mt-0.5">Sub2API + 3 API vendors</div>
                </div>
                <div className="bg-[#13161d] border border-zinc-800/80 rounded-xl p-4">
                  <div className="text-[11px] uppercase tracking-wider text-zinc-500">Secrets Kept From Vendors</div>
                  <div className="text-xl font-semibold text-indigo-300 mt-1 font-mono">
                    {state.masking.total7d.toLocaleString()}
                  </div>
                  <div className="text-[11px] text-zinc-400 mt-0.5">Last 7 days (in-memory only)</div>
                </div>
              </div>

              {usageTab === 'allowances' && (
                <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
                  {state.accounts.map((acc) => (
                    <div
                      key={acc.id}
                      className="bg-[#13161d] border border-zinc-800/80 rounded-xl p-4 space-y-3"
                    >
                      <div className="flex items-center justify-between">
                        <div>
                          <div className="text-sm font-semibold text-zinc-100">{acc.user}</div>
                          <div className="text-xs text-zinc-400">
                            {acc.provider} · {acc.plan}
                          </div>
                        </div>
                        <button
                          onClick={() => refreshQuota(acc.id)}
                          className="p-1.5 rounded-lg bg-zinc-800/80 hover:bg-zinc-700 text-zinc-300 cursor-pointer"
                          title="Refresh this card"
                        >
                          <RefreshCw
                            className={`w-3.5 h-3.5 ${refreshingQuota === acc.id ? 'animate-spin' : ''}`}
                          />
                        </button>
                      </div>
                      {acc.windows.map((w) => (
                        <div key={w.label} className="space-y-1">
                          <div className="flex justify-between text-xs">
                            <span className="text-zinc-400">{w.label}</span>
                            <span className="font-mono text-zinc-200">
                              {100 - w.usedPercent}% left · resets {w.resetsIn}
                            </span>
                          </div>
                          <div className="h-2 rounded-full bg-zinc-800 overflow-hidden">
                            <div
                              className={`h-full rounded-full ${
                                w.usedPercent > 85 ? 'bg-amber-400' : 'bg-sky-400'
                              }`}
                              style={{ width: `${w.usedPercent}%` }}
                            />
                          </div>
                        </div>
                      ))}
                    </div>
                  ))}
                </div>
              )}

              {usageTab === 'ledger' && (
                <div className="bg-[#13161d] border border-zinc-800/80 rounded-xl overflow-hidden">
                  <table className="w-full text-left border-collapse text-xs">
                    <thead>
                      <tr className="border-b border-zinc-800 text-zinc-400 bg-[#101218]">
                        <th className="py-2.5 px-4">Agent</th>
                        <th className="py-2.5 px-4">Routed Model</th>
                        <th className="py-2.5 px-4">Tokens (In / Out / Cache)</th>
                        <th className="py-2.5 px-4">Speed &amp; TTFT</th>
                        <th className="py-2.5 px-4">Cost</th>
                        <th className="py-2.5 px-4">Status</th>
                      </tr>
                    </thead>
                    <tbody className="divide-y divide-zinc-800/60 font-mono">
                      {state.usage.map((u) => (
                        <tr key={u.route_id} className="hover:bg-zinc-800/25">
                          <td className="py-2.5 px-4 font-sans font-medium text-zinc-200">{u.agent}</td>
                          <td className="py-2.5 px-4 text-sky-300">
                            {u.provider}/{u.served}
                            {u.maskedCount ? (
                              <span className="ml-2 text-[10px] px-1.5 py-0.5 rounded bg-indigo-500/20 text-indigo-300">
                                {u.maskedCount} masked
                              </span>
                            ) : null}
                          </td>
                          <td className="py-2.5 px-4 text-zinc-300">
                            {u.in.toLocaleString()} / {u.out.toLocaleString()}{' '}
                            <span className="text-zinc-500">({u.cache_read.toLocaleString()} cached)</span>
                          </td>
                          <td className="py-2.5 px-4 text-zinc-300">
                            {u.tps > 0 ? `${u.tps.toFixed(1)} tok/s · ${u.ttft_ms}ms` : '—'}
                          </td>
                          <td className="py-2.5 px-4 text-emerald-400">${u.costUSD.toFixed(3)}</td>
                          <td className="py-2.5 px-4">
                            <span
                              className={`px-1.5 py-0.5 rounded text-[11px] ${
                                u.status === 200
                                  ? 'bg-emerald-500/15 text-emerald-300'
                                  : 'bg-amber-500/15 text-amber-300'
                              }`}
                            >
                              {u.status}
                            </span>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}

              {usageTab === 'masking' && (
                <div className="bg-[#13161d] border border-zinc-800/80 rounded-xl p-4 space-y-4">
                  <div className="flex flex-wrap gap-2">
                    {Object.entries(state.masking.byKind).map(([kind, count]) => (
                      <div
                        key={kind}
                        className="px-3 py-2 rounded-lg bg-[#0e1015] border border-zinc-800 flex items-center gap-2 text-xs"
                      >
                        <ShieldCheck className="w-3.5 h-3.5 text-indigo-400" />
                        <span className="font-mono text-zinc-300">{kind}</span>
                        <span className="font-mono font-semibold text-indigo-300">{count}</span>
                      </div>
                    ))}
                  </div>
                  <div className="space-y-2">
                    {state.masking.records.map((rec) => (
                      <div
                        key={rec.id}
                        className="flex items-center justify-between px-3 py-2 rounded-lg bg-[#0e1015] border border-zinc-800/70 text-xs font-mono"
                      >
                        <div className="flex items-center gap-3">
                          <span className="text-indigo-300">{rec.kind}</span>
                          <span className="text-zinc-500">
                            {rec.agent} → {rec.provider}
                          </span>
                          <span className="text-zinc-400">
                            {rec.previewPrefix} ({rec.length} chars)
                          </span>
                        </div>
                        <div className="flex items-center gap-3">
                          <span className="text-amber-300">{rec.placeholder}</span>
                          <span className="text-zinc-500 font-sans">{rec.timestamp}</span>
                        </div>
                      </div>
                    ))}
                  </div>
                </div>
              )}
            </div>
          )}

          {/* ==================== SESSIONS VIEW ==================== */}
          {view === 'sessions' && (
            <div className="space-y-6">
              <div>
                <h1 className="text-base font-semibold text-zinc-100">Agent Conversation Sessions</h1>
                <p className="text-xs text-zinc-400 mt-0.5">
                  Every local Claude Code, Codex, and OpenCode session across project folders, with token counts and one-click terminal resume commands.
                </p>
              </div>

              <div className="space-y-2.5">
                {state.sessions.map((s) => {
                  const resumeCmd =
                    s.agent === 'claude'
                      ? `cd ${s.cwd} && claude --resume ${s.id}`
                      : `cd ${s.cwd} && codex resume ${s.id}`;
                  return (
                    <div
                      key={s.id}
                      className="bg-[#13161d] border border-zinc-800/80 rounded-xl p-4 flex flex-col sm:flex-row sm:items-center justify-between gap-3"
                    >
                      <div className="space-y-1">
                        <div className="flex items-center gap-2">
                          <span className="text-xs font-mono px-2 py-0.5 rounded bg-zinc-800 text-sky-300">
                            {s.agent}
                          </span>
                          <span className="font-medium text-sm text-zinc-100">{s.title}</span>
                        </div>
                        <div className="flex items-center gap-3 text-xs text-zinc-400 font-mono">
                          <span>{s.cwd}</span>
                          <span>·</span>
                          <span>{s.model}</span>
                          <span>·</span>
                          <span>{s.turns} turns</span>
                          <span>·</span>
                          <span>{(s.tokens / 1000).toFixed(1)}k tokens</span>
                          <span>·</span>
                          <span>{s.updatedAt}</span>
                        </div>
                      </div>
                      <button
                        onClick={() => copyText(resumeCmd, s.id)}
                        className="text-xs px-3 py-1.5 rounded-lg bg-zinc-800 hover:bg-zinc-700 text-zinc-200 font-mono flex items-center gap-1.5 shrink-0 cursor-pointer"
                      >
                        {copiedText === s.id ? (
                          <>
                            <Check className="w-3.5 h-3.5 text-emerald-400" /> Copied resume cmd
                          </>
                        ) : (
                          <>
                            <Terminal className="w-3.5 h-3.5" /> Resume in terminal
                          </>
                        )}
                      </button>
                    </div>
                  );
                })}
              </div>
            </div>
          )}

          {/* ==================== LIBRARY VIEW ==================== */}
          {view === 'library' && (
            <div className="space-y-6">
              <div>
                <h1 className="text-base font-semibold text-zinc-100">Shared Library (Skills, MCP &amp; Instructions)</h1>
                <p className="text-xs text-zinc-400 mt-0.5">
                  Write instructions, MCP servers, and <code className="text-sky-300">SKILL.md</code> packages once; magpie syncs them into each agent&apos;s native directory.
                </p>
              </div>
              <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                {state.skills.map((sk) => (
                  <div
                    key={sk.name}
                    className="bg-[#13161d] border border-zinc-800/80 rounded-xl p-4 space-y-2"
                  >
                    <div className="flex items-center justify-between">
                      <span className="font-mono font-semibold text-sm text-sky-300">{sk.name}</span>
                      <span className="text-[11px] font-mono text-zinc-400 bg-zinc-800/80 px-2 py-0.5 rounded">
                        {sk.source.kind === 'github' ? sk.source.repo : sk.source.dir}
                      </span>
                    </div>
                    <p className="text-xs text-zinc-300">{sk.description}</p>
                    <div className="flex items-center gap-1.5 pt-1">
                      <span className="text-[11px] text-zinc-500">Synced to:</span>
                      {sk.agents.map((ag) => (
                        <span
                          key={ag}
                          className="text-[11px] font-mono px-1.5 py-0.5 rounded bg-zinc-800 text-zinc-300"
                        >
                          {ag}
                        </span>
                      ))}
                    </div>
                  </div>
                ))}
              </div>
            </div>
          )}

          {/* ==================== PLUGINS VIEW ==================== */}
          {view === 'plugins' && (
            <div className="space-y-6">
              <div>
                <h1 className="text-base font-semibold text-zinc-100">Built-in &amp; Community OpenCode Plugins</h1>
                <p className="text-xs text-zinc-400 mt-0.5">
                  Retired built-in subscriptions (Cursor, Devin, Factory, Grok, Kiro, Qoder, WorkBuddy, ZCode, Zed) run through community OpenCode auth plugins on the Bun plugin host.
                </p>
              </div>

              <div className="space-y-2.5">
                {state.plugins.map((pl) => (
                  <div
                    key={pl.id}
                    className="bg-[#13161d] border border-zinc-800/80 rounded-xl p-4 flex items-center justify-between gap-4"
                  >
                    <div className="space-y-1">
                      <div className="flex items-center gap-2 flex-wrap">
                        <span className="font-semibold text-sm text-zinc-100">{pl.name}</span>
                        <span className="text-xs font-mono text-zinc-400 bg-zinc-800 px-2 py-0.5 rounded">
                          {pl.packageName}
                        </span>
                        <span className="text-[11px] text-zinc-500 font-mono">{pl.version}</span>
                        {pl.replacesBuiltin && (
                          <span className="text-[11px] px-2 py-0.5 rounded bg-indigo-500/15 text-indigo-300 border border-indigo-500/30">
                            serves retired built-in: {pl.replacesBuiltin}
                          </span>
                        )}
                      </div>
                      <p className="text-xs text-zinc-400">{pl.description}</p>
                    </div>
                    <button
                      disabled={pl.essential}
                      onClick={() => togglePlugin(pl.id, !pl.on)}
                      className={`px-3 py-1 rounded-lg text-xs font-medium transition cursor-pointer ${
                        pl.on
                          ? 'bg-emerald-500/20 text-emerald-300 border border-emerald-500/40'
                          : 'bg-zinc-800 text-zinc-400'
                      } ${pl.essential ? 'opacity-60 cursor-not-allowed' : ''}`}
                    >
                      {pl.essential ? 'Essential' : pl.on ? 'Enabled' : 'Disabled'}
                    </button>
                  </div>
                ))}
              </div>
            </div>
          )}

          {/* ==================== SETTINGS VIEW ==================== */}
          {view === 'settings' && (
            <div className="space-y-6">
              <div>
                <h1 className="text-base font-semibold text-zinc-100">Settings &amp; Privacy Masking</h1>
                <p className="text-xs text-zinc-400 mt-0.5">
                  Configure client-side prompt secret masking, subscription discovery, and browser access.
                </p>
              </div>

              <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                <div className="bg-[#13161d] border border-zinc-800/80 rounded-xl p-4 space-y-3">
                  <div className="flex items-center justify-between">
                    <div className="flex items-center gap-2">
                      <Lock className="w-4 h-4 text-emerald-400" />
                      <span className="text-sm font-semibold text-zinc-100">Privacy Masking</span>
                    </div>
                    <button
                      onClick={() => updateSettings({ maskingEnabled: !state.settings.maskingEnabled })}
                      className={`px-3 py-1 rounded-lg text-xs font-semibold cursor-pointer ${
                        state.settings.maskingEnabled
                          ? 'bg-emerald-500 text-zinc-950'
                          : 'bg-zinc-800 text-zinc-400'
                      }`}
                    >
                      {state.settings.maskingEnabled ? 'Masking Active' : 'Off'}
                    </button>
                  </div>
                  <p className="text-xs text-zinc-400">
                    Swaps API keys, private keys, bearer tokens, emails, and public IPs with deterministic placeholders like <code className="text-sky-300">{`{{API_KEY_k3v9x2mq}}`}</code> before requests leave your machine, and restores them in streamed replies and tool arguments.
                  </p>
                </div>

                <div className="bg-[#13161d] border border-zinc-800/80 rounded-xl p-4 space-y-3">
                  <div className="flex items-center justify-between">
                    <div className="flex items-center gap-2">
                      <Cpu className="w-4 h-4 text-sky-400" />
                      <span className="text-sm font-semibold text-zinc-100">Subscription Account Discovery</span>
                    </div>
                    <button
                      onClick={() =>
                        updateSettings({ discoverSubscriptions: !state.settings.discoverSubscriptions })
                      }
                      className={`px-3 py-1 rounded-lg text-xs font-semibold cursor-pointer ${
                        state.settings.discoverSubscriptions
                          ? 'bg-sky-500 text-zinc-950'
                          : 'bg-zinc-800 text-zinc-400'
                      }`}
                    >
                      {state.settings.discoverSubscriptions ? 'Enabled' : 'Off'}
                    </button>
                  </div>
                  <p className="text-xs text-zinc-400">
                    Automatically discovers signed-in Claude Code and Codex subscriptions in <code className="text-zinc-300">~/.claude</code> and <code className="text-zinc-300">~/.codex</code> so every other agent can share their quota windows.
                  </p>
                </div>
              </div>

              {/* Live "Try it" Masking Sandbox (/api/masking/preview) */}
              <div className="bg-[#13161d] border border-zinc-800/80 rounded-xl p-4 space-y-3">
                <div className="flex items-center gap-2">
                  <EyeOff className="w-4 h-4 text-indigo-400" />
                  <h2 className="text-sm font-semibold text-zinc-100">
                    Try Masking Live (kept in no storage)
                  </h2>
                </div>
                <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                  <div>
                    <label className="block text-[11px] uppercase text-zinc-500 mb-1">
                      Sample Prompt Text
                    </label>
                    <textarea
                      rows={4}
                      value={maskTryInput}
                      onChange={(e) => setMaskTryInput(e.target.value)}
                      className="w-full bg-[#0e1015] border border-zinc-800 rounded-lg p-3 text-xs font-mono text-zinc-200 focus:outline-none focus:border-sky-500"
                    />
                  </div>
                  <div>
                    <label className="block text-[11px] uppercase text-zinc-500 mb-1">
                      What the Upstream Vendor Receives
                    </label>
                    <div className="w-full min-h-[98px] bg-[#0b0d12] border border-zinc-800 rounded-lg p-3 text-xs font-mono text-amber-300 whitespace-pre-wrap break-all">
                      {maskTryOutput?.masked || maskTryInput}
                    </div>
                  </div>
                </div>
              </div>
            </div>
          )}
        </div>
      </main>
    </div>
  );
}
