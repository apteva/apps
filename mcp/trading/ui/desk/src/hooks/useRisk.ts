import { getRisk, getUniversePolicy, listObjectives } from "../api/risk.ts";
import { useFetch } from "./useFetch.ts";

export function useRisk(portfolioId: number | null) {
  const risk = useFetch(() => portfolioId == null ? Promise.resolve(null) : getRisk(portfolioId), [portfolioId], 30000);
  const objectives = useFetch(() => portfolioId == null ? Promise.resolve([]) : listObjectives(portfolioId), [portfolioId], 30000);
  const universe = useFetch(() => portfolioId == null ? Promise.resolve(null) : getUniversePolicy(portfolioId), [portfolioId], 30000);
  return { risk, objectives, universe };
}
