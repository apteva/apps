import { parse, stringify, LosslessNumber, isSafeNumber } from "lossless-json";

export type ColumnType =
  | "text"
  | "number"
  | "bool"
  | "datetime"
  | "json"
  | "file_id";
export interface ColumnDef {
  name: string;
  type: ColumnType;
  nullable: boolean;
  default?: unknown;
}
export function parseJSON(text: string): unknown {
  return parse(text, undefined, (value: string) =>
    isSafeNumber(value) ? Number(value) : new LosslessNumber(value),
  );
}
export function stringifyJSON(value: unknown, space?: number): string {
  return stringify(value, undefined, space) ?? "null";
}
export function parseInputValue(column: ColumnDef, raw: string): unknown {
  switch (column.type) {
    case "text":
      return raw;
    case "datetime":
      if (
        !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(
          raw,
        ) ||
        !Number.isFinite(Date.parse(raw))
      )
        throw new Error(
          `${column.name}: enter an RFC3339 datetime with timezone`,
        );
      return raw;
    case "bool":
      if (raw !== "true" && raw !== "false")
        throw new Error(`${column.name}: choose true or false`);
      return raw === "true";
    case "number": {
      if (!raw.trim()) throw new Error(`${column.name}: enter a number`);
      const n = Number(raw);
      if (!Number.isFinite(n))
        throw new Error(`${column.name}: enter a finite number`);
      return n;
    }
    case "file_id": {
      if (!/^[1-9]\d*$/.test(raw) || BigInt(raw) > 9223372036854775807n)
        throw new Error(`${column.name}: enter a positive integer file ID`);
      return raw; // Decimal strings preserve integer precision in browsers.
    }
    case "json":
      return parseJSON(raw); // Invalid JSON remains a validation error.
  }
}

export type FieldMode = "value" | "null" | "default";
export interface FieldValue {
  mode: FieldMode;
  text: string;
}
export function initialField(
  column: ColumnDef,
  value: unknown,
  insert = false,
): FieldValue {
  if (insert) return { mode: "default", text: "" };
  if (value === null || value === undefined) return { mode: "null", text: "" };
  return {
    mode: "value",
    text: column.type === "json" ? stringifyJSON(value) : String(value),
  };
}
export function fieldValue(column: ColumnDef, field: FieldValue): unknown {
  if (field.mode === "null") {
    if (!column.nullable) throw new Error(`${column.name} cannot be null`);
    return null;
  }
  if (field.mode === "default") {
    if (!column.nullable && column.default === undefined)
      throw new Error(`${column.name} is required`);
    return undefined;
  }
  return parseInputValue(column, field.text);
}
