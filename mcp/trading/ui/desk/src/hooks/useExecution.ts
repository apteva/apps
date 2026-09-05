import { getExecutionCosts, getVenueProfiles } from "../api/execution.ts";
import { useFetch } from "./useFetch.ts";

export function useExecution(portfolioId: number | null) {
  return {
    profiles: useFetch(getVenueProfiles, [], 30000),
    costs: useFetch(() => portfolioId == null ? Promise.resolve({costs: [], totals: {}}) : getExecutionCosts(portfolioId), [portfolioId], 30000),
  };
}
