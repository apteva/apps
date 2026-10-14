import { Database } from "bun:sqlite";

// Use only after the isolated runner has exited. Bun's read-only SQLite open
// cannot recreate missing WAL sidecars on macOS after a clean SDK shutdown.
// Allow journal bookkeeping while forbidding SQL data writes. Unlike immutable
// mode this also preserves any uncheckpointed WAL records in the evidence.
export function openPostRunDatabase(path: string): Database {
  const db = new Database(path, { readwrite: true, create: false });
  try {
    db.exec("PRAGMA query_only=ON");
    return db;
  } catch (error) {
    db.close();
    throw error;
  }
}
