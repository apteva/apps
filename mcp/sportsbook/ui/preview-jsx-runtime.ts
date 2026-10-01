// Resolve the runtime source directly so its import-map alias cannot self-import.
// @ts-ignore — React exposes this CommonJS entry at runtime.
import * as Mod from './node_modules/react/jsx-runtime.js';
export const jsx=(Mod as any).jsx;
export const jsxs=(Mod as any).jsxs;
export const Fragment=(Mod as any).Fragment;
