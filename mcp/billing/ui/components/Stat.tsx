import {useCallback,useEffect,useMemo,useRef,useState} from "react";

export function Stat({ label, value }: {
    label: string;
    value: string;
}) {
    return (<div className="border border-border rounded p-2">
      <div className="text-[10px] uppercase tracking-wide text-text-dim">
        {label}
      </div>
      <div className="text-sm text-text mt-0.5">{value}</div>
    </div>);
}
