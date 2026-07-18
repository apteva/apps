package main

// Yahoo Finance public client — free, no-auth equity / etf quotes, OHLCV
// bars, and dividend history. Backed by Yahoo's /v8/finance/chart
// endpoint, the same source the `yfinance` Python lib uses. One request
// carries the meta-style quote, the timestamp + OHLCV arrays, AND (with
// events=div) the dividend payment history — so a single call covers
// every facet this app surfaces for a symbol.
//
// We deliberately use /chart rather than the more obviously-named
// /v7/finance/quote or /v10/finance/quoteSummary endpoints: those have
// had cookie / crumb requirements bolted on over the years that break
// unauthenticated callers, while /chart has stayed stable and key-free.
// The trade-off is that fundamentals only available via quoteSummary
// (market cap, P/E, payout ratio) aren't here in v0.1; dividend yield is
// computed from the dividend events + current price instead, which keeps
// the whole app on the one reliable endpoint.
//
// v0.2 will route this through the platform's yahoo-finance integration
// (ExecuteIntegrationTool) so richer fundamentals can come from a bound
// provider; v0.1 hits the public endpoint directly so a fresh install
// shows a real AAPL price with zero operator setup.

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"
)

// hiddenInputRe pulls the hidden form fields out of Yahoo's EU consent
// page (csrfToken, sessionId, originalDoneUrl, namespace).
var hiddenInputRe = regexp.MustCompile(`<input type="hidden" name="([^"]+)" value="([^"]*)">`)

// Rate budget. perMin is the sustained refill; burst is the bucket
// capacity, sized so an interactive detail open (~6 calls) clears
// instantly. Background warming only draws above `reserve`, leaving that
// many tokens for on-demand so detail loads never queue behind the warmer.
const (
	yahooRatePerMin = 20
	yahooBurst      = 30
	yahooReserve    = 12
	chartBackoff    = 2 * time.Minute
)

// rateLimiter is a token bucket: every Yahoo request acquires a token
// before going out, so total request rate is bounded no matter where the
// call originates (the per-client semaphore only bounds concurrency).
// Background callers yield to on-demand via the reserve.
type rateLimiter struct {
	tokens  chan struct{}
	reserve int
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
}

func newRateLimiter(perMin, burst, reserve int) *rateLimiter {
	rl := &rateLimiter{
		tokens: make(chan struct{}, burst), reserve: reserve,
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	for i := 0; i < burst; i++ { // start full so the initial warm can burst
		rl.tokens <- struct{}{}
	}
	go func() {
		defer close(rl.done)
		t := time.NewTicker(time.Minute / time.Duration(perMin))
		defer t.Stop()
		for {
			select {
			case <-rl.stop:
				return
			case <-t.C:
				select {
				case rl.tokens <- struct{}{}:
				default: // bucket full
				}
			}
		}
	}()
	return rl
}

func (rl *rateLimiter) close() {
	rl.once.Do(func() { close(rl.stop) })
	<-rl.done
}

// wait blocks for a token or until ctx is done. On-demand callers (bg=false)
// take any available token immediately; background warming (bg=true) only
// draws when the bucket is above the reserve, so it never starves a detail
// load.
func (rl *rateLimiter) wait(ctx context.Context, bg bool) error {
	if !bg {
		select {
		case <-rl.tokens:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	t := time.NewTicker(150 * time.Millisecond)
	defer t.Stop()
	for {
		if len(rl.tokens) > rl.reserve {
			select {
			case <-rl.tokens:
				return nil
			default:
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

const yahooBase = "https://query1.finance.yahoo.com"

// browserUA — Yahoo silently drops the Go default UA and gates the crumb
// endpoint on a browser-ish agent.
const browserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"

type yahooClient struct {
	base   string
	client *http.Client
	// Concurrency cap on parallel symbol fetches. Yahoo's unofficial
	// rate limit is ~2k/hour observed; capping at 4 in-flight keeps the
	// universe warm-up well under that.
	sem chan struct{}
	// crumb + cookie state for the quoteSummary fundamentals endpoint,
	// which (unlike /chart) requires the cookie+crumb handshake. Guarded
	// by mu; cookies live in the client's jar. crumbRetryAt backs off the
	// handshake after a failure (Yahoo throttles getcrumb hard) so the
	// warmer doesn't hammer it once per symbol.
	mu           sync.Mutex
	crumb        string
	crumbRetryAt time.Time
	chartRetryAt time.Time
	limiter      *rateLimiter
}

const crumbBackoff = 15 * time.Minute

func newYahoo() *yahooClient {
	// publicsuffix.List is required so the jar treats A1/A3 as
	// .yahoo.com domain cookies and sends them across subdomains
	// (finance/consent/guce/query1); a nil list keeps them host-only and
	// getcrumb 401s.
	jar, _ := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	return &yahooClient{
		base:    yahooBase,
		client:  &http.Client{Timeout: 8 * time.Second, Jar: jar},
		sem:     make(chan struct{}, 4),
		limiter: newRateLimiter(yahooRatePerMin, yahooBurst, yahooReserve),
	}
}

func (y *yahooClient) close() {
	if y != nil && y.limiter != nil {
		y.limiter.close()
	}
}

// acquire waits for a rate-limiter token. bg=true (background warming) waits
// patiently and yields to on-demand; bg=false (interactive) takes a token
// immediately from the burst budget.
func (y *yahooClient) acquire(bg bool) error {
	return y.acquireContext(context.Background(), bg)
}

func (y *yahooClient) acquireContext(ctx context.Context, bg bool) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return y.limiter.wait(ctx, bg)
}

// Bar is one OHLCV candle. Short JSON keys keep the chart payload small
// over the wire to the panel.
type Bar struct {
	T int64   `json:"t"` // unix seconds
	O float64 `json:"o"`
	H float64 `json:"h"`
	L float64 `json:"l"`
	C float64 `json:"c"`
	V float64 `json:"v"`
}

// Dividend is one per-share cash dividend payment.
type Dividend struct {
	ExDate int64   `json:"ex_date"` // unix seconds
	Amount float64 `json:"amount"`
}

// quoteMeta is the normalized slice of chart.result[0].meta this app
// reads. Yahoo packs many more fields there; we pull only what the get /
// list surfaces need.
type quoteMeta struct {
	Symbol           string  `json:"symbol"`
	Currency         string  `json:"currency"`
	Exchange         string  `json:"exchange"`
	InstrumentType   string  `json:"instrument_type"`
	Name             string  `json:"name"`
	Price            float64 `json:"price"`
	PreviousClose    float64 `json:"previous_close"`
	DayHigh          float64 `json:"day_high"`
	DayLow           float64 `json:"day_low"`
	Volume           float64 `json:"volume"`
	FiftyTwoWeekHigh float64 `json:"fifty_two_week_high"`
	FiftyTwoWeekLow  float64 `json:"fifty_two_week_low"`
}

// chartResult is one fully-parsed /chart response.
type chartResult struct {
	Meta      quoteMeta
	Bars      []Bar
	Dividends []Dividend
}

// fetchChart makes one call to /v8/finance/chart for a symbol. rng +
// interval are Yahoo's own enums (range: 1mo|6mo|1y|5y|max; interval:
// 1d|1wk|1mo). withDivs adds events=div so the response carries the
// dividend history.
func (y *yahooClient) fetchChart(symbol, rng, interval string, withDivs, bg bool) (*chartResult, error) {
	return y.fetchChartContext(context.Background(), symbol, rng, interval, withDivs, bg)
}

func (y *yahooClient) fetchChartContext(ctx context.Context, symbol, rng, interval string, withDivs, bg bool) (*chartResult, error) {
	q := url.Values{}
	q.Set("range", rng)
	q.Set("interval", interval)
	if withDivs {
		q.Set("events", "div")
	}
	u := y.base + "/v8/finance/chart/" + url.PathEscape(strings.ToUpper(symbol)) + "?" + q.Encode()

	y.mu.Lock()
	backoff := time.Now().Before(y.chartRetryAt)
	y.mu.Unlock()
	if backoff {
		return nil, fmt.Errorf("chart endpoint backing off after 429")
	}
	if err := y.acquireContext(ctx, bg); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	// Yahoo silently drops requests carrying the Go default UA.
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Apteva-Stocks/0.1)")
	req.Header.Set("Accept", "application/json")

	resp, err := y.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		y.mu.Lock()
		y.chartRetryAt = time.Now().Add(chartBackoff)
		y.mu.Unlock()
	}
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("yahoo HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}

	var doc struct {
		Chart struct {
			Result []struct {
				Meta struct {
					Symbol               string  `json:"symbol"`
					Currency             string  `json:"currency"`
					ExchangeName         string  `json:"exchangeName"`
					InstrumentType       string  `json:"instrumentType"`
					LongName             string  `json:"longName"`
					ShortName            string  `json:"shortName"`
					RegularMarketPrice   float64 `json:"regularMarketPrice"`
					PreviousClose        float64 `json:"previousClose"`
					ChartPreviousClose   float64 `json:"chartPreviousClose"`
					RegularMarketDayHigh float64 `json:"regularMarketDayHigh"`
					RegularMarketDayLow  float64 `json:"regularMarketDayLow"`
					RegularMarketVolume  float64 `json:"regularMarketVolume"`
					FiftyTwoWeekHigh     float64 `json:"fiftyTwoWeekHigh"`
					FiftyTwoWeekLow      float64 `json:"fiftyTwoWeekLow"`
				} `json:"meta"`
				Timestamp  []int64 `json:"timestamp"`
				Indicators struct {
					Quote []struct {
						Open   []float64 `json:"open"`
						High   []float64 `json:"high"`
						Low    []float64 `json:"low"`
						Close  []float64 `json:"close"`
						Volume []float64 `json:"volume"`
					} `json:"quote"`
				} `json:"indicators"`
				Events struct {
					Dividends map[string]struct {
						Amount float64 `json:"amount"`
						Date   int64   `json:"date"`
					} `json:"dividends"`
				} `json:"events"`
			} `json:"result"`
			Error any `json:"error"`
		} `json:"chart"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("yahoo decode: %w", err)
	}
	if doc.Chart.Error != nil {
		return nil, fmt.Errorf("yahoo error envelope: %v", doc.Chart.Error)
	}
	if len(doc.Chart.Result) == 0 {
		return nil, fmt.Errorf("yahoo: no data for %s", symbol)
	}
	r := doc.Chart.Result[0]

	out := &chartResult{Meta: quoteMeta{
		Symbol:         orStr(r.Meta.Symbol, strings.ToUpper(symbol)),
		Currency:       r.Meta.Currency,
		Exchange:       r.Meta.ExchangeName,
		InstrumentType: r.Meta.InstrumentType,
		Name:           orStr(r.Meta.LongName, r.Meta.ShortName),
		Price:          r.Meta.RegularMarketPrice,
		// previousClose is the *prior trading day's* close — but Yahoo only
		// populates it on intraday ranges; on daily ranges it's null. Do NOT
		// fall back to chartPreviousClose here: that's the close before the
		// whole range window (≈1y ago for range=1y), which would turn the day
		// change into a 1-year return. Callers derive the real prior close
		// from the bars when this is 0 (see dayChange).
		PreviousClose:    r.Meta.PreviousClose,
		DayHigh:          r.Meta.RegularMarketDayHigh,
		DayLow:           r.Meta.RegularMarketDayLow,
		Volume:           r.Meta.RegularMarketVolume,
		FiftyTwoWeekHigh: r.Meta.FiftyTwoWeekHigh,
		FiftyTwoWeekLow:  r.Meta.FiftyTwoWeekLow,
	}}

	if len(r.Indicators.Quote) > 0 {
		qd := r.Indicators.Quote[0]
		out.Bars = make([]Bar, 0, len(r.Timestamp))
		for i, t := range r.Timestamp {
			// Yahoo emits null inside OHLC arrays for holidays / gaps;
			// the decoder turns those into 0. Skip zero-close rows so
			// the chart doesn't plot flat-zero dips.
			if i >= len(qd.Close) || qd.Close[i] == 0 {
				continue
			}
			b := Bar{T: t, C: qd.Close[i]}
			if i < len(qd.Open) {
				b.O = qd.Open[i]
			}
			if i < len(qd.High) {
				b.H = qd.High[i]
			}
			if i < len(qd.Low) {
				b.L = qd.Low[i]
			}
			if i < len(qd.Volume) {
				b.V = qd.Volume[i]
			}
			out.Bars = append(out.Bars, b)
		}
	}

	for _, d := range r.Events.Dividends {
		if d.Amount <= 0 || d.Date == 0 {
			continue
		}
		out.Dividends = append(out.Dividends, Dividend{ExDate: d.Date, Amount: d.Amount})
	}
	sort.Slice(out.Dividends, func(i, j int) bool { return out.Dividends[i].ExDate < out.Dividends[j].ExDate })

	return out, nil
}

// ─── Fundamentals (P/E, payout) via cookie+crumb quoteSummary ───────
//
// Yahoo's /v10/finance/quoteSummary endpoint carries the fundamentals
// /chart doesn't (trailing P/E, payout ratio) but gates them behind a
// cookie+crumb handshake — the same one yahoo-finance2 performs: seed an
// A1/A3 cookie from a quote page, exchange it for a crumb at
// /v1/test/getcrumb, then pass the crumb on every quoteSummary call. The
// crumb is cached and only re-fetched on a 401. Every error degrades
// gracefully (caller leaves P/E / payout blank).

// ensureCrumb returns a cached crumb, fetching one if absent.
func (y *yahooClient) ensureCrumb(bg bool) (string, error) {
	return y.ensureCrumbContext(context.Background(), bg)
}

func (y *yahooClient) ensureCrumbContext(ctx context.Context, bg bool) (string, error) {
	y.mu.Lock()
	defer y.mu.Unlock()
	if y.crumb != "" {
		return y.crumb, nil
	}
	if time.Now().Before(y.crumbRetryAt) {
		return "", fmt.Errorf("crumb handshake backing off until %s", y.crumbRetryAt.Format(time.RFC3339))
	}
	// On any failure below, back off so a throttled / failing getcrumb
	// isn't re-hit once per symbol (which would perpetuate the throttle).
	fail := func(err error) (string, error) {
		y.crumbRetryAt = time.Now().Add(crumbBackoff)
		return "", err
	}
	// Basic strategy: fc.yahoo.com 404s but sets the A1/A3 session cookie
	// on most IPs — enough for getcrumb. (The publicsuffix jar is required
	// so that .yahoo.com cookie is sent on to query1.)
	_, _, _ = y.httpGetContext(ctx, "https://fc.yahoo.com", bg)
	crumb, err := y.getCrumbRawContext(ctx, bg)
	if err != nil || !validCrumb(crumb) {
		// Fallback for EU / consent-walled IPs where fc sets nothing: walk
		// Yahoo's GDPR consent flow, then retry getcrumb.
		if cerr := y.establishViaConsentContext(ctx, bg); cerr != nil {
			return fail(fmt.Errorf("crumb handshake failed (fc: %v; consent: %v)", err, cerr))
		}
		crumb, err = y.getCrumbRawContext(ctx, bg)
		if err != nil {
			return fail(err)
		}
	}
	if !validCrumb(crumb) {
		return fail(fmt.Errorf("getcrumb returned no usable crumb (%.40q)", crumb))
	}
	y.crumb = crumb
	y.crumbRetryAt = time.Time{}
	return crumb, nil
}

// validCrumb rejects error bodies (HTML, JSON, "Too Many Requests") — a
// real crumb is a short token with no spaces or markup.
func validCrumb(c string) bool {
	return c != "" && len(c) < 40 && !strings.ContainsAny(c, "<{ ")
}

// establishViaConsent loads a quote page and, if Yahoo redirects to its EU
// GDPR consent wall, submits the consent form so the A1/A3 cookies get
// set. Mirrors yahoo-finance2's getCrumb consent handling.
func (y *yahooClient) establishViaConsent(bg bool) error {
	return y.establishViaConsentContext(context.Background(), bg)
}

func (y *yahooClient) establishViaConsentContext(ctx context.Context, bg bool) error {
	finalURL, body, err := y.httpGetContext(ctx, "https://finance.yahoo.com/quote/AAPL", bg)
	if err != nil {
		return err
	}
	if strings.Contains(finalURL, "guce.yahoo") || strings.Contains(finalURL, "consent.yahoo") {
		return y.submitConsentContext(ctx, finalURL, body, bg)
	}
	return nil
}

// submitConsent replays Yahoo's GDPR consent form (mirrors
// yahoo-finance2's getCrumb): POST the hidden fields back with
// agree=agree, then GET copyConsent so the platform sets the cookies.
func (y *yahooClient) submitConsent(consentURL, body string, bg bool) error {
	return y.submitConsentContext(context.Background(), consentURL, body, bg)
}

func (y *yahooClient) submitConsentContext(ctx context.Context, consentURL, body string, bg bool) error {
	fields := map[string]string{}
	for _, m := range hiddenInputRe.FindAllStringSubmatch(body, -1) {
		fields[m[1]] = html.UnescapeString(m[2])
	}
	form := url.Values{}
	for _, k := range []string{"csrfToken", "sessionId", "originalDoneUrl", "namespace"} {
		if v, ok := fields[k]; ok {
			form.Set(k, v)
		}
	}
	form.Add("agree", "agree")
	form.Add("agree", "agree")
	if _, _, err := y.httpPostContext(ctx, consentURL, form, bg); err != nil {
		return err
	}
	if sid := fields["sessionId"]; sid != "" {
		_, _, _ = y.httpGetContext(ctx, "https://guce.yahoo.com/copyConsent?sessionId="+url.QueryEscape(sid), bg)
	}
	return nil
}

// getCrumbRaw fetches the crumb (cookies already in the client jar).
func (y *yahooClient) getCrumbRaw(bg bool) (string, error) {
	return y.getCrumbRawContext(context.Background(), bg)
}

func (y *yahooClient) getCrumbRawContext(ctx context.Context, bg bool) (string, error) {
	_, body, err := y.httpGetContext(ctx, "https://query1.finance.yahoo.com/v1/test/getcrumb", bg)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(body), nil
}

// httpGet does a UA-stamped GET (cookies via the client jar, redirects
// followed) and returns the final URL + body. Non-2xx is an error.
func (y *yahooClient) httpGet(u string, bg bool) (finalURL, body string, err error) {
	return y.httpGetContext(context.Background(), u, bg)
}

func (y *yahooClient) httpGetContext(ctx context.Context, u string, bg bool) (finalURL, body string, err error) {
	if err := y.acquireContext(ctx, bg); err != nil {
		return "", "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("User-Agent", browserUA)
	// "*/*" — getcrumb replies text/plain, which a narrower Accept rejects
	// with 406. Browsers send */*; matching that keeps every step happy.
	req.Header.Set("Accept", "*/*")
	resp, err := y.client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return resp.Request.URL.String(), string(b), fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
	}
	return resp.Request.URL.String(), string(b), nil
}

// httpPost submits a form (UA-stamped, jar cookies, redirects followed).
func (y *yahooClient) httpPost(u string, form url.Values, bg bool) (finalURL, body string, err error) {
	return y.httpPostContext(context.Background(), u, form, bg)
}

func (y *yahooClient) httpPostContext(ctx context.Context, u string, form url.Values, bg bool) (finalURL, body string, err error) {
	if err := y.acquireContext(ctx, bg); err != nil {
		return "", "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := y.client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		return resp.Request.URL.String(), string(b), fmt.Errorf("POST %s: HTTP %d", u, resp.StatusCode)
	}
	return resp.Request.URL.String(), string(b), nil
}

func (y *yahooClient) clearCrumb() {
	y.mu.Lock()
	y.crumb = ""
	y.mu.Unlock()
}

// fundamentalsState reports the crumb handshake health for the sync view:
// "ok" (have a crumb), "backoff" (handshake failed, retrying at retryAt),
// or "untried" (no crumb yet, no failure recorded).
func (y *yahooClient) fundamentalsState() (state string, retryAt *int64) {
	y.mu.Lock()
	defer y.mu.Unlock()
	if y.crumb != "" {
		return "ok", nil
	}
	if time.Now().Before(y.crumbRetryAt) {
		t := y.crumbRetryAt.Unix()
		return "backoff", &t
	}
	return "untried", nil
}

// fundamentals returns trailing P/E and payout ratio (as a percentage)
// for a symbol. Either may be nil when Yahoo doesn't report it; an error
// means the whole fetch failed (throttle, crumb) and the caller should
// leave both blank.
// fundamentals returns trailing P/E, payout ratio (%), and market cap (in
// billions) for a symbol. Any may be nil when Yahoo doesn't report it; an
// error means the whole fetch failed (throttle, crumb).
func (y *yahooClient) fundamentals(symbol string, bg bool) (pe, payoutPct, mcap *float64, err error) {
	return y.fundamentalsContext(context.Background(), symbol, bg)
}

func (y *yahooClient) fundamentalsContext(ctx context.Context, symbol string, bg bool) (pe, payoutPct, mcap *float64, err error) {
	crumb, err := y.ensureCrumbContext(ctx, bg)
	if err != nil {
		return nil, nil, nil, err
	}
	pe, payoutPct, mcap, status, err := y.fetchSummaryContext(ctx, symbol, crumb, bg)
	if status == http.StatusUnauthorized {
		// Stale crumb — refresh once and retry.
		y.clearCrumb()
		if crumb, err = y.ensureCrumbContext(ctx, bg); err == nil {
			pe, payoutPct, mcap, _, err = y.fetchSummaryContext(ctx, symbol, crumb, bg)
		}
	}
	return pe, payoutPct, mcap, err
}

func (y *yahooClient) fetchSummary(symbol, crumb string, bg bool) (pe, payoutPct, mcap *float64, status int, err error) {
	return y.fetchSummaryContext(context.Background(), symbol, crumb, bg)
}

func (y *yahooClient) fetchSummaryContext(ctx context.Context, symbol, crumb string, bg bool) (pe, payoutPct, mcap *float64, status int, err error) {
	u := y.base + "/v10/finance/quoteSummary/" + url.PathEscape(strings.ToUpper(symbol)) +
		"?modules=summaryDetail&crumb=" + url.QueryEscape(crumb)
	if err := y.acquireContext(ctx, bg); err != nil {
		return nil, nil, nil, 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("User-Agent", browserUA)
	resp, err := y.client.Do(req)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	defer resp.Body.Close()
	status = resp.StatusCode
	if status != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 512))
		return nil, nil, nil, status, fmt.Errorf("quoteSummary HTTP %d", status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, nil, nil, status, err
	}
	var doc struct {
		QuoteSummary struct {
			Result []struct {
				SummaryDetail struct {
					TrailingPE struct {
						Raw float64 `json:"raw"`
					} `json:"trailingPE"`
					PayoutRatio struct {
						Raw float64 `json:"raw"`
					} `json:"payoutRatio"`
					MarketCap struct {
						Raw float64 `json:"raw"`
					} `json:"marketCap"`
				} `json:"summaryDetail"`
			} `json:"result"`
		} `json:"quoteSummary"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, nil, nil, status, err
	}
	if len(doc.QuoteSummary.Result) == 0 {
		return nil, nil, nil, status, fmt.Errorf("quoteSummary: empty result for %s", symbol)
	}
	sd := doc.QuoteSummary.Result[0].SummaryDetail
	if sd.TrailingPE.Raw > 0 {
		v := sd.TrailingPE.Raw
		pe = &v
	}
	// payoutRatio is a fraction (0.45 = 45%); store as a percentage.
	if sd.PayoutRatio.Raw > 0 {
		v := sd.PayoutRatio.Raw * 100
		payoutPct = &v
	}
	// marketCap is raw currency; store in billions for a single screener unit.
	if sd.MarketCap.Raw > 0 {
		v := sd.MarketCap.Raw / 1e9
		mcap = &v
	}
	return pe, payoutPct, mcap, status, nil
}
