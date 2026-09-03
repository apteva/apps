import { apiGet, apiPatch, apiPost, apiPut } from "./client.ts";
import type { PortfolioObjective, RiskPolicy, RiskState } from "./types.ts";

export const getRisk = (portfolioId: number) => apiGet<{ policy: RiskPolicy; state: RiskState }>(`/portfolios/${portfolioId}/risk`);
export const updateRisk = (portfolioId: number, policy: Partial<RiskPolicy> & { risk_level: string }) => apiPut<{ policy: RiskPolicy }>(`/portfolios/${portfolioId}/risk`, policy);
export const listObjectives = (portfolioId: number) => apiGet<{ objectives: PortfolioObjective[] }>(`/portfolios/${portfolioId}/objectives`).then((r) => r.objectives);
export const createObjective = (portfolioId: number, body: unknown) => apiPost<{ objective: PortfolioObjective }>(`/portfolios/${portfolioId}/objectives`, body);
export const archiveObjective = (portfolioId: number, objectiveId: number) => apiPatch(`/portfolios/${portfolioId}/objectives/${objectiveId}`, { status: "archived" });
