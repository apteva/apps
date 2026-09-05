import { apiGet } from "./client.ts";

export type CorporateAction = {
  provider: string;
  provider_event_id: string;
  revision: number;
  action_type: string;
  status: string;
  symbol?: string;
  new_symbol?: string;
  announcement_date?: string;
  ex_date?: string;
  record_date?: string;
  payable_date?: string;
  effective_date?: string;
  process_date?: string;
  ratio_numerator?: number;
  ratio_denominator?: number;
  cash_amount?: number;
  currency?: string;
  data_quality: string;
};

export type ReferenceIssue = {
  id: number;
  provider: string;
  issue_key: string;
  severity: string;
  category: string;
  message: string;
  status: string;
  last_seen_at: string;
};

export type ExchangeSession = {
  venue: string;
  session_date: string;
  session_type: string;
  open_at?: string;
  close_at?: string;
  status: string;
  source: string;
};

export type ReferenceStatus = {
  provider: string;
  survivorship: string;
  securities?: number;
  listings?: number;
  corporate_actions?: number;
  sessions?: number;
  open_issues?: number;
  checkpoints?: Array<{ stream: string; last_ok_at?: string; last_error?: string; rows_ingested: number }>;
  universe?: { id: string; coverage_start?: string; historical_verified: boolean };
};

export const getReferenceStatus = () => apiGet<ReferenceStatus>("/reference/status");
export const getCorporateActions = (symbol?: string) => apiGet<{ corporate_actions: CorporateAction[] }>("/reference/corporate-actions", { symbol, limit: 250 }).then(r => r.corporate_actions ?? []);
export const getReferenceIssues = () => apiGet<{ issues: ReferenceIssue[] }>("/reference/quality", { status: "open", limit: 100 }).then(r => r.issues ?? []);
export const getExchangeSessions = () => {
  const now = new Date();
  const end = new Date(now.getTime() + 14 * 86400000);
  return apiGet<{ sessions: ExchangeSession[] }>("/reference/sessions", {
    venue: "US_EQUITY", start: now.toISOString().slice(0, 10), end: end.toISOString().slice(0, 10), limit: 20,
  }).then(r => r.sessions ?? []);
};
