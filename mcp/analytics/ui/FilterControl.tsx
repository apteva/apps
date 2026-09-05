export const encodeFilter = (value: unknown) =>
  `@json:${JSON.stringify({ value })}`;
export function requestFilters(
  values: Record<string, string>,
): Record<string, unknown> {
  return Object.fromEntries(
    Object.entries(values).map(([key, value]) => {
      if (value.startsWith("@json:")) {
        try {
          return [key, JSON.parse(value.slice(6))];
        } catch {
          /* old plain value */
        }
      }
      return [key, value];
    }),
  );
}
export function FilterControl({
  type,
  value,
  options,
  onChange,
  label,
}: {
  type?: string;
  value: string;
  options: Array<{ value: unknown; label: unknown }>;
  onChange: (value: string) => void;
  label?: string;
}) {
  const multiple = type === "multi_select",
    typed = type !== "date_window";
  const optionValue = (v: unknown) => (typed ? encodeFilter(v) : String(v));
  let selection: string | string[] = value;
  if (multiple) {
    let raw: unknown = value === "all" ? [] : [value];
    if (value.startsWith("@json:")) {
      try {
        raw = JSON.parse(value.slice(6)).value;
      } catch {
        raw = [];
      }
    }
    selection = (Array.isArray(raw) ? raw : [raw]).map(encodeFilter);
  } else if (typed && value !== "all" && !value.startsWith("@json:"))
    selection = encodeFilter(value);
  return (
    <select
      aria-label={label}
      multiple={multiple}
      value={selection}
      onChange={(e) =>
        onChange(
          multiple
            ? encodeFilter(
                Array.from(e.target.selectedOptions).map(
                  (o) => JSON.parse(o.value.slice(6)).value,
                ),
              )
            : e.target.value,
        )
      }
      className="bg-bg-input border border-border rounded px-2 py-1 text-xs"
    >
      {typed && !multiple && <option value="all">All</option>}
      {options.map((o) => (
        <option key={optionValue(o.value)} value={optionValue(o.value)}>
          {String(o.label ?? o.value ?? "null")}
        </option>
      ))}
    </select>
  );
}
