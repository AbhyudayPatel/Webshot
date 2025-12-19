# webshot Deployment Guide for High Load (200-300 concurrent requests)

## Architecture Overview

The service now uses a **worker pool pattern** with:
- **20 pre-initialized Chrome workers** (configurable)
- **Request caching** to reduce duplicate processing (5-minute default TTL)
- **Graceful degradation** when all workers are busy
- **Automatic metrics tracking** and health monitoring
- **Panic recovery** to prevent crashes
- **Proper resource cleanup** on shutdown

## Resource Requirements

### Minimum (Production)
- **CPU**: 4 cores
- **RAM**: 4GB
- **Disk**: 2GB

### Recommended (High Load)
- **CPU**: 8 cores
- **RAM**: 8GB
- **Disk**: 5GB

## Quick Start

### Using Docker Compose (Recommended)
```bash
docker-compose up -d
```

### Using Docker
```bash
docker build -t webshot .
docker run -d \
  --name webshot \
  -p 8080:8080 \
  --memory=4g \
  --cpus=4 \
  --shm-size=512m \
  -e MAX_CHROME_WORKERS=20 \
  -e CACHE_ENABLED=true \
  webshot
```

## Configuration

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `MAX_CHROME_WORKERS` | 20 | Number of concurrent Chrome instances |
| `SCREENSHOT_TIMEOUT` | 45 | Timeout per screenshot (seconds) |
| `WORKER_TIMEOUT` | 15 | Timeout to acquire worker (seconds) |
| `CACHE_ENABLED` | true | Enable response caching |
| `CACHE_DURATION_SECONDS` | 300 | Cache TTL (seconds) |

### Tuning for Your Load

**For 100-200 concurrent requests:**
```bash
MAX_CHROME_WORKERS=15
```

**For 300-500 concurrent requests:**
```bash
MAX_CHROME_WORKERS=30
# Also increase resources: 8GB RAM, 6 CPUs
```

**Disable caching (if all requests are unique):**
```bash
CACHE_ENABLED=false
```

## API Endpoints

## API Endpoints

### 1. Screenshot Capture
```
GET /get?url=<URL>&width=<W>&height=<H>
```

**Parameters:**
- `url` (required): Target URL to capture
- `width` (optional): Screenshot width (default: 1280, max: 3840)
- `height` (optional): Screenshot height (default: 720, max: 2160)

**Response:**
- `200 OK`: PNG image
- `400 Bad Request`: Missing URL parameter
- `408 Request Timeout`: Screenshot timeout
- `500 Internal Server Error`: Capture failed
- `503 Service Unavailable`: No workers available

**Headers:**
- `X-Cache`: `HIT` (cached) or `MISS` (fresh capture)
- `Cache-Control`: Caching policy

### 2. Health Check
```
GET /health
```

**Response:**
```json
{
  "status": "healthy",
  "active_requests": 5,
  "total_requests": 1234,
  "failed_requests": 12,
  "timeout_requests": 3,
  "available_workers": 15,
  "max_workers": 20
}
```

**Status Codes:**
- `200 OK`: System healthy
- `429 Too Many Requests`: All workers busy (degraded)

## Monitoring

### Log Output
The service logs every 30 seconds:
```
webshot initialized with 20 Chrome workers, cache: true (5m0s)
Stats: Active=5, Total=1234, Failed=12, Timeouts=3, Workers=20
```

### Health Monitoring
Set up monitoring to check `/health` endpoint:
```bash
curl http://localhost:8080/health
```

Alert if:
- `available_workers` = 0 (system overloaded)
- `failed_requests` increasing rapidly
- Status code = 429

## Performance Optimization

### 1. Enable Load Balancer (for >500 req/s)
Run multiple instances behind a load balancer:
```bash
docker-compose up --scale shotlink=3
```

### 2. Reduce Screenshot Timeout
If pages load fast:
```bash
SCREENSHOT_TIMEOUT=30
```

### 3. Increase Cache Duration
For frequently requested URLs:
```bash
CACHE_DURATION_SECONDS=600  # 10 minutes
```

### 4. Monitor and Scale
Watch the metrics:
- If `available_workers` frequently drops to 0: increase `MAX_CHROME_WORKERS`
- If `timeout_requests` is high: increase `SCREENSHOT_TIMEOUT`
- If memory usage is high: reduce `MAX_CHROME_WORKERS`

## Troubleshooting

### Issue: "Server busy" errors (503)
**Cause**: All workers occupied  
**Solution**: 
```bash
MAX_CHROME_WORKERS=30  # Increase workers
WORKER_TIMEOUT=20      # Increase wait time
```

### Issue: "Context deadline exceeded" errors
**Cause**: Pages loading slowly  
**Solution**:
```bash
SCREENSHOT_TIMEOUT=60  # Increase timeout
```

### Issue: High memory usage
**Cause**: Too many Chrome instances  
**Solution**:
```bash
MAX_CHROME_WORKERS=15  # Reduce workers
```
Also ensure proper memory limits in Docker.

### Issue: Chrome crashes (fork/exec errors)
**Cause**: Resource exhaustion  
**Solution**:
- Increase Docker memory: `--memory=8g`
- Increase shared memory: `--shm-size=1g`
- Reduce workers: `MAX_CHROME_WORKERS=15`

## Production Checklist

- [ ] Set appropriate resource limits (CPU/Memory)
- [ ] Configure shared memory size (`shm_size`)
- [ ] Enable health check monitoring
- [ ] Set up log aggregation
- [ ] Configure alerts for worker exhaustion
- [ ] Test with load testing tools (e.g., Apache Bench)
- [ ] Set up auto-scaling based on `/health` metrics
- [ ] Configure reverse proxy with rate limiting

## Load Testing

Test with Apache Bench:
```bash
# Test 500 requests with 50 concurrent
ab -n 500 -c 50 "http://localhost:8080/get?url=https://example.com"
```

Monitor during test:
```bash
watch -n 1 'curl -s http://localhost:8080/health | jq'
```

## Graceful Shutdown

The service handles SIGTERM/SIGINT properly:
```bash
docker stop webshot  # Waits for ongoing requests to complete
```

All Chrome workers are cleaned up on shutdown.

---

**Created by:** [Abhyuday Patel](https://github.com/abhyudaypatel)  
**Last Updated:** December 19, 2025
