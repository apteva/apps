import { Window } from "happy-dom";
const win=new Window();
Object.assign(globalThis,{window:win,document:win.document,navigator:win.navigator,HTMLElement:win.HTMLElement});
