import { afterEach, expect, test } from "bun:test";
import React, { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { CreatePortfolioForm } from "../../TradingPanel.tsx";

let root: Root;
let container: HTMLElement;
afterEach(async()=>{ if(root) await act(async()=>root.unmount());container?.remove(); });
function mount(){container=document.createElement("div");document.body.append(container);root=createRoot(container);}

test("broker paper excludes live-only crypto providers", async()=>{
 mount();
 const brokers=[{slug:"binance-trading",bound:true,paper_supported:false,asset_classes:["crypto"]},{slug:"alpaca-trading",bound:true,paper_supported:true,asset_classes:["equity","etf","crypto"]}];
 const api:any=async()=>({brokers});
 await act(async()=>root.render(<CreatePortfolioForm api={api} onCreated={()=>{}} onCancel={()=>{}} setError={()=>{}}/>));
 const radios=container.querySelectorAll<HTMLInputElement>('input[type="radio"]');
 await act(async()=>radios[2]!.click());
 expect(container.querySelector("select")!.textContent).toContain("binance-trading");
 await act(async()=>radios[1]!.click());
 expect(container.querySelector("select")!.textContent).toContain("alpaca-trading");
 expect(container.querySelector("select")!.textContent).not.toContain("binance-trading");
});


