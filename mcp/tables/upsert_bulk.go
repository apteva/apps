package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type bulkUpdate struct {
	position int
	columns  []string
	values   []any
}

// Complete-row snapshots use one indexed lookup per chunk, insert new rows
// with cached statements, and update only changed columns. Equal rows do not write or emit.
func (a *App) bulkUpsert(ctx *sdk.AppCtx, t *Table, table string, keys []string, raw []any) (any, error) {
	normalized := make([][]any, len(raw))
	identities := make([][]any, len(raw))
	seen := map[string]bool{}
	columnNames := make([]string, len(t.Columns))
	columnIndex := map[string]int{}
	for i, col := range t.Columns {
		columnNames[i] = quote(col.Name)
		columnIndex[col.Name] = i
	}
	var bytes int64
	for i, value := range raw {
		row := value.(map[string]any)
		if len(row) != len(t.Columns) {
			return nil, errf("bulk upsert requires complete rows")
		}
		normalized[i] = make([]any, len(t.Columns))
		for j, column := range t.Columns {
			v, present := row[column.Name]
			if !present {
				return nil, errf("bulk upsert requires column %q", column.Name)
			}
			if v == nil && !column.Nullable {
				return nil, errf("rows[%d]: column %q is required", i, column.Name)
			}
			coerced, err := coerceForTableStorage(t, column, v)
			if err != nil {
				return nil, err
			}
			if err = chargeStoredValue(ctx, &bytes, column.Name, coerced); err != nil {
				return nil, err
			}
			if err = validateStoredValueSize(ctx, column, coerced); err != nil {
				return nil, err
			}
			normalized[i][j] = coerced
		}
		identities[i] = make([]any, len(keys))
		for j, key := range keys {
			value := normalized[i][columnIndex[key]]
			// SQLite treats both signs of zero as the same numeric key.
			if number, ok := value.(float64); ok && number == 0 {
				value = float64(0)
			}
			identities[i][j] = value
		}
		encoded, err := json.Marshal(identities[i])
		if err != nil {
			return nil, err
		}
		if seen[string(encoded)] {
			return nil, errf("bulk upsert requires distinct keys")
		}
		seen[string(encoded)] = true
	}
	identityJSON, err := json.Marshal(identities)
	if err != nil {
		return nil, err
	}
	tx, err := beginWrite(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Acquire the writer before reading; comparison and writes share one snapshot.
	if _, err = tx.Exec(`UPDATE tables_meta SET updated_at=updated_at WHERE id=?`, t.ID); err != nil {
		return nil, err
	}
	if err = ensureUniqueUpsertIndex(tx, t, keys); err != nil {
		return nil, err
	}
	joins := make([]string, len(keys))
	for i, key := range keys {
		joins[i] = fmt.Sprintf("p.%s IS json_extract(j.value, '$[%d]')", quote(key), i)
	}
	from := " FROM json_each(?) j JOIN " + quote(t.PhysicalName) + " p ON " + strings.Join(joins, " AND ")
	selected := []string{"p.id", "CAST(j.key AS INTEGER)"}
	for _, name := range columnNames {
		selected = append(selected, "p."+name)
	}
	rows, err := tx.Query("SELECT "+strings.Join(selected, ",")+from, string(identityJSON))
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(raw))
	updates := []bulkUpdate{}
	var readBytes int64
	for rows.Next() {
		var id int64
		var position int
		old := make([]any, len(t.Columns))
		dest := []any{&id, &position}
		for i := range old {
			dest = append(dest, &old[i])
		}
		if err = rows.Scan(dest...); err != nil {
			rows.Close()
			return nil, err
		}
		ids[position] = id
		update := bulkUpdate{position: position}
		for i, value := range old {
			if b, ok := value.([]byte); ok {
				value = string(b)
			}
			if s, ok := value.(string); ok {
				readBytes += int64(len(s))
			}
			if n, ok := value.(int64); ok && t.Columns[i].Type == "number" {
				value = float64(n)
			}
			if !reflect.DeepEqual(value, normalized[position][i]) {
				update.columns = append(update.columns, columnNames[i]+"=?")
				update.values = append(update.values, normalized[position][i])
			}
		}
		if readBytes > maxQueryBytes(ctx) {
			rows.Close()
			return nil, errf("bulk comparison exceeds max_query_bytes; use smaller chunks")
		}
		if len(update.columns) > 0 {
			updates = append(updates, update)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	missing := []int{}
	for i, id := range ids {
		if id == 0 {
			missing = append(missing, i)
		}
	}
	if err = reserveRows(tx, t, int64(len(missing)), maxRowsPerTable(ctx)); err != nil {
		return nil, err
	}
	insertedIDs, updatedIDs := []int64{}, []int64{}
	statements := newBatchStatements(tx)
	defer statements.close()
	names := make([]string, len(t.Columns))
	for i, column := range t.Columns {
		names[i] = column.Name
	}
	// Reuse one small prepared INSERT instead of compiling a VALUES expression
	// with tens of thousands of variables. Row count capacity is reserved once.
	for _, position := range missing {
		res, err := statements.insert(t, names, normalized[position])
		if err != nil {
			return nil, err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return nil, err
		}
		ids[position] = id
		insertedIDs = append(insertedIDs, id)
	}
	for _, update := range updates {
		set := append(update.columns, updateTimestampSQL(t))
		values := append(update.values, ids[update.position])
		if _, err = statements.exec("UPDATE "+quote(t.PhysicalName)+" SET "+strings.Join(set, ",")+" WHERE id=?", values...); err != nil {
			return nil, err
		}
		updatedIDs = append(updatedIDs, ids[update.position])
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	if len(insertedIDs) > 0 {
		emit(ctx, topicRowInserted, map[string]any{"table": table, "ids": insertedIDs, "count": len(insertedIDs)})
	}
	if len(updatedIDs) > 0 {
		emit(ctx, topicRowUpdated, map[string]any{"table": table, "ids": updatedIDs, "count": len(updatedIDs)})
	}
	return map[string]any{"ids": ids, "inserted": len(insertedIDs), "updated": len(updatedIDs), "unchanged": len(raw) - len(insertedIDs) - len(updatedIDs)}, nil
}
