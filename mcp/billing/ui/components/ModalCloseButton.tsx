import {useCallback,useEffect,useMemo,useRef,useState} from "react";

export function ModalCloseButton({ onClose }: {
    onClose: () => void;
}) {
    return (<button type="button" onClick={onClose} aria-label="Close" className="text-text-muted hover:text-text">
      <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round">
        <path d="M4 4 L12 12"/>
        <path d="M12 4 L4 12"/>
      </svg>
    </button>);
}
