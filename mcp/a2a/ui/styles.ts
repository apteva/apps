// Scoped layout, using the same host typography, geometry, and color tokens as Tasks and CRM.
export const styles = `
.a2a {
  --a-bg: var(--bg);
  --a-card: var(--bg-card);
  --a-text: var(--text);
  --a-muted: var(--text-muted);
  --a-border: var(--border);
  --a-accent: var(--accent);
  color: var(--a-text);
  background: var(--a-bg);
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  min-width: 0;
  overflow: hidden;
  font-family: var(--font-base, inherit);
  font-size: 12px;
  line-height: 1.5;
  color-scheme: dark;
}
[data-mode="light"] .a2a {
  color-scheme: light;
}
.a2a * {
  box-sizing: border-box;
}
.a2a button,
.a2a input,
.a2a select,
.a2a textarea {
  font: inherit;
  color: inherit;
}
.a2a button,
.a2a a {
  touch-action: manipulation;
}
.a2a button {
  cursor: pointer;
}
.a2a button:disabled {
  opacity: 0.5;
  cursor: wait;
}
.a2a :focus-visible {
  outline: 2px solid var(--a-accent);
  outline-offset: 3px;
}
.a2a h1,
.a2a h2,
.a2a h3,
.a2a p {
  margin: 0;
}
.a2a h1 {
  font-size: 18px;
  font-weight: 700;
  letter-spacing: normal;
  line-height: 1.5;
}
.a2a h2 {
  font-size: 12px;
  font-weight: 700;
  letter-spacing: normal;
}
.a2a h3 {
  font-size: 12px;
  font-weight: 600;
}
.a2a a {
  color: var(--a-accent);
  text-decoration: none;
}
.a2a a:hover {
  text-decoration: underline;
}
.a2a svg {
  flex-shrink: 0;
}
.a2a small {
  font-size: 10px;
}
.a2a pre {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  font: 11px/1.65 var(--font-mono-fixed, monospace);
  background: var(--a-bg);
  padding: 14px;
  border-radius: var(--radius-sm, 2px);
  max-height: 360px;
  overflow: auto;
}
.a2a summary {
  cursor: pointer;
}
.a2a .muted {
  color: var(--a-muted);
}
.a2a .eyebrow {
  font-size: 10px;
  letter-spacing: 0.5px;
  text-transform: uppercase;
  font-weight: 650;
  color: var(--a-muted);
}
.a2a .row {
  display: flex;
  align-items: center;
  gap: 10px;
}
.a2a .wrap {
  flex-wrap: wrap;
}
.a2a .between {
  justify-content: space-between;
}
.a2a .grow {
  flex: 1;
  min-width: 0;
}
.a2a .stack {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.a2a .gap-sm {
  gap: 8px;
}
.a2a .truncate {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.a2a .break {
  overflow-wrap: anywhere;
}
.a2a .header {
  padding: 16px 24px 0;
  flex-shrink: 0;
  border-bottom: 1px solid var(--a-border);
  background: var(--a-bg);
}
.a2a .title-line {
  margin: 0 0 12px;
  flex-wrap: wrap;
  gap: 12px;
}
.a2a .subtitle {
  margin-top: 4px;
  color: var(--text-dim, var(--a-muted));
  font-size: 12px;
}
.a2a .tabs {
  display: flex;
  gap: 4px;
  overflow-x: auto;
}
.a2a .tab {
  display: flex;
  align-items: center;
  gap: 6px;
  border: 0;
  border-bottom: 2px solid transparent;
  padding: 6px 12px;
  background: none;
  color: var(--a-muted);
  white-space: nowrap;
  font-size: 12px;
}
.a2a .tab[aria-selected="true"] {
  border-bottom-color: var(--a-accent);
  color: var(--a-accent);
}
.a2a .main {
  padding: 16px 24px;
  flex: 1;
  min-height: 0;
  overflow: auto;
  width: 100%;
  max-width: none;
  margin: 0;
}
.a2a .btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 7px;
  padding: 6px 10px;
  border: 1px solid var(--a-border);
  border-radius: var(--radius-sm, 2px);
  background: var(--a-card);
  font-size: 12px;
  white-space: nowrap;
  text-decoration: none;
}
.a2a .btn:hover {
  border-color: var(--a-muted);
  text-decoration: none;
}
.a2a .primary:hover {
  background: color-mix(in srgb, var(--a-accent) 20%, transparent);
}
.a2a .primary {
  background: color-mix(in srgb, var(--a-accent) 10%, transparent);
  border-color: var(--a-accent);
  color: var(--a-accent);
  font-weight: 700;
}
.a2a .quiet {
  background: transparent;
}
.a2a .danger {
  color: var(--error);
}
.a2a .icon-btn {
  border: 0;
  background: none;
  padding: 6px;
  display: inline-flex;
  align-items: center;
  border-radius: var(--radius-sm, 2px);
  color: var(--a-muted);
}
.a2a .panel {
  box-shadow: var(--shadow-card, none);
  border: 1px solid var(--a-border);
  border-radius: var(--radius-md, 4px);
  background: var(--a-card);
  overflow: hidden;
}
.a2a .pad {
  padding: 12px 16px;
}
.a2a .section-head {
  padding: 10px 12px;
  border-bottom: 1px solid var(--a-border);
}
.a2a .metrics {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 12px;
}
.a2a .metric {
  padding: 12px 14px;
  text-align: left;
  color: inherit;
  display: block;
}
.a2a .metric:hover {
  border-color: var(--a-accent);
}
.a2a .metric-value {
  font-size: 24px;
  font-weight: 700;
  line-height: 1.25;
  letter-spacing: normal;
  margin: 6px 0 2px;
}
.a2a .metric .row {
  justify-content: space-between;
  color: var(--a-muted);
  font-size: 12px;
}
.a2a .metric small {
  color: var(--a-muted);
}
.a2a .overview-grid {
  display: grid;
  grid-template-columns: minmax(0, 1.7fr) minmax(270px, 1fr);
  gap: 16px;
  align-items: start;
}
.a2a .list-row {
  width: 100%;
  padding: 10px 12px;
  background: none;
  border: 0;
  border-bottom: 1px solid var(--a-border);
  text-align: left;
  color: inherit;
  display: block;
}
.a2a .list-row:last-child {
  border-bottom: 0;
}
.a2a .list-row:hover,
.a2a .list-row.selected {
  background: var(--bg-hover, var(--a-bg));
}
.a2a .list-row.selected {
  box-shadow: inset 3px 0 var(--a-accent);
}
.a2a .list-row p {
  margin: 4px 0;
  color: var(--a-muted);
  font-size: 12px;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  overflow-wrap: anywhere;
}
.a2a .list-row small {
  font-size: 11px;
  color: var(--a-muted);
}
.a2a .badge {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-size: 10px;
  line-height: 1.5;
  border: 1px solid var(--a-border);
  border-radius: var(--radius-sm, 2px);
  padding: 3px 7px;
  white-space: nowrap;
  color: var(--a-muted);
}
.a2a .badge.good {
  color: var(--success);
  border-color: color-mix(in srgb, var(--success) 30%, transparent);
  background: color-mix(in srgb, var(--success) 7%, transparent);
}
.a2a .badge.warn {
  color: var(--warn);
  border-color: color-mix(in srgb, var(--warn) 30%, transparent);
  background: color-mix(in srgb, var(--warn) 7%, transparent);
}
.a2a .badge.bad {
  color: var(--error);
  border-color: color-mix(in srgb, var(--error) 30%, transparent);
}
.a2a .badge.active {
  color: var(--info);
}
.a2a .avatar {
  width: 28px;
  height: 28px;
  display: flex;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--a-border);
  background: var(--a-bg);
  border-radius: var(--radius-sm, 2px);
  color: var(--a-accent);
  flex-shrink: 0;
}
.a2a .agent-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 12px;
}
.a2a .agent-card {
  padding: 12px;
  text-align: left;
  color: inherit;
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.a2a .agent-card:hover {
  border-color: var(--a-accent);
}
.a2a .agent-card p {
  font-size: 12px;
  color: var(--a-muted);
  min-height: 40px;
}
.a2a .chips {
  display: flex;
  flex-wrap: wrap;
  gap: 5px;
}
.a2a .toolbar {
  display: flex;
  gap: 10px;
  flex-wrap: wrap;
  align-items: center;
}
.a2a .input {
  width: 100%;
  border: 1px solid var(--a-border);
  border-radius: var(--radius-sm, 2px);
  background: var(--bg-input, var(--a-bg));
  padding: 6px 10px;
  font-size: 12px;
  min-width: 0;
}
.a2a .input::placeholder {
  color: var(--a-muted);
  opacity: 0.7;
}
.a2a .search {
  position: relative;
  flex: 1;
  min-width: 190px;
}
.a2a .search svg {
  position: absolute;
  top: 9px;
  left: 10px;
  color: var(--a-muted);
}
.a2a .search .input {
  padding-left: 32px;
}
.a2a .toolbar select {
  width: auto;
  max-width: 220px;
}
.a2a .toolbar label {
  display: flex;
  align-items: center;
  gap: 6px;
  color: var(--a-muted);
  font-size: 11px;
}
.a2a .toolbar input[type="date"] {
  width: 142px;
}
.a2a .segmented {
  display: flex;
  border: 1px solid var(--a-border);
  border-radius: var(--radius-sm, 2px);
  padding: 3px;
  gap: 3px;
}
.a2a .segmented button {
  background: none;
  border: 0;
  border-radius: var(--radius-sm, 2px);
  padding: 6px 9px;
  color: var(--a-muted);
  font-size: 12px;
  display: flex;
  align-items: center;
  gap: 6px;
}
.a2a .segmented button[aria-pressed="true"] {
  background: var(--a-card);
  color: var(--a-text);
}
.a2a .exchange-grid {
  display: grid;
  grid-template-columns: minmax(280px, 0.8fr) minmax(0, 1.2fr);
  align-items: start;
  gap: 16px;
}
.a2a .exchange-list {
  max-height: 720px;
  overflow: auto;
}
.a2a .timeline {
  padding: 16px;
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.a2a .timeline-item {
  position: relative;
  border-left: 1px solid var(--a-border);
  padding-left: 20px;
  margin-left: 7px;
}
.a2a .timeline-item:before {
  content: "";
  position: absolute;
  left: -4px;
  top: 7px;
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--a-accent);
}
.a2a .message-body {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  font-size: 12px;
  line-height: 1.75;
  margin-top: 9px !important;
}
.a2a .detail-meta {
  display: flex;
  gap: 14px;
  flex-wrap: wrap;
  padding: 10px 12px;
  border-bottom: 1px solid var(--a-border);
  font-size: 12px;
  color: var(--a-muted);
}
.a2a .artifacts {
  border-top: 1px solid var(--a-border);
  padding: 12px;
}
.a2a .empty {
  padding: 24px 16px;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-direction: column;
  gap: 8px;
  text-align: center;
  color: var(--a-muted);
}
.a2a .empty h3 {
  color: var(--a-text);
}
.a2a .empty p {
  max-width: 390px;
  font-size: 12px;
}
.a2a .notice {
  padding: 12px 15px;
  border: 1px solid var(--a-border);
  border-radius: var(--radius-sm, 2px);
  font-size: 12px;
  display: flex;
  align-items: center;
  gap: 10px;
  overflow-wrap: anywhere;
}
.a2a .notice.error {
  border-color: var(--error);
  color: var(--error);
}
.a2a .pagination {
  padding: 12px 16px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  border-top: 1px solid var(--a-border);
  font-size: 12px;
  color: var(--a-muted);
}
.a2a .connection-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
  gap: 12px;
}
.a2a .connection {
  padding: 16px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.a2a .kv {
  display: grid;
  grid-template-columns: 110px minmax(0, 1fr);
  font-size: 12px;
  gap: 10px;
}
.a2a .kv dt {
  color: var(--a-muted);
}
.a2a .kv dd {
  margin: 0;
  overflow-wrap: anywhere;
}
.a2a .connection-footer {
  border-top: 1px solid var(--a-border);
  padding-top: 16px;
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}
.a2a .overlay {
  position: fixed;
  inset: 0;
  z-index: 100;
  background: var(--bg-overlay, rgb(0 0 0 / 0.5));
  display: flex;
  justify-content: flex-end;
}
.a2a .sheet {
  background: var(--a-bg);
  border-left: 1px solid var(--a-border);
  width: min(560px, 100%);
  height: 100%;
  overflow: auto;
  box-shadow: var(--shadow-popover, none);
  padding: 20px;
  display: flex;
  flex-direction: column;
  gap: 24px;
}
.a2a .field {
  display: flex;
  flex-direction: column;
  gap: 7px;
  font-size: 12px;
  font-weight: 550;
}
.a2a .field small {
  color: var(--a-muted);
  font-weight: 400;
}
.a2a .steps {
  display: flex;
  gap: 8px;
}
.a2a .step {
  flex: 1;
  padding: 8px 0;
  border-bottom: 2px solid var(--a-border);
  color: var(--a-muted);
  font-size: 11px;
}
.a2a .step.current {
  border-color: var(--a-accent);
  color: var(--a-text);
}
.a2a .option {
  padding: 20px;
  display: flex;
  gap: 14px;
  text-align: left;
  color: inherit;
  align-items: center;
  width: 100%;
}
.a2a .option[aria-pressed="true"] {
  border-color: var(--a-accent);
}
.a2a .checks {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
}
.a2a .checks label {
  font-size: 12px;
  display: flex;
  align-items: center;
  gap: 6px;
}
.a2a input[type="checkbox"] {
  accent-color: var(--a-accent);
}
.a2a .map {
  display: grid;
  grid-template-columns: 220px minmax(40px, 1fr) minmax(240px, 1.5fr);
  align-items: center;
  padding: 20px;
  min-height: 260px;
}
.a2a .map-hub {
  border: 1px solid var(--a-accent);
  border-radius: var(--radius-md, 4px);
  padding: 16px;
  text-align: center;
  background: var(--a-bg);
}
.a2a .map-lines {
  align-self: stretch;
  position: relative;
}
.a2a .map-lines:before {
  content: "";
  position: absolute;
  top: 50%;
  width: 100%;
  border-top: 1px solid var(--a-border);
}
.a2a .map-peers {
  border-left: 1px solid var(--a-border);
  padding-left: 24px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.a2a .map-peer {
  position: relative;
}
.a2a .map-peer:before {
  content: "";
  position: absolute;
  left: -24px;
  width: 24px;
  top: 50%;
  border-top: 1px solid var(--a-border);
}
.a2a .map-peer button {
  width: 100%;
  text-align: left;
  padding: 12px;
  color: inherit;
  display: flex;
  align-items: center;
  gap: 12px;
}
.a2a .spin {
  animation: a2a-spin 1s linear infinite;
}
@keyframes a2a-spin {
  to {
    transform: rotate(360deg);
  }
}
@media (prefers-reduced-motion: reduce) {
  .a2a .spin {
    animation: none;
  }
}
@media (max-width: 1000px) {
  .a2a .overview-grid,
  .a2a .exchange-grid {
    grid-template-columns: 1fr;
  }
  .a2a .exchange-list {
    max-height: 390px;
  }
  .a2a .metrics {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
@media (max-width: 600px) {
  .a2a .header {
    padding: 12px 16px 0;
  }
  .a2a .main {
    padding: 16px;
  }
  .a2a .title-line {
    align-items: flex-start;
  }
  .a2a h1 {
    font-size: 18px;
  }
  .a2a .tabs {
    gap: 0;
  }
  .a2a .tab svg {
    display: none;
  }
  .a2a .metrics {
    gap: 8px;
  }
  .a2a .metric {
    padding: 14px;
  }
  .a2a .metric-value {
    font-size: 22px;
  }
  .a2a .connection-grid {
    grid-template-columns: 1fr;
  }
  .a2a .map {
    grid-template-columns: 1fr;
    padding: 20px;
    gap: 24px;
  }
  .a2a .map-lines {
    display: none;
  }
  .a2a .map-peers {
    padding-left: 15px;
  }
  .a2a .map-peer:before {
    left: -15px;
    width: 15px;
  }
  .a2a .sheet {
    padding: 20px;
  }
}
`;
