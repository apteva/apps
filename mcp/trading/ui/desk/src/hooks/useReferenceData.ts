import { useFetch } from "./useFetch.ts";
import { getCorporateActions, getExchangeSessions, getReferenceIssues, getReferenceStatus } from "../api/reference.ts";

export function useReferenceData(symbol: string) {
  return {
    status: useFetch(getReferenceStatus, [], 30000),
    actions: useFetch(() => getCorporateActions(symbol || undefined), [symbol], 30000),
    issues: useFetch(getReferenceIssues, [], 30000),
    sessions: useFetch(getExchangeSessions, [], 60000),
  };
}
