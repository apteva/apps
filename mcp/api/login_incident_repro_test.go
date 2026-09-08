package main

import (
 "context"
 "io"
 "net"
 "net/http"
 "net/http/httptest"
 "sync/atomic"
 "testing"
 "time"
)

// Isolates the two SetReadDeadline lines added to handleGateway in v0.6.0.
// Use a short deadline to reproduce the same long-lived request behavior.
func TestIncidentReadDeadlineKeepAlive(t *testing.T) {
 var connections atomic.Int32
 var poisoned atomic.Bool
 s:=httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  if r.URL.Path=="/stream" {
   c:=http.NewResponseController(w)
   _=c.SetReadDeadline(time.Now().Add(30*time.Millisecond))
   defer c.SetReadDeadline(time.Time{})
   select{case <-r.Context().Done(): case <-time.After(time.Second):t.Error("read deadline did not cancel stream")}
  } else {poisoned.Store(r.Context().Err()!=nil)}
  w.Header().Set("Content-Length","2")
  _,_=io.WriteString(w,"OK")
 }))
 s.Config.ConnState=func(_ net.Conn,state http.ConnState){if state==http.StateNew{connections.Add(1)}}
 s.Start();defer s.Close()
 client:=s.Client()
 for _,path:=range []string{"/stream","/login"}{
  r,err:=client.Get(s.URL+path);if err!=nil{t.Fatal(err)};_,_=io.Copy(io.Discard,r.Body);r.Body.Close()
 }
 t.Logf("connections=%d next request canceled=%v",connections.Load(),poisoned.Load())
 if connections.Load()!=1||!poisoned.Load(){t.Fatal("poisoned keep-alive mechanism not reproduced")}
}

func TestIncidentAuthBackendCancellation(t *testing.T){
 t.Setenv("APTEVA_GATEWAY_URL","http://127.0.0.1:7300")
 a:=&App{httpClient:gatewayHTTPClient()}
 ctx,cancel:=context.WithCancel(context.Background());cancel()
 r:=httptest.NewRequest("GET","http://gateway/gw/flexylead/v1/session/bootstrap",nil).WithContext(ctx)
 r.Header.Set("Authorization","Bearer test-token")
 _,err:=a.verifyAuthJWT(r,"test-project")
 if err==nil{t.Fatal("expected cancellation")}
 t.Log(err)
}

func TestIncidentClearingBodyDeadlineBeforeStream(t *testing.T){
 var poisoned atomic.Bool
 s:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  c:=http.NewResponseController(w)
  _=c.SetReadDeadline(time.Now().Add(30*time.Millisecond))
  // No request body remains: clear before Auth/upstream/streaming work.
  _=c.SetReadDeadline(time.Time{})
  if r.URL.Path=="/stream" {time.Sleep(60*time.Millisecond)}
  poisoned.Store(poisoned.Load()||r.Context().Err()!=nil)
  w.Header().Set("Content-Length","2");_,_=io.WriteString(w,"OK")
 }));defer s.Close()
 for _,path:=range []string{"/stream","/login"}{
  r,err:=s.Client().Get(s.URL+path);if err!=nil{t.Fatal(err)};_,_=io.Copy(io.Discard,r.Body);r.Body.Close()
 }
 if poisoned.Load(){t.Fatal("cleared deadline still poisoned request")}
}
