package main

// A small JSON scalar reader for remote hosts that already require POSIX awk.
// Unlike regex extraction, this follows object paths, decodes JSON strings and
// rejects malformed input. No Python/jq installation is required on the host.
const remoteJSONReader = `json_get() {
  LC_ALL=C awk -v wanted="$1" '
  function fail() { bad=1; exit 1 }
  function ws() { while (substr(s,p,1) ~ /[ \t\r\n]/ && p<=length(s)) p++ }
  function hex4(    n,i,c,v) {
    n=0
    for(i=0;i<4;i++) {
      c=tolower(substr(s,p++,1)); v=index("0123456789abcdef",c)-1
      if(length(c)!=1 || v<0) fail()
      n=n*16+v
    }
    return n
  }
  function utf8(n) {
    if(n<128) return sprintf("%c",n)
    if(n<2048) return sprintf("%c%c",192+int(n/64),128+n%64)
    if(n<65536) return sprintf("%c%c%c",224+int(n/4096),128+int(n/64)%64,128+n%64)
    return sprintf("%c%c%c%c",240+int(n/262144),128+int(n/4096)%64,128+int(n/64)%64,128+n%64)
  }
  function str(    out,c,n,lo) {
    if(substr(s,p++,1)!="\"") fail()
    out=""
    while(p<=length(s)) {
      c=substr(s,p++,1)
      if(c=="\"") return out
      if(c ~ /[[:cntrl:]]/) fail()
      if(c=="\\") {
        c=substr(s,p++,1)
        if(c=="u") {
          n=hex4()
          if(n>=55296 && n<=56319) {
            if(substr(s,p,2)!="\\u") fail()
            p+=2; lo=hex4()
            if(lo<56320 || lo>57343) fail()
            n=65536+(n-55296)*1024+lo-56320
          } else if(n>=56320 && n<=57343) fail()
          c=utf8(n)
        } else if(c=="n") c="\n"
        else if(c=="r") c="\r"
        else if(c=="t") c="\t"
        else if(c=="b") c=sprintf("%c",8)
        else if(c=="f") c=sprintf("%c",12)
        else if(c!="\"" && c!="\\" && c!="/") fail()
      }
      out=out c
    }
    fail()
  }
  function value(path,depth,    c,key,v,start,i) {
    if(depth>64) fail()
    ws(); c=substr(s,p,1)
    if(c=="{") {
      p++; ws()
      if(substr(s,p,1)=="}") {p++; return}
      while(1) {
        ws(); key=str(); ws()
        if(substr(s,p++,1)!=":") fail()
        value(path=="" ? key : path "." key,depth+1); ws(); c=substr(s,p++,1)
        if(c=="}") return
        if(c!=",") fail()
      }
    } else if(c=="[") {
      p++; ws(); i=0
      if(substr(s,p,1)=="]") {p++; return}
      while(1) {
        value(path "." i++,depth+1); ws(); c=substr(s,p++,1)
        if(c=="]") return
        if(c!=",") fail()
      }
    } else if(c=="\"") v=str()
    else {
      start=p
      while(p<=length(s) && substr(s,p,1) !~ /[ \t\r\n,}\]]/) p++
      v=substr(s,start,p-start)
      if(v!="true" && v!="false" && v!="null" && v !~ /^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$/) fail()
    }
    if(path==wanted) { answer=v; found=1 }
  }
  {s=s $0 "\n"}
  END {if(bad) exit 1; p=1; value("",0); ws(); if(p<=length(s)) exit 1; if(found) printf "%s",answer}
  '
}
json_file_id() {
  JSON_RESPONSE=$(cat)
  ID=$(printf '%s' "$JSON_RESPONSE" | json_get file.id) || return 1
  if [ -z "$ID" ]; then ID=$(printf '%s' "$JSON_RESPONSE" | json_get id) || return 1; fi
  case "$ID" in ''|*[!0-9]*) return 1;; esac
  printf '%s' "$ID"
}
`
