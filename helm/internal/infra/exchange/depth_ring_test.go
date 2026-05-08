package exchange

import (
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func makeSnap(price float64) L2Snapshot {
	p := decimal.NewFromFloat(price)
	return L2Snapshot{
		Symbol:    "BTCUSDT",
		Timestamp: time.Now(),
		Bids:      [5]L2Level{{Price: p, Size: decimal.NewFromFloat(1)}},
		Asks:      [5]L2Level{{Price: p.Add(decimal.NewFromFloat(0.01)), Size: decimal.NewFromFloat(1)}},
	}
}

func TestDepthRing_Empty(t *testing.T) {
	r := &DepthRing{}
	if _, ok := r.Latest(); ok {
		t.Fatal("expected empty")
	}
	if r.Len() != 0 || r.Head() != 0 || r.Tail() != 0 {
		t.Fatalf("want len=0 head=0 tail=0, got %d %d %d", r.Len(), r.Head(), r.Tail())
	}
}

func TestDepthRing_NotFull(t *testing.T) {
	r := &DepthRing{}
	for i := 1; i <= 5; i++ {
		r.Push(makeSnap(float64(i * 10)))
	}
	if r.Tail() != 0 {
		t.Fatalf("tail should be 0 while not full, got %d", r.Tail())
	}
	if r.Head() != 5 || r.Len() != 5 {
		t.Fatalf("head=%d len=%d, want 5 5", r.Head(), r.Len())
	}
	snap, ok := r.Latest()
	if !ok || snap.Bids[0].Price.InexactFloat64() != 50 {
		t.Fatalf("Latest want 50, got %s", snap.Bids[0].Price)
	}
}

func TestDepthRing_TailAdvancesWhenFull(t *testing.T) {
	r := &DepthRing{}
	cap := uint64(depthRingCap)

	// fill exactly to capacity — tail stays 0
	for i := uint64(0); i < cap; i++ {
		r.Push(makeSnap(float64(i)))
	}
	if r.Tail() != 0 {
		t.Fatalf("tail should be 0 when exactly full, got %d", r.Tail())
	}
	if r.Len() != depthRingCap {
		t.Fatalf("len want %d got %d", depthRingCap, r.Len())
	}

	// cap+1 th push → head=cap+1, tail=1
	r.Push(makeSnap(float64(cap)))
	if r.Tail() != 1 {
		t.Fatalf("tail should be 1 after first overwrite, got %d", r.Tail())
	}
	if r.Len() != depthRingCap {
		t.Fatalf("len should stay %d, got %d", depthRingCap, r.Len())
	}

	// 2 more → tail=3
	r.Push(makeSnap(float64(cap + 1)))
	r.Push(makeSnap(float64(cap + 2)))
	if r.Tail() != 3 {
		t.Fatalf("tail should be 3, got %d", r.Tail())
	}
}

func TestDepthRing_Oldest(t *testing.T) {
	r := &DepthRing{}

	// push cap+3 entries so tail=3, ring holds entries [3, cap+3)
	n := depthRingCap + 3
	for i := 0; i < n; i++ {
		r.Push(makeSnap(float64(i)))
	}
	if r.Tail() != 3 {
		t.Fatalf("tail want 3, got %d", r.Tail())
	}

	// Oldest(4) → entries at indices 3,4,5,6 → prices 3,4,5,6
	old4 := r.Oldest(4)
	if len(old4) != 4 {
		t.Fatalf("want 4, got %d", len(old4))
	}
	for i, want := range []float64{3, 4, 5, 6} {
		got := old4[i].Bids[0].Price.InexactFloat64()
		if got != want {
			t.Errorf("Oldest[%d] want %.0f got %.0f", i, want, got)
		}
	}

	// Oldest(all) → cap entries, first is price=3, last is price=cap+2
	all := r.Oldest(depthRingCap)
	if len(all) != depthRingCap {
		t.Fatalf("want %d, got %d", depthRingCap, len(all))
	}
	if all[0].Bids[0].Price.InexactFloat64() != 3 {
		t.Errorf("first want 3, got %s", all[0].Bids[0].Price)
	}
	if all[depthRingCap-1].Bids[0].Price.InexactFloat64() != float64(n-1) {
		t.Errorf("last want %d, got %s", n-1, all[depthRingCap-1].Bids[0].Price)
	}
}

func TestDepthRing_Last(t *testing.T) {
	r := &DepthRing{}
	for i := 1; i <= 5; i++ {
		r.Push(makeSnap(float64(i * 10)))
	}
	last3 := r.Last(3)
	if len(last3) != 3 {
		t.Fatalf("want 3, got %d", len(last3))
	}
	// newest first: 50, 40, 30
	for i, want := range []float64{50, 40, 30} {
		got := last3[i].Bids[0].Price.InexactFloat64()
		if got != want {
			t.Errorf("Last[%d] want %.0f got %.0f", i, want, got)
		}
	}
}

func TestDepthRing_ConcurrentReadWrite(t *testing.T) {
	r := &DepthRing{}
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10_000; i++ {
			r.Push(makeSnap(float64(i)))
		}
	}()

	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 10_000 {
				r.Latest()  //nolint
				r.Oldest(8) //nolint
			}
		}()
	}
	wg.Wait()
}
