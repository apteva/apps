import { getUniverse } from "../api/markets.ts";
import { useFetch } from "./useFetch.ts";

export function useUniverse() {
  return useFetch(getUniverse, [], 30000); // heartbeat — `tick` events drive fast updates
}
