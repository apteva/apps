package engine

import (
	"context"
	"testing"

	native "github.com/apteva/db"
)

func TestAptevaSyncProfilesWriteAndReopen(t *testing.T) {
	for _, profile := range []struct {
		name string
		mode native.SyncMode
	}{{"full", native.SyncFull}, {"barrier", native.SyncBarrier}, {"os", native.SyncOS}} {
		t.Run(profile.name, func(t *testing.T) {
			root := t.TempDir()
			options := DefaultAptevaOptions()
			options.SyncMode = profile.name
			n := nativeAptevaOptions(options)
			if !n.Sync || n.SyncMode != profile.mode || n.NoVerifyChecksums {
				t.Fatal("flush mode must preserve synchronous writes and checksums", n)
			}
			m, e := OpenWithConfig(root, ManagerOptions{Apteva: &options})
			checkErr(t, e)
			call(t, m, "p", "database_create", Request{Database: "native", Adapter: "aptevadb"})
			call(t, m, "p", "collection_create", Request{Database: "native", Collection: "items", Fields: []Field{{"i", "integer", false}}})
			call(t, m, "p", "insert", Request{Database: "native", Collection: "items", Records: []Record{{"id": "a", "i": "9007199254740993"}}})
			plan := call(t, m, "p", "explain", Request{Database: "native", Collection: "items"}).(map[string]any)
			if plan["syncMode"] != profile.name || plan["synchronousWrites"] != true {
				t.Fatal(plan)
			}
			checkErr(t, m.Close())
			// A profile is a process setting, not an on-disk schema/codec choice.
			options.SyncMode = "full"
			m, e = OpenWithConfig(root, ManagerOptions{Apteva: &options})
			checkErr(t, e)
			t.Cleanup(func() { checkErr(t, m.Close()) })
			got := call(t, m, "p", "get", Request{Database: "native", Collection: "items", Key: Record{"id": "a"}}).(map[string]any)
			if got["found"] != true || got["record"].(Record)["i"] != "9007199254740993" {
				t.Fatal(got)
			}
			call(t, m, "p", "update", Request{Database: "native", Collection: "items", Key: Record{"id": "a"}, Increment: Record{"i": "1"}})
			v, e := m.Execute(context.Background(), "p", "aggregate", Request{Database: "native", Collection: "items", Metrics: []Metric{{"sum", "sum", "i"}}})
			checkErr(t, e)
			if v.(map[string]any)["rows"].([]Record)[0]["sum"] != "9007199254740994" {
				t.Fatal(v)
			}
		})
	}
	if m, e := OpenWithConfig(t.TempDir(), ManagerOptions{Apteva: &AptevaOptions{SyncMode: "async"}}); e == nil {
		m.Close()
		t.Fatal("invalid async profile accepted")
	}
	o, e := (AptevaOptions{}).normalized()
	checkErr(t, e)
	n := nativeAptevaOptions(o)
	if o.SyncMode != "full" || !n.Sync || n.SyncMode != native.SyncFull {
		t.Fatal("zero config must preserve full-flush default", o, n)
	}
}
