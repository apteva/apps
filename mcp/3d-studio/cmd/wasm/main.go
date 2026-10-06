//go:build js && wasm

package main

import (
 "encoding/json"
 "syscall/js"
 "github.com/apteva/apps/mcp/3d-studio/engine"
)
func main() {
 evaluate:=js.FuncOf(func(_ js.Value,args []js.Value) any {
  reply:=map[string]any{}; if len(args)!=1 { return `{"error":"JSON request required"}` }; var request engine.EditRequest
  if err:=json.Unmarshal([]byte(args[0].String()),&request); err!=nil { reply["error"]=err.Error() } else if result,err:=engine.Evaluate(request); err!=nil { reply["error"]=err.Error() } else { reply["result"]=result; mesh,err:=engine.Triangles(result.Document); if err!=nil { reply["error"]=err.Error() } else { reply["render_mesh"]=mesh } }
  raw,err:=json.Marshal(reply); if err!=nil { return `{"error":"could not encode result"}` }; return string(raw)
 }); js.Global().Set("studioEvaluate",evaluate); select{}
}
