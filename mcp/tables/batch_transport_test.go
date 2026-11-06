package main

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func packedBatch(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	w, _ := gzip.NewWriterLevel(&buffer, gzip.BestSpeed)
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buffer.Bytes())
}
func TestCompressedBatchKeepsLargePayloadAndExactNumbers(t *testing.T) {
	ops := []any{map[string]any{"id": "write", "operation": "rows_upsert", "args": map[string]any{"table": "t", "rows": []any{map[string]any{"id": json.Number("9007199254740993"), "fraction": json.Number("0.875"), "unicode": "é", "raw": strings.Repeat("x", 9<<20)}}}}}
	decoded, err := decodeBatchOperations(map[string]any{"operations_gzip_base64": packedBatch(t, ops)})
	if err != nil || !reflect.DeepEqual(decoded, ops) {
		t.Fatalf("compressed operations changed data: %v", err)
	}
}
func TestCompressedBatchRejectsMalformedAmbiguousAndOversizedInput(t *testing.T) {
	invalid := []map[string]any{{"operations_gzip_base64": "?"}, {"operations_gzip_base64": base64.StdEncoding.EncodeToString([]byte("no gzip"))}, {"operations_gzip_base64": packedBatch(t, []any{})}, {"operations_gzip_base64": packedBatch(t, map[string]any{"id": "x"})}, {"operations": []any{1}, "operations_gzip_base64": packedBatch(t, []any{1})}, {"operations_gzip_base64": packedBatch(t, []any{strings.Repeat("x", maxDecodedBatchBytes)})}}
	for i, args := range invalid {
		if _, err := decodeBatchOperations(args); err == nil {
			t.Fatalf("invalid case %d accepted", i)
		}
	}
}
func TestCompressedBatchUsesSameWritesEventsAndRollback(t *testing.T) {
	c, rec := newTestCtxWithRecorder(t)
	a := &App{}
	snapshotTable(t, a, c, "compressed")
	operation := func(rows []any) map[string]any {
		return map[string]any{"id": "save", "operation": "rows_upsert", "args": map[string]any{"table": "compressed", "key": []any{"key"}, "bulk": true, "rows": rows}}
	}
	out := mustCall(t, a, c, "tables_batch", map[string]any{"mode": "write_transaction", "coalesce_events": true, "operations_gzip_base64": packedBatch(t, []any{operation([]any{snapshotRow("keep", 1.5)})})})
	if out["results"].(map[string]any)["save"].(map[string]any)["status"] != "ok" {
		t.Fatal(out)
	}
	before := len(rec.EventsByTopic(topicRowUpdated))
	row := snapshotRow("keep", 2.5)
	ops := []any{operation([]any{row}), map[string]any{"id": "fail", "operation": "rows_upsert", "args": map[string]any{"table": "compressed", "key": []any{"key"}, "rows": []any{map[string]any{"key": "bad", "unknown": true}}}}}
	failed := mustCall(t, a, c, "tables_batch", map[string]any{"mode": "write_transaction", "operations_gzip_base64": packedBatch(t, ops)})
	if failed["results"].(map[string]any)["save"].(map[string]any)["status"] != "rolled_back" {
		t.Fatal(failed)
	}
	rows := mustCall(t, a, c, "rows_search", map[string]any{"table": "compressed"})["rows"].([]map[string]any)
	if len(rows) != 1 || rows[0]["metric"] != float64(1.5) || len(rec.EventsByTopic(topicRowUpdated)) != before {
		t.Fatal("compressed rollback changed rows or events")
	}
}
