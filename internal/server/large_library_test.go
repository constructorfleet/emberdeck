package server_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestLargeLibraryProgressPollsStayFast stores 500 programs of 4 MiB each
// (about 2 GiB) through the public upload API, then measures worker progress
// polls and operator list polls against that library.
func TestLargeLibraryProgressPollsStayFast(t *testing.T) {
	if testing.Short() {
		t.Skip("stores about 2 GiB of job programs")
	}
	// CPU- and disk-bound; overlap it with the package's timer-bound tests.
	t.Parallel()
	const jobs = 500
	const programBytes = 4 << 20
	s := openServer(t)
	// Leave room below the 4 MiB program cap for the JSON envelope and escaped
	// line endings. Long comment-padded lines keep the escaping overhead small.
	line := "G1 X10.000 Y10.000 F1000 ; " + strings.Repeat("p", 1024-28) + "\n"
	program := strings.Repeat(line, (programBytes-8<<10)/len(line))
	encoded, _ := json.Marshal(program)
	payload := func(i int) string {
		return fmt.Sprintf(`{"name":"large-%03d.gcode","gcode":%s}`, i, encoded)
	}
	started := time.Now()
	ids := make([]string, jobs)
	var wg sync.WaitGroup
	next := make(chan int)
	errs := make(chan string, jobs)
	for worker := 0; worker < runtime.GOMAXPROCS(0); worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				w := request(t, s, "POST", "/api/v1/jobs", payload(i), "test-key")
				if w.Code != http.StatusCreated {
					errs <- fmt.Sprintf("upload %d: %d %s", i, w.Code, w.Body.String())
					continue
				}
				var job struct{ ID string }
				_ = json.Unmarshal(w.Body.Bytes(), &job)
				ids[i] = job.ID
			}
		}()
	}
	// Stop feeding uploads well before the go test deadline so a slow runner
	// fails this test with a clear message instead of panicking the package.
	stopAt := time.Time{}
	if deadline, ok := t.Deadline(); ok {
		stopAt = deadline.Add(-time.Until(deadline) / 4)
	}
	queued := 0
	for ; queued < jobs; queued++ {
		if !stopAt.IsZero() && time.Now().After(stopAt) {
			break
		}
		next <- queued
	}
	close(next)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if queued < jobs {
		t.Fatalf("library build exceeded its time budget after %d of %d uploads in %s; raise the go test -timeout", queued, jobs, time.Since(started))
	}
	t.Logf("stored %d programs of %d bytes in %s", jobs, len(program), time.Since(started))

	lease := startOnDriver(t, s, "bench", ids[jobs-1])
	var progress, listing []time.Duration
	largestList := 0
	for acked := 1; acked <= 100; acked++ {
		body := fmt.Sprintf(`{"session":"bench-session","job_id":"%s","lease":"%s","state":"running","controller_state":"Run","acked":%d,"total":%d}`, ids[jobs-1], lease, acked, len(program)/len(line))
		begin := time.Now()
		w := request(t, s, "POST", "/api/v1/drivers/bench/poll", body, "test-key")
		progress = append(progress, time.Since(begin))
		if w.Code != http.StatusOK {
			t.Fatalf("progress %d: %d %s", acked, w.Code, w.Body.String())
		}
		if acked%10 == 0 {
			begin = time.Now()
			w = request(t, s, "GET", "/api/v1/jobs?limit=200", "", "test-key")
			listing = append(listing, time.Since(begin))
			if w.Code != http.StatusOK {
				t.Fatalf("list: %d %s", w.Code, w.Body.String())
			}
			largestList = max(largestList, w.Body.Len())
		}
	}
	p50, p99, worst := percentiles(progress)
	_, _, worstList := percentiles(listing)
	t.Logf("progress poll p50=%s p99=%s max=%s; list max=%s, largest list body=%d bytes", p50, p99, worst, worstList, largestList)
	// Rewriting a 2 GiB library per poll takes seconds; metadata-only polls take
	// microseconds. The bounds leave room for the race detector and slow CI disks.
	if p99 > 100*time.Millisecond || worst > 500*time.Millisecond {
		t.Fatalf("progress poll latency grows with library size: p99=%s max=%s", p99, worst)
	}
	if worstList > 500*time.Millisecond || largestList > 256<<10 {
		t.Fatalf("job list is not bounded metadata: max=%s body=%d bytes", worstList, largestList)
	}
}

func percentiles(samples []time.Duration) (time.Duration, time.Duration, time.Duration) {
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2], sorted[(len(sorted)*99)/100], sorted[len(sorted)-1]
}
