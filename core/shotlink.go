package core

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
)

var (
	// Worker pool to handle concurrent Chrome instances
	workerPool     chan *chromeWorker
	maxWorkers     int
	workers        []*chromeWorker
	workersLock    sync.RWMutex
	shutdownOnce   sync.Once
	shutdownChan   chan struct{}
	
	// Metrics
	activeRequests  int64
	totalRequests   int64
	failedRequests  int64
	timeoutRequests int64
	
	// Cache for screenshots
	screenCache     sync.Map // map[string]*cacheEntry
	cacheEnabled    bool
	cacheDuration   time.Duration
)

type chromeWorker struct {
	id       int
	allocCtx context.Context
	cancel   context.CancelFunc
	busy     atomic.Bool
	lastUsed time.Time
	mu       sync.Mutex
}

type cacheEntry struct {
	data      []byte
	timestamp time.Time
}

func init() {
	// Default to 20 workers for high load (200-300 concurrent requests)
	maxWorkers = 20
	if mw := os.Getenv("MAX_CHROME_WORKERS"); mw != "" {
		if val, err := strconv.Atoi(mw); err == nil && val > 0 {
			maxWorkers = val
		}
	}

	// Enable caching (reduces duplicate requests)
	cacheEnabled = true
	if ce := os.Getenv("CACHE_ENABLED"); ce == "false" || ce == "0" {
		cacheEnabled = false
	}

	// Cache duration (default 5 minutes)
	cacheDuration = 5 * time.Minute
	if cd := os.Getenv("CACHE_DURATION_SECONDS"); cd != "" {
		if val, err := strconv.Atoi(cd); err == nil && val > 0 {
			cacheDuration = time.Duration(val) * time.Second
		}
	}

	shutdownChan = make(chan struct{})
	initializeWorkerPool()

	// Start background cleanup goroutine
	go cleanupExpiredCache()
	go monitorWorkers()

	log.Printf("webshot initialized with %d Chrome workers, cache: %v (%v)", 
		maxWorkers, cacheEnabled, cacheDuration)
}

func initializeWorkerPool() {
	workerPool = make(chan *chromeWorker, maxWorkers)
	workers = make([]*chromeWorker, maxWorkers)

	for i := 0; i < maxWorkers; i++ {
		worker := createWorker(i)
		workers[i] = worker
		workerPool <- worker
	}
}

func createWorker(id int) *chromeWorker {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-default-apps", true),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("disable-translate", true),
		chromedp.Flag("hide-scrollbars", true),
		chromedp.Flag("metrics-recording-only", true),
		chromedp.Flag("mute-audio", true),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("safebrowsing-disable-auto-update", true),
		chromedp.Flag("disable-setuid-sandbox", true),
		chromedp.Flag("disable-web-security", true),
		chromedp.Flag("disable-features", "site-per-process,TranslateUI,BlinkGenPropertyTrees"),
		chromedp.Flag("headless", true),
	)

	allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)

	return &chromeWorker{
		id:       id,
		allocCtx: allocCtx,
		cancel:   cancel,
		lastUsed: time.Now(),
	}
}

func getWorker(timeout time.Duration) (*chromeWorker, error) {
	select {
	case worker := <-workerPool:
		worker.busy.Store(true)
		return worker, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("no worker available within timeout")
	case <-shutdownChan:
		return nil, fmt.Errorf("service is shutting down")
	}
}

func releaseWorker(worker *chromeWorker) {
	if worker != nil {
		worker.busy.Store(false)
		worker.lastUsed = time.Now()
		select {
		case workerPool <- worker:
			// Worker returned to pool
		default:
			// Pool is full (shouldn't happen, but defensive)
			log.Printf("Warning: Worker pool full, worker %d not returned", worker.id)
		}
	}
}

func cleanupExpiredCache() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if !cacheEnabled {
				continue
			}
			now := time.Now()
			screenCache.Range(func(key, value interface{}) bool {
				if entry, ok := value.(*cacheEntry); ok {
					if now.Sub(entry.timestamp) > cacheDuration {
						screenCache.Delete(key)
					}
				}
				return true
			})
		case <-shutdownChan:
			return
		}
	}
}

func monitorWorkers() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			active := atomic.LoadInt64(&activeRequests)
			total := atomic.LoadInt64(&totalRequests)
			failed := atomic.LoadInt64(&failedRequests)
			timeouts := atomic.LoadInt64(&timeoutRequests)
			
			log.Printf("Stats: Active=%d, Total=%d, Failed=%d, Timeouts=%d, Workers=%d", 
				active, total, failed, timeouts, maxWorkers)
		case <-shutdownChan:
			return
		}
	}
}

func getCacheKey(url string, width, height int) string {
	hash := md5.Sum([]byte(fmt.Sprintf("%s:%dx%d", url, width, height)))
	return hex.EncodeToString(hash[:])
}

func Shutdown() {
	shutdownOnce.Do(func() {
		log.Println("webshot: Shutting down Chrome worker pool...")
		close(shutdownChan)
		
		workersLock.Lock()
		defer workersLock.Unlock()
		
		for _, worker := range workers {
			if worker != nil && worker.cancel != nil {
				worker.cancel()
			}
		}
		log.Println("webshot: Worker pool shutdown complete")
	})
}

func HandleScreenshot(writer http.ResponseWriter, r *http.Request) {
	// Panic recovery for safety
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("Panic recovered in HandleScreenshot: %v", rec)
			atomic.AddInt64(&failedRequests, 1)
			http.Error(writer, "Internal server error", http.StatusInternalServerError)
		}
		atomic.AddInt64(&activeRequests, -1)
	}()

	atomic.AddInt64(&totalRequests, 1)
	atomic.AddInt64(&activeRequests, 1)

	url := r.URL.Query().Get("url")
	if url == "" {
		http.Error(writer, "'url' parameter is required", http.StatusBadRequest)
		return
	}

	width, height := 1280, 720
	if w := r.URL.Query().Get("width"); w != "" {
		if val, err := strconv.Atoi(w); err == nil && val > 0 && val <= 3840 {
			width = val
		}
	}
	if h := r.URL.Query().Get("height"); h != "" {
		if val, err := strconv.Atoi(h); err == nil && val > 0 && val <= 2160 {
			height = val
		}
	}

	// Check cache first
	if cacheEnabled {
		cacheKey := getCacheKey(url, width, height)
		if cached, ok := screenCache.Load(cacheKey); ok {
			if entry, ok := cached.(*cacheEntry); ok {
				if time.Since(entry.timestamp) < cacheDuration {
					writer.Header().Set("Content-Type", "image/png")
					writer.Header().Set("X-Cache", "HIT")
					writer.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(cacheDuration.Seconds())))
					writer.WriteHeader(http.StatusOK)
					writer.Write(entry.data)
					return
				}
			}
		}
	}

	timeout := 45 * time.Second
	if t := os.Getenv("SCREENSHOT_TIMEOUT"); t != "" {
		if val, err := strconv.Atoi(t); err == nil && val > 0 {
			timeout = time.Duration(val) * time.Second
		}
	}

	workerTimeout := 15 * time.Second
	if wt := os.Getenv("WORKER_TIMEOUT"); wt != "" {
		if val, err := strconv.Atoi(wt); err == nil && val > 0 {
			workerTimeout = time.Duration(val) * time.Second
		}
	}

	// Get worker from pool
	worker, err := getWorker(workerTimeout)
	if err != nil {
		log.Printf("Failed to get worker for %s: %v", url, err)
		atomic.AddInt64(&timeoutRequests, 1)
		atomic.AddInt64(&failedRequests, 1)
		http.Error(writer, "Server busy, please retry later", http.StatusServiceUnavailable)
		return
	}
	defer releaseWorker(worker)

	// Capture screenshot
	buf, err := captureScreenshot(worker, url, width, height, timeout)
	if err != nil {
		log.Printf("Error capturing screenshot (%s): %v", url, err)
		atomic.AddInt64(&failedRequests, 1)
		
		if err == context.DeadlineExceeded {
			atomic.AddInt64(&timeoutRequests, 1)
			http.Error(writer, "Screenshot timeout - page took too long to load", http.StatusRequestTimeout)
		} else {
			http.Error(writer, "Error capturing screenshot", http.StatusInternalServerError)
		}
		return
	}

	// Cache the result
	if cacheEnabled && len(buf) > 0 {
		cacheKey := getCacheKey(url, width, height)
		screenCache.Store(cacheKey, &cacheEntry{
			data:      buf,
			timestamp: time.Now(),
		})
	}

	writer.Header().Set("Content-Type", "image/png")
	writer.Header().Set("X-Cache", "MISS")
	writer.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(cacheDuration.Seconds())))
	writer.WriteHeader(http.StatusOK)
	writer.Write(buf)
}

func captureScreenshot(worker *chromeWorker, url string, width, height int, timeout time.Duration) ([]byte, error) {
	worker.mu.Lock()
	defer worker.mu.Unlock()

	ctx, cancel := chromedp.NewContext(worker.allocCtx)
	defer cancel()

	ctx, timeoutCancel := context.WithTimeout(ctx, timeout)
	defer timeoutCancel()

	var buf []byte
	err := chromedp.Run(ctx,
		emulation.SetDeviceMetricsOverride(int64(width), int64(height), 1.0, false),
		chromedp.Navigate(url),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Sleep(1*time.Second),
		chromedp.FullScreenshot(&buf, 90),
	)

	return buf, err
}

func HandleHealth(writer http.ResponseWriter, r *http.Request) {
	active := atomic.LoadInt64(&activeRequests)
	total := atomic.LoadInt64(&totalRequests)
	failed := atomic.LoadInt64(&failedRequests)
	timeouts := atomic.LoadInt64(&timeoutRequests)
	
	availableWorkers := len(workerPool)
	
	status := "healthy"
	statusCode := http.StatusOK
	
	if availableWorkers == 0 {
		status = "degraded"
		statusCode = http.StatusTooManyRequests
	}
	
	response := fmt.Sprintf(`{"status":"%s","active_requests":%d,"total_requests":%d,"failed_requests":%d,"timeout_requests":%d,"available_workers":%d,"max_workers":%d}`,
		status, active, total, failed, timeouts, availableWorkers, maxWorkers)
	
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(statusCode)
	writer.Write([]byte(response))
}
