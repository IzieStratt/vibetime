package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestLoadBudget simulates Hackatime-scale traffic: VIBETIME_LOAD=1 go test -run LoadBudget -v ./internal/server
//
// 109,759 h/month / 730 h ≈ 150 users active at any moment. We simulate `devices` concurrently
// active devices (default 1500 = 10x average) each syncing once per simulated minute, for `minutes`
// simulated minutes, as fast as the server allows, and report sustained throughput.
func TestLoadBudget(t *testing.T) {
	if os.Getenv("VIBETIME_LOAD") == "" {
		t.Skip("set VIBETIME_LOAD=1")
	}
	const devices, minutes = 1500, 10
	s, err := Open(filepath.Join(t.TempDir(), "load.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	clock := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	var nowNs atomic.Int64
	nowNs.Store(clock.UnixNano())
	s.Now = func() time.Time { return time.Unix(0, nowNs.Load()).UTC() }
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	toks := make([]string, devices)
	for i := range toks {
		toks[i], _ = s.CreateUser(fmt.Sprintf("u%d", i), false)
	}
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 256}}
	var reqs, fails atomic.Int64
	start := time.Now()
	for m := 0; m < minutes; m++ {
		now := clock.Add(time.Duration(m+1) * time.Minute)
		nowNs.Store(now.UnixNano())
		sem := make(chan struct{}, 128)
		var wg sync.WaitGroup
		for d := 0; d < devices; d++ {
			wg.Add(1)
			sem <- struct{}{}
			go func(d int) {
				defer wg.Done()
				defer func() { <-sem }()
				body, _ := json.Marshal(SyncRequest{Device: fmt.Sprintf("device-%06d", d), Head: fmt.Sprintf("%064d", m),
					Minutes: []SyncMinute{{Project: "p", Minute: now.Add(-time.Minute).Unix(), Seconds: 60}}})
				req, _ := http.NewRequest("POST", ts.URL+"/api/v1/sync", bytes.NewReader(body))
				req.Header.Set("Authorization", "Bearer "+toks[d])
				r, err := client.Do(req)
				reqs.Add(1)
				if err != nil || r.StatusCode != 200 {
					fails.Add(1)
				}
				if r != nil {
					r.Body.Close()
				}
			}(d)
		}
		wg.Wait()
	}
	el := time.Since(start)
	rps := float64(reqs.Load()) / el.Seconds()
	needed := float64(devices) / 60
	fi, _ := os.Stat(filepath.Join(filepath.Dir(s.dbPath()), "load.db"))
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	t.Logf("%d syncs in %v: %.0f req/s sustained on %d cores (need %.0f req/s for %d active devices: %.0fx headroom); heap %d MB",
		reqs.Load(), el.Round(time.Millisecond), rps, runtime.NumCPU(), needed, devices, rps/needed, ms.HeapAlloc>>20)
	if fi != nil {
		perRow := float64(fi.Size()) / float64(devices*minutes)
		monthRows := 109759.0 * 60
		t.Logf("db %.1f MB for %d minute-rows: ≈ %.0f B/row → ≈ %.1f GB/month at Hackatime volume", float64(fi.Size())/1e6, devices*minutes, perRow, perRow*monthRows/1e9)
	}
	if fails.Load() > 0 || rps < needed*10 {
		t.Fatalf("fails=%d rps=%.0f", fails.Load(), rps)
	}
}
