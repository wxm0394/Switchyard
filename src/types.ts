export type ViewId =
  | 'agents'
  | 'sessions'
  | 'providers'
  | 'routing'
  | 'gateway'
  | 'usage'
  | 'library'
  | 'plugins'
  | 'settings';

export interface AgentItem {
  id: string;
  name: string;
  category: 'CLI' | 'IDE' | 'Desktop';
  version: string;
  installed: boolean;
  connected: boolean;
  model: string;
  effort: 'low' | 'medium' | 'high' | 'xhigh';
  configPath: string;
  protocol: string;
  shortlist: string[];
  activeSessions: number;
}

export interface ProviderKeyItem {
  name: string;
  maskedKey: string;
  off?: boolean;
  requestsToday: number;
}

export interface SubscriptionWindow {
  label: string;
  usedPercent: number;
  resetsIn: string;
  resetAt: string;
}

export interface SubscriptionAccount {
  id: string;
  provider: string;
  user: string;
  plan: string;
  active: boolean;
  credits?: string | null;
  windows: SubscriptionWindow[];
}

export interface ProviderItem {
  id: string;
  name: string;
  kind: 'api' | 'subscription' | 'relay' | 'local';
  protocol: string;
  baseUrl: string;
  routing: 'order' | 'rotate' | 'smart' | 'usage' | 'pace';
  models: string[];
  keys: ProviderKeyItem[];
  balanceUSD?: number;
  windows?: { window: string; limit: number; used: number; remaining: number; resetsIn: string }[];
  move?: {
    state: 'plugin' | 'unmoved';
    pluginPkg: string;
  };
}

export interface RoutingRule {
  use: string;
  agents?: string[];
  intent?: string;
}

export interface RoutingGroup {
  id: string;
  name: string;
  routing: 'smart' | 'order' | 'rotate' | 'usage' | 'pace';
  members: string[];
  rules: RoutingRule[];
}

export interface UsageCallRecord {
  route_id: number;
  t: string;
  agent: string;
  provider: string;
  model: string;
  req: string;
  served: string;
  in: number;
  out: number;
  cache_read: number;
  reasoning?: number;
  effort?: string;
  ms: number;
  ttft_ms: number;
  tps: number;
  costUSD: number;
  status: number;
  ep: string;
  session: string;
  providerKeyName?: string;
  providerAccount?: string;
  maskedCount?: number;
  err?: string;
}

export interface AgentSessionItem {
  id: string;
  agent: 'claude' | 'codex' | 'opencode' | 'gemini';
  title: string;
  cwd: string;
  model: string;
  turns: number;
  tokens: number;
  costUSD: number;
  updatedAt: string;
}

export interface LibrarySkillItem {
  name: string;
  description: string;
  agents: string[];
  source: {
    kind: 'github' | 'folder';
    repo?: string;
    dir?: string;
  };
}

export interface PluginItem {
  id: string;
  name: string;
  packageName: string;
  kind: 'builtin' | 'auth' | 'middleware';
  version: string;
  description: string;
  on: boolean;
  essential?: boolean;
  replacesBuiltin?: string;
}

export interface MaskingRecord {
  id: string;
  timestamp: string;
  agent: string;
  provider: string;
  kind: 'API_KEY' | 'EMAIL' | 'PRIVATE_KEY' | 'BEARER_TOKEN' | 'IPV4';
  action: 'Mask' | 'Warn' | 'Block';
  placeholder: string;
  previewPrefix: string;
  length: number;
}

export interface MagpieSettings {
  maskingEnabled: boolean;
  maskSecrets: boolean;
  maskPersonal: boolean;
  localModelFinder: boolean;
  neverMask: string[];
  redactRouteModel: string;
  lanShare: boolean;
  browserAccess: 'off' | 'local' | 'lan';
  discoverSubscriptions: boolean;
  keepAwake: boolean;
  activeProfile: string;
  gatewayAddr: string;
}

export interface MagpieState {
  version: string;
  gatewayUrl: string;
  profiles: { id: string; name: string; description: string; agentModels: Record<string, string> }[];
  agents: AgentItem[];
  providers: ProviderItem[];
  accounts: SubscriptionAccount[];
  groups: RoutingGroup[];
  usage: UsageCallRecord[];
  sessions: AgentSessionItem[];
  skills: LibrarySkillItem[];
  plugins: PluginItem[];
  masking: {
    total7d: number;
    byKind: Record<string, number>;
    records: MaskingRecord[];
  };
  settings: MagpieSettings;
}
