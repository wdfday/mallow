// hand-stress load-tests the Hand actor model: N real Hand goroutines under
// M real HelmRuntimes, driven through the exact production entry point
// (HelmRuntime.DispatchHandSignal — what SignalDispatcher.Dispatch calls for
// every NATS-delivered signal), with a fake exchange that fills synchronously
// so the whole entry→fill→poslog path actually runs.
//
// Two things get measured:
//  1. memory/goroutine footprint per hand (create + Start N hands, no traffic)
//  2. end-to-end round-trip throughput: broadcast one signal to every hand,
//     wait for every hand's fill event, repeat alternating long/exit —
//     optionally with a real embedded-NATS JetStream poslog wired in, to see
//     whether the WAL write path adds meaningful latency at scale.
//
// Usage:
//
//	go run ./cmd/hand-stress -phase footprint -hands 50000
//	go run ./cmd/hand-stress -phase throughput -hands 5000 -rounds 50 -poslog
//	go run ./cmd/hand-stress -phase throughput -hands 5000 -rounds 200 -cpuprofile cpu.pprof
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"runtime/pprof"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/fleet/actor"
	"mallow/helm/internal/fleet/actor/core/portfolio"
	"mallow/helm/internal/fleet/actor/core/risk"
	"mallow/helm/internal/fleet/actor/core/strategy"
	"mallow/helm/internal/fleet/actor/core/tactics"
	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/journal/poslog"
	"mallow/helm/internal/infra/natsapi"
	"mallow/helm/internal/module/hand/domain"
)

func main() {
	slog.SetLogLoggerLevel(slog.LevelError) // hand run-loops log a lot; keep the report readable

	phase := flag.String("phase", "footprint", "footprint | throughput")
	hands := flag.Int("hands", 5000, "total hands")
	helms := flag.Int("helms", 1, "spread hands round-robin across N helms")
	rounds := flag.Int("rounds", 50, "throughput phase: alternating long/exit rounds")
	withPoslog := flag.Bool("poslog", false, "throughput phase: wire a real embedded-NATS JetStream poslog")
	cpuProfile := flag.String("cpuprofile", "", "throughput phase: write a pprof CPU profile of the round loop here")
	concurrentDispatch := flag.Bool("concurrent", false, "throughput phase: fire each round's signals from N goroutines instead of one sequential loop")
	flag.Parse()

	switch *phase {
	case "footprint":
		runFootprint(*hands, *helms)
	case "throughput":
		runThroughput(*hands, *helms, *rounds, *withPoslog, *cpuProfile, *concurrentDispatch)
	case "poslog":
		runPoslogBench()
	default:
		fmt.Fprintf(os.Stderr, "unknown -phase %q (want footprint | throughput | poslog)\n", *phase)
		os.Exit(1)
	}
}

// ── simExchange ─────────────────────────────────────────────────────────────
// Copy of actor_test.simExchange (signal_unit_test.go) — that file is a _test.go
// in an external test package and can't be imported from a real binary, so the
// fake is duplicated here rather than pulled in.

type simExchange struct {
	mu        sync.Mutex
	fillPrice decimal.Decimal
	onFill    func(exchange.WsFillEvent)
	placed    int
}

func newSimExchange() *simExchange {
	return &simExchange{fillPrice: decimal.NewFromFloat(50_000)}
}

func (s *simExchange) Name() string { return "sim" }

func (s *simExchange) PlaceOrder(_ context.Context, _ exchange.Credentials, req exchange.OrderRequest) (*exchange.OrderResult, error) {
	s.mu.Lock()
	s.placed++
	id := fmt.Sprintf("sim-%d", s.placed)
	qty := req.Qty
	if qty.IsZero() {
		qty = decimal.NewFromFloat(0.01)
	}
	onFill := s.onFill
	price := s.fillPrice
	s.mu.Unlock()

	result := &exchange.OrderResult{
		ID: id, Symbol: req.Symbol, Side: req.Side, Status: "submitted",
		Qty: qty, FilledQty: decimal.Zero, FilledAvg: decimal.Zero,
	}
	if onFill != nil {
		go onFill(exchange.WsFillEvent{
			OrderID: id, ClientOrderID: req.ClientOrderID, Symbol: req.Symbol, Side: req.Side,
			FilledQty: qty, FilledAvg: price, FillID: "sim-fill-" + id, Timestamp: time.Now(),
		})
	}
	return result, nil
}

func (s *simExchange) StreamOrders(
	_ context.Context, _ exchange.Credentials,
	_ func(exchange.OrderLifecycleEvent),
	onFill func(exchange.WsFillEvent),
	_ func(exchange.BalanceEvent),
	_ func(exchange.PositionEvent),
	_ func(exchange.RiskEvent),
	_ func(string),
) error {
	s.mu.Lock()
	s.onFill = onFill
	s.mu.Unlock()
	return nil
}

func (s *simExchange) GetOrder(_ context.Context, _ exchange.Credentials, id string) (*exchange.OrderResult, error) {
	return &exchange.OrderResult{ID: id, Status: "filled"}, nil
}
func (s *simExchange) CancelOrder(_ context.Context, _ exchange.Credentials, _ string) error {
	return nil
}
func (s *simExchange) ListOpenOrders(_ context.Context, _ exchange.Credentials, _ string) ([]exchange.OrderResult, error) {
	return nil, nil
}
func (s *simExchange) ListPositions(_ context.Context, _ exchange.Credentials) ([]exchange.PositionResult, error) {
	return nil, nil
}

// ── fleet construction ──────────────────────────────────────────────────────

func buildRuntime(maxPositions int) *actor.HelmRuntime {
	pf := portfolio.New(decimal.NewFromFloat(1_000_000_000))
	cfg := risk.Config{MaxPositions: maxPositions, DailyLossLimitPct: 0.99, MaxDrawdownPct: 0.99}
	rm := risk.New(cfg, pf)
	rt := actor.NewHelmRuntime(
		uuid.New(), uuid.New(), uuid.New(),
		"sim", pf, rm, newSimExchange(), exchange.Credentials{}, nil, time.Now(),
	)
	rm.SetUnitCounter(rt.OpenUnitCount)
	rt.StartStreaming(context.Background())
	return rt
}

func addHand(rt *actor.HelmRuntime, symbol string) *actor.Hand {
	strat := strategy.NewSignalFollower(0.5)
	tact := tactics.New(tactics.SizingConfig{Mode: tactics.SizingFixedQty, FixedQty: decimal.NewFromFloat(0.01)})
	h := actor.NewHand(
		uuid.New(), rt.HelmID, rt,
		strat, tact,
		false, 1, 24*time.Hour,
		nil, domain.OrderTypeMarket, 0, domain.LimitFallbackCancel,
		domain.HandGuardConfig{}, decimal.Zero,
	)
	h.Symbol = symbol
	h.StrategyName = "signal_follower"
	h.EnableEventSink()
	rt.MarketData.SetPrice(symbol, decimal.NewFromFloat(50_000)) // fleet/market normally owns this; nothing feeds it in this synthetic harness
	rt.AddHand(h, &domain.Hand{ID: h.ID(), HelmID: rt.HelmID, Symbols: domain.StringSlice{symbol}})
	h.Start()
	return h
}

// handRT pairs a Hand with the HelmRuntime that owns it — Hand doesn't expose
// its runtime back out, and DispatchHandSignal (the real production entry
// point, called by SignalDispatcher.Dispatch for every NATS signal) lives on
// HelmRuntime, so callers need both.
type handRT struct {
	h  *actor.Hand
	rt *actor.HelmRuntime
}

// buildFleet spreads n hands round-robin across m helms, each on its own symbol.
func buildFleet(n, m int) []handRT {
	if m < 1 {
		m = 1
	}
	runtimes := make([]*actor.HelmRuntime, m)
	for i := range runtimes {
		runtimes[i] = buildRuntime(n)
	}
	fleet := make([]handRT, n)
	for i := 0; i < n; i++ {
		rt := runtimes[i%m]
		fleet[i] = handRT{h: addHand(rt, fmt.Sprintf("STRESS%d", i)), rt: rt}
	}
	return fleet
}

// ── phase 1: memory / goroutine footprint ───────────────────────────────────

func runFootprint(maxHands, helmsCount int) {
	sweep := []int{100, 1000, 5000, 20000, maxHands}
	sort.Ints(sweep)
	fmt.Printf("%-10s %-10s %-14s %-14s %-12s %-12s\n", "hands", "helms", "heap_alloc", "rss_sys", "goroutines", "bytes/hand")
	seen := map[int]bool{}
	for _, n := range sweep {
		if n <= 0 || seen[n] {
			continue
		}
		seen[n] = true
		runtime.GC()
		var before runtime.MemStats
		runtime.ReadMemStats(&before)
		g0 := runtime.NumGoroutine()

		hands := buildFleet(n, helmsCount)

		runtime.GC()
		var after runtime.MemStats
		runtime.ReadMemStats(&after)
		g1 := runtime.NumGoroutine()

		perHand := float64(0)
		if n > 0 {
			perHand = float64(after.HeapAlloc-before.HeapAlloc) / float64(n)
		}
		fmt.Printf("%-10d %-10d %-14s %-14s %-12d %-12.0f\n",
			n, helmsCount,
			humanBytes(after.HeapAlloc-before.HeapAlloc),
			humanBytes(after.Sys-before.Sys),
			g1-g0,
			perHand,
		)
		_ = hands // keep alive until measured; GC'd on process exit
	}
}

func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// ── phase 2: end-to-end throughput ───────────────────────────────────────────

func setupPoslog() (poslog.Log, func(), error) {
	opts := natstest.DefaultTestOptions
	opts.Port = -1
	opts.JetStream = true
	dir, err := os.MkdirTemp("", "hand-stress-js")
	if err != nil {
		return nil, nil, err
	}
	opts.StoreDir = dir
	srv := natstest.RunServer(&opts)

	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		srv.Shutdown()
		os.RemoveAll(dir)
		return nil, nil, err
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		srv.Shutdown()
		os.RemoveAll(dir)
		return nil, nil, err
	}
	if _, err := js.AddStream(&nats.StreamConfig{
		Name: "HELM_POSITIONS", Subjects: []string{"helm.pos.>"},
		Storage: nats.MemoryStorage, MaxAge: 30 * 24 * time.Hour, Duplicates: 2 * time.Minute,
	}); err != nil {
		nc.Close()
		srv.Shutdown()
		os.RemoveAll(dir)
		return nil, nil, err
	}
	log, err := poslog.NewNatsLog(js)
	cleanup := func() {
		nc.Close()
		srv.Shutdown()
		os.RemoveAll(dir)
	}
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return log, cleanup, nil
}

func runThroughput(n, m, rounds int, withPoslog bool, cpuProfilePath string, concurrentDispatch bool) {
	fmt.Printf("hands=%d helms=%d rounds=%d poslog=%v concurrent=%v\n", n, m, rounds, withPoslog, concurrentDispatch)

	var pl poslog.Log
	if withPoslog {
		log, cleanup, err := setupPoslog()
		if err != nil {
			fmt.Fprintf(os.Stderr, "embedded NATS/JetStream setup failed: %v\n", err)
			os.Exit(1)
		}
		defer cleanup()
		pl = log
	}

	fleet := buildFleet(n, m)
	if pl != nil {
		runtimesSeen := map[*actor.HelmRuntime]bool{}
		for _, f := range fleet {
			if !runtimesSeen[f.rt] {
				f.rt.PosLog = pl
				runtimesSeen[f.rt] = true
			}
		}
	}

	// One persistent listener goroutine per hand, resolving the round's WaitGroup
	// slot on ANY terminal-for-this-signal event — a fill, or a gate rejecting the
	// signal outright (e.g. the helm-level circuit breaker below). Only counting
	// CodeOrderFilled would leave the round hanging on rejections until the
	// 10s timeout, once per rejected hand, which is a real outcome worth reporting,
	// not something to mask.
	var wg sync.WaitGroup
	var filled, rejected atomic.Int64
	var reasonsMu sync.Mutex
	reasons := map[string]int64{}
	for _, f := range fleet {
		// buf=64: handEventBus.publish is non-blocking-drop under backpressure, and a
		// single signal fans out 4-5 codes (received→approved→placed→filled); too
		// small a buffer here silently drops the terminal event and hangs the round.
		events := f.h.Subscribe(64)
		go func(ch <-chan natsapi.HelmEvent) {
			for ev := range ch {
				switch ev.Code {
				case actor.CodeOrderFilled:
					filled.Add(1)
					wg.Done()
				case actor.CodeSignalRejected, actor.CodeSignalDoNothing, actor.CodeSignalStale,
					actor.CodeSignalHelmPaused, actor.CodeSignalNoPosition, actor.CodeSignalRateLimited,
					actor.CodeOrderFailed:
					rejected.Add(1)
					reasonsMu.Lock()
					reasons[fmt.Sprintf("%d:%s", ev.Code, ev.Reason)]++
					reasonsMu.Unlock()
					wg.Done()
				}
			}
		}(events)
	}

	if cpuProfilePath != "" {
		f, err := os.Create(cpuProfilePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cpuprofile: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}

	var roundTimes []time.Duration
	var timeouts int
	long := true
	for r := 0; r < rounds; r++ {
		wg.Add(len(fleet))
		start := time.Now()
		dir := strategy.DirLong
		if !long {
			dir = strategy.DirExit
		}
		if concurrentDispatch {
			var dispatchWg sync.WaitGroup
			dispatchWg.Add(len(fleet))
			for _, f := range fleet {
				go func(f handRT) {
					defer dispatchWg.Done()
					sig := strategy.Signal{Symbol: f.h.Symbol, Direction: dir, Strength: 1.0, ReceivedAt: time.Now().UTC()}
					f.rt.DispatchHandSignal(f.h.ID().String(), sig)
				}(f)
			}
			dispatchWg.Wait()
		} else {
			for _, f := range fleet {
				sig := strategy.Signal{Symbol: f.h.Symbol, Direction: dir, Strength: 1.0, ReceivedAt: time.Now().UTC()}
				f.rt.DispatchHandSignal(f.h.ID().String(), sig)
			}
		}
		if !waitTimeout(&wg, 10*time.Second) {
			timeouts++
		}
		roundTimes = append(roundTimes, time.Since(start))
		long = !long
	}

	sort.Slice(roundTimes, func(i, j int) bool { return roundTimes[i] < roundTimes[j] })
	p50 := roundTimes[len(roundTimes)*50/100]
	p99 := roundTimes[min(len(roundTimes)*99/100, len(roundTimes)-1)]
	worst := roundTimes[len(roundTimes)-1]
	fmt.Printf("round latency  p50=%v  p99=%v  max=%v  filled=%d  rejected=%d  timed_out_rounds=%d\n",
		p50, p99, worst, filled.Load(), rejected.Load(), timeouts)
	if len(reasons) > 0 {
		fmt.Println("rejection breakdown:")
		for k, v := range reasons {
			fmt.Printf("  %-50s %d\n", k, v)
		}
	}
}

func waitTimeout(wg *sync.WaitGroup, timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// ── phase 3: poslog write throughput in isolation ───────────────────────────
//
// Bypasses the Hand actor entirely — hammers poslog.Log.Publish (real embedded
// JetStream, same code path as production) directly from N concurrent
// goroutines, sweeping concurrency to find where the single JetStream instance
// actually saturates.

func runPoslogBench() {
	pl, cleanup, err := setupPoslog()
	if err != nil {
		fmt.Fprintf(os.Stderr, "embedded NATS/JetStream setup failed: %v\n", err)
		os.Exit(1)
	}
	defer cleanup()

	const perLevel = 2 * time.Second
	sweep := []int{1, 10, 50, 200, 1000, 5000}
	fmt.Printf("%-12s %-14s %-12s %-12s %-12s\n", "concurrency", "events/sec", "p50", "p99", "max")
	for _, c := range sweep {
		eps, p50, p99, max := runPoslogLevel(pl, c, perLevel)
		fmt.Printf("%-12d %-14.0f %-12v %-12v %-12v\n", c, eps, p50, p99, max)
	}
}

func runPoslogLevel(pl poslog.Log, concurrency int, duration time.Duration) (eventsPerSec float64, p50, p99, max time.Duration) {
	var wg sync.WaitGroup
	resultsCh := make(chan []time.Duration, concurrency)
	stop := make(chan struct{})
	ctx := context.Background()

	start := time.Now()
	for g := 0; g < concurrency; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			local := make([]time.Duration, 0, 1024)
			i := 0
			for {
				select {
				case <-stop:
					resultsCh <- local
					return
				default:
				}
				ev := poslog.Event{
					ID:      fmt.Sprintf("g%d-%d-%d", gid, i, time.Now().UnixNano()),
					HandID:  fmt.Sprintf("hand-%d", gid),
					HelmID:  "stress-helm",
					TradeID: fmt.Sprintf("trade-%d-%d", gid, i),
					Kind:    poslog.KindOrderFilled,
					Payload: []byte(`{}`),
					At:      time.Now(),
				}
				t0 := time.Now()
				_ = pl.Publish(ctx, ev)
				local = append(local, time.Since(t0))
				i++
			}
		}(g)
	}
	time.Sleep(duration)
	close(stop)
	wg.Wait()
	close(resultsCh)
	elapsed := time.Since(start)

	var all []time.Duration
	total := 0
	for local := range resultsCh {
		all = append(all, local...)
		total += len(local)
	}
	if total == 0 {
		return 0, 0, 0, 0
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	p50 = all[len(all)*50/100]
	p99 = all[min(len(all)*99/100, len(all)-1)]
	max = all[len(all)-1]
	eventsPerSec = float64(total) / elapsed.Seconds()
	return
}
