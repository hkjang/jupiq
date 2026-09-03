const syntheticKeys = new WeakMap<object, string>()
let sequence = 0

// antd deprecated the `index` argument of Table's `rowKey`, yet read-only rows
// (audit entries, cost aggregates, GPU samples) often carry no identifier at
// all. Anchoring the fallback key to the record object keeps every row unique
// and stable for as long as that object lives, so antd never reuses one row's
// cells for another and the table stops flickering while data refreshes.
export function stableRowKey(record: object, ...preferred: unknown[]): string {
  for (const value of preferred) {
    if (value !== undefined && value !== null && String(value) !== '') return String(value)
  }
  let key = syntheticKeys.get(record)
  if (!key) {
    sequence += 1
    key = `row-${sequence}`
    syntheticKeys.set(record, key)
  }
  return key
}
