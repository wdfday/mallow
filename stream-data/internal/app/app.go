package app

import (
	"context"
	"log"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"stream-data/internal/infra"
	"stream-data/internal/model"
	"stream-data/internal/saver"
	"stream-data/internal/stream"
)

// App holds all wired dependencies for the streaming service.
type App struct {
	cfg           *Config
	barProviders  []stream.BarRegistration
	tickProviders []stream.Registration
	tickSink      stream.TickSink
	jsPub         *infra.JetStreamPublisher
	nc            *nats.Conn // shared connection; nil if NATS disabled
}

// NewApp constructs a fully wired App from config.
//
// Tick pipeline (e.g. TwelveData):
//
//	ticks → FanOut(
//	    writer,              // always: persist to disk
//	    barAggregator,       // when NATS active: aggregate → bars.{symbol} on JetStream
//	    natsPub,             // optional: raw ticks → ticks.{class}.{symbol} on NATS core
//	)
//
// Bar pipeline (Binance, OKX, Alpaca):
//
//	bars → JetStream (bars.{symbol})
func NewApp(cfg *Config) *App {
	barProviders := BuildBarProviders(cfg)
	tickProviders := BuildTickProviders(cfg)

	hasBar := len(barProviders) > 0
	hasTick := len(tickProviders) > 0

	if !hasBar && !hasTick {
		slog.Warn("no sources enabled — check config.yaml")
	}

	// NATS + JetStream — only needed when providers are active
	var (
		nc    *nats.Conn
		jsPub *infra.JetStreamPublisher
	)
	if cfg.NATS.Enabled && (hasBar || hasTick) {
		conn, err := infra.NewNATSConn(cfg.NATS.URL)
		if err != nil {
			log.Fatalf("nats connect failed: %v", err)
		}
		nc = conn
		js, err := infra.NewJetStreamPublisher(nc)
		if err != nil {
			log.Fatalf("jetstream publisher init failed: %v", err)
		}
		jsPub = js
	}

	// Tick sink — only needed when tick providers are active
	var tickSink stream.TickSink
	if hasTick {
		s := saver.New(cfg.Data.Format)
		if s == nil {
			slog.Warn("unknown data.format, falling back to parquet", "format", cfg.Data.Format)
			s = saver.New("parquet")
		}
		writer := stream.NewWriter(cfg.Data.Dir, s, cfg.Data.FlushInterval, cfg.Data.MaxBufferSize)

		tickSinks := []stream.TickSink{writer}
		if jsPub != nil {
			interval := cfg.Bars.Interval
			if interval == 0 {
				interval = time.Minute
			}
			tickSinks = append(tickSinks, stream.NewBarAggregator(interval, jsPub))
			slog.Info("bar aggregation enabled for tick providers", "interval", interval)
		}

		if len(tickSinks) == 1 {
			tickSink = tickSinks[0]
		} else {
			tickSink = stream.NewFanOut(tickSinks...)
		}
	}

	return &App{
		cfg:           cfg,
		barProviders:  barProviders,
		tickProviders: tickProviders,
		tickSink:      tickSink,
		jsPub:         jsPub,
		nc:            nc,
	}
}

// Run wires the application and blocks until ctx is cancelled.
func Run(ctx context.Context) {
	InitLogger()

	cfg, err := LoadConfig()
	if err != nil {
		slog.Error("config load failed", "err", err)
		os.Exit(1)
	}
	defer cfg.ApplyLogger()()

	NewApp(cfg).run(ctx)
}

// run starts all providers and routes data until ctx is cancelled.
func (a *App) run(ctx context.Context) {
	defer a.close()

	if len(a.barProviders) == 0 && len(a.tickProviders) == 0 {
		slog.Error("no sources enabled — check config.yaml")
		return
	}

	var wg sync.WaitGroup

	// Bar pipeline: BarProviders → MergeBars → JetStream
	if len(a.barProviders) > 0 && a.jsPub != nil {
		var barChans []<-chan model.Bar
		for _, reg := range a.barProviders {
			ch, err := reg.Provider.StreamBars(ctx, reg.Symbols)
			if err != nil {
				slog.Error("bar provider failed", "provider", reg.Provider.Name(), "err", err)
				continue
			}
			slog.Info("streaming bars", "provider", reg.Provider.Name(), "symbols", len(reg.Symbols))
			barChans = append(barChans, ch)
		}
		if len(barChans) > 0 {
			merged := stream.MergeBars(barChans...)
			wg.Go(func() { a.jsPub.RunBars(ctx, merged) })
		}
	}

	// Tick pipeline: TickProviders → tickSink (writer + aggregator + optional natsPub)
	if len(a.tickProviders) > 0 {
		var tickChans []<-chan model.Tick
		for _, reg := range a.tickProviders {
			ch, err := reg.Provider.Stream(ctx, reg.Symbols)
			if err != nil {
				slog.Error("tick provider failed", "provider", reg.Provider.Name(), "err", err)
				continue
			}
			slog.Info("streaming ticks", "provider", reg.Provider.Name(), "symbols", len(reg.Symbols))
			tickChans = append(tickChans, ch)
		}
		if len(tickChans) > 0 {
			merged := stream.Merge(tickChans...)
			wg.Go(func() { a.tickSink.Run(ctx, merged) })
		}
	}

	slog.Info("pipeline started",
		"bar_providers", len(a.barProviders),
		"tick_providers", len(a.tickProviders),
		"nats", a.cfg.NATS.Enabled)

	wg.Wait()
	slog.Info("pipeline stopped")
}

func (a *App) close() {
	if a.nc != nil {
		if err := a.nc.Drain(); err != nil {
			slog.Warn("nats drain error", "err", err)
		}
		a.nc.Close()
	}
}
