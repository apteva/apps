import { listPortfolios, getPortfolio, listPositions, listOrders, readJournal } from "../api/portfolios.ts";
import { useFetch } from "./useFetch.ts";

export function usePortfolios() {
  return useFetch(listPortfolios, [], 5000);
}

export function usePortfolio(id: number | null) {
  return useFetch(
    async () => (id == null ? null : await getPortfolio(id)),
    [id],
    30000,  // heartbeat — fast updates come from useAppEvents
  );
}

export function usePositions(id: number | null) {
  return useFetch(
    async () => (id == null ? [] : await listPositions(id)),
    [id],
    30000,  // heartbeat — fast updates come from useAppEvents
  );
}

export function useOrders(id: number | null) {
  return useFetch(
    async () => (id == null ? [] : await listOrders(id, "all", 50)),
    [id],
    30000,  // heartbeat — fast updates come from useAppEvents
  );
}

export function useJournal(id: number | null) {
  return useFetch(
    async () => (id == null ? [] : await readJournal(id, { limit: 30 })),
    [id],
    30000,  // heartbeat — fast updates come from useAppEvents
  );
}
