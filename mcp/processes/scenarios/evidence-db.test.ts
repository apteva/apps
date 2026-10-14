import { expect, test } from "bun:test";
import { Database } from "bun:sqlite";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { openPostRunDatabase } from "./evidence-db";

test("post-run evidence opens WAL databases without sidecars and rejects SQL writes",()=>{
 const directory=mkdtempSync(join(tmpdir(),"processes-evidence-db-")),path=join(directory,"fixture.db");
 try{
  const writer=new Database(path);writer.exec("PRAGMA journal_mode=WAL; CREATE TABLE evidence(id TEXT); INSERT INTO evidence VALUES('source:9007199254740993')");writer.close();
  const reader=openPostRunDatabase(path);
  try{expect(reader.query("SELECT id FROM evidence").get()).toEqual({id:"source:9007199254740993"});expect(()=>reader.exec("DELETE FROM evidence")).toThrow();}finally{reader.close()}
 }finally{rmSync(directory,{recursive:true,force:true})}
});
test("post-run evidence includes uncheckpointed WAL rows",()=>{
 const directory=mkdtempSync(join(tmpdir(),"processes-evidence-wal-")),path=join(directory,"fixture.db");
 const writer=new Database(path);
 try{
  writer.exec("PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; CREATE TABLE evidence(id TEXT); INSERT INTO evidence VALUES('latest')");
  const reader=openPostRunDatabase(path);try{expect(reader.query("SELECT id FROM evidence").get()).toEqual({id:"latest"});}finally{reader.close()}
 }finally{writer.close();rmSync(directory,{recursive:true,force:true})}
});
test("post-run evidence never creates missing fixture databases",()=>expect(()=>openPostRunDatabase(join(tmpdir(),"missing-processes-fixture-"+crypto.randomUUID()))).toThrow());
