import { useEffect, useMemo, useState } from "react";
import { Header } from "./components/Header.tsx";
import { PortfolioSidebar } from "./components/PortfolioSidebar.tsx";
import { PortfolioHeader } from "./components/PortfolioHeader.tsx";
import { Watchlist } from "./components/Watchlist.tsx";
import { SymbolPanel } from "./components/SymbolPanel.tsx";
import { OrderTicket } from "./components/OrderTicket.tsx";
import { PositionsTable } from "./components/PositionsTable.tsx";
import { OrdersTable } from "./components/OrdersTable.tsx";
import { AgentFeed } from "./components/AgentFeed.tsx";
import { usePortfolios, usePortfolio, usePositions, useOrders, useJournal } from "./hooks/usePortfolios.ts";
import { useUniverse } from "./hooks/useUniverse.ts";
import { useHealth } from "./hooks/useHealth.ts";
import { useAppEvents } from "./hooks/useAppEvents.ts";

export function App() {
  const [dark, setDark] = useState(true);
  const [portfolioId, setPortfolioId] = useState<number | null>(null);
  const [bottomTab, setBottomTab] = useState<"positions" | "orders" | "feed">("positions");

  useEffect(() => { document.body.classList.toggle("dark", dark); }, [dark]);

  const portfolios = usePortfolios();
  const universe = useUniverse();
  const portfolio = usePortfolio(portfolioId);
  const positions = usePositions(portfolioId);
  const orders = useOrders(portfolioId);
  const journal = useJournal(portfolioId);
  const health = useHealth();

  // Event-driven cache invalidation. Each app-event from the sidecar
  // (order.placed, fill, journal.appended, tick, etc.) bumps the
  // refresh tick of the relevant useFetch hooks. The hooks themselves
  // poll on a slow heartbeat (30s) as a backstop for missed events.
  useAppEvents((ev: { topic: string; data: any }) => {
    const pid = ev.data && (ev.data.portfolio_id ?? ev.data.id);
    const matchesSelected = portfolioId == null || pid == null || pid === portfolioId;
    switch (ev.topic) {
      case "tick":
        // Marks moved → universe + health surfaces refresh.
        universe.refresh();
        health.refresh();
        // Positions re-mark too (their market_value depends on marks).
        if (matchesSelected) positions.refresh();
        return;
      case "portfolio.created":
      case "portfolio.status.changed":
        portfolios.refresh();
        if (matchesSelected) portfolio.refresh();
        return;
      case "order.placed":
      case "order.filled":
      case "order.cancelled":
      case "order.rejected":
        if (!matchesSelected) return;
        orders.refresh();
        portfolio.refresh();   // cash/equity drift
        positions.refresh();   // potential new/removed position
        return;
      case "position.changed":
        if (!matchesSelected) return;
        positions.refresh();
        portfolio.refresh();
        return;
      case "journal.appended":
        if (!matchesSelected) return;
        journal.refresh();
        return;
      case "watchlist.changed":
        if (!matchesSelected) return;
        portfolio.refresh();   // watchlist is a field on Portfolio
        return;
      case "alert.fired":
        if (!matchesSelected) return;
        journal.refresh();     // alert engine writes a journal row too
        return;
      default:
        return;
    }
  });

  // Auto-select the first portfolio once the list loads.
  useEffect(() => {
    if (portfolioId == null && portfolios.data && portfolios.data.length > 0) {
      setPortfolioId(portfolios.data[0]!.id);
    }
  }, [portfolios.data, portfolioId]);

  // Default symbol = first watched symbol on the selected portfolio.
  const [symbol, setSymbol] = useState<string>("");
  useEffect(() => {
    if (portfolio.data && portfolio.data.watchlist && portfolio.data.watchlist.length > 0) {
      setSymbol(portfolio.data.watchlist[0]!);
    }
  }, [portfolio.data?.id]);

  const allSyms = useMemo(() => universe.data ?? [], [universe.data]);

  if (portfolios.loading && portfolios.data == null) {
    return <div className="min-h-dvh grid place-items-center t-tertiary text-sm">Loading…</div>;
  }
  if (portfolios.error && portfolios.data == null) {
    return (
      <div className="min-h-dvh grid place-items-center px-6">
        <div className="glass rounded-2xl p-6 max-w-md text-center">
          <p className="text-sm t-down mb-2">Could not reach the trading sidecar.</p>
          <p className="text-[11px] t-tertiary mono">{portfolios.error}</p>
        </div>
      </div>
    );
  }
  if (portfolios.data && portfolios.data.length === 0) {
    return (
      <div className="min-h-dvh grid place-items-center px-6">
        <div className="glass rounded-2xl p-6 max-w-md text-center">
          <p className="text-sm t-secondary">No portfolios yet in this project.</p>
          <p className="text-[11px] t-tertiary mt-2">Have an agent call <span className="mono">portfolio_list</span>, or create one through the dashboard.</p>
        </div>
      </div>
    );
  }

  return (
    <div className="min-h-dvh px-4 py-4 sm:px-5 sm:py-4">
      <div className="mx-auto w-full max-w-[1700px] flex flex-col gap-4 min-h-[calc(100dvh-2rem)]">
        <Header
          dark={dark}
          onToggleDark={() => setDark((d) => !d)}
          portfolios={portfolios.data ?? []}
          health={health.data ?? null}
        />

        <div
          className="grid gap-4 flex-1 min-h-0"
          style={{ gridTemplateColumns: "minmax(240px, 280px) minmax(0, 1fr)" }}
        >
          <PortfolioSidebar
            portfolios={portfolios.data ?? []}
            selectedId={portfolioId ?? -1}
            onSelect={setPortfolioId}
          />

          <div className="flex flex-col gap-4 min-h-0">
            {portfolio.data && <PortfolioHeader p={portfolio.data} />}

            <div
              className="grid gap-4"
              style={{ gridTemplateColumns: "minmax(260px, 300px) minmax(0, 1fr) minmax(280px, 340px)" }}
            >
              {portfolio.data && (
                <Watchlist
                  portfolio={portfolio.data}
                  universe={allSyms}
                  selected={symbol}
                  onSelect={setSymbol}
                />
              )}
              {symbol && allSyms.length > 0 && <SymbolPanel symbol={symbol} universe={allSyms} />}
              {portfolio.data && symbol && (
                <OrderTicket symbol={symbol} portfolio={portfolio.data} universe={allSyms} />
              )}
            </div>

            <section className="glass-strong rounded-2xl p-1.5 flex flex-col fade-up flex-1 min-h-[300px]">
              <div className="flex items-center gap-1 px-2 pt-1 pb-2 border-b border-[var(--border)]">
                {(
                  [
                    { k: "positions", label: "Positions", count: positions.data?.length ?? 0 },
                    { k: "orders",    label: "Orders",    count: orders.data?.length ?? 0 },
                    { k: "feed",      label: "Agent",     count: journal.data?.length ?? 0 },
                  ] as const
                ).map((t) => (
                  <button
                    key={t.k}
                    onClick={() => setBottomTab(t.k)}
                    className={`tab flex items-center gap-1.5 ${bottomTab === t.k ? "active" : ""}`}
                  >
                    <span>{t.label}</span>
                    <span className="text-[10px] t-tertiary mono">{t.count}</span>
                  </button>
                ))}
                <span className="ml-auto pr-2 text-[10px] t-tertiary mono">v0.1 · paper</span>
              </div>

              <div className="flex-1 min-h-0 mt-1.5">
                {bottomTab === "positions" && <PositionsTable positions={positions.data ?? []} onSelect={setSymbol} />}
                {bottomTab === "orders"    && <OrdersTable orders={orders.data ?? []} />}
                {bottomTab === "feed"      && portfolio.data && (
                  <AgentFeed portfolio={portfolio.data} entries={journal.data ?? []} />
                )}
              </div>
            </section>
          </div>
        </div>

        <footer className="flex items-center justify-center gap-3 text-[11px] t-tertiary py-1">
          <span>Apteva · Trading</span>
          <span>·</span>
          <span>Paper mode — no broker connected.</span>
        </footer>
      </div>
    </div>
  );
}
