import {useCallback,useEffect,useMemo,useRef,useState} from "react";
import {type NativePanelProps} from "./shared";
import {BillingWorkspace} from "./components/BillingWorkspace";

// ─── Panel ──────────────────────────────────────────────────────────
export default function BillingPanel(props: NativePanelProps) { return <BillingWorkspace key={`${props.projectId}:${props.installId}`} {...props}/>; }
