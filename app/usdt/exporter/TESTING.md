# Testing the OTel Exporter

This guide explains how to test the bpftrace-to-OTel exporter in isolation, without requiring USDT probes or bpftrace.

## Quick Test

```bash
cd app/usdt/exporter
./test-exporter.sh
```

This will:
1. Build the test version of the exporter
2. Feed it dummy bpftrace JSON events
3. Export spans to the OTel Collector
4. Report success/failure

## Prerequisites

### 1. OTel Collector Running

Start the collector if not already running:

```bash
# From project root
docker-compose up -d otel-collector jaeger
```

Verify it's accessible:

```bash
curl http://localhost:4318/v1/traces
# Should return: 404 or method not allowed (means endpoint exists)
```

### 2. Go Environment

```bash
cd app/usdt/exporter
go mod init exporter
go mod tidy
```

## Test Data

The test uses `test-data.json` which contains 4 request pairs:

```json
{"event":"request_start","reqid":"req-001","timestamp":1706285000000000000}
{"event":"request_end","reqid":"req-001","start":1706285000000000000,"duration":2500000}
{"event":"request_start","reqid":"req-002","timestamp":1706285001000000000}
{"event":"request_start","reqid":"req-003","timestamp":1706285001500000000}
{"event":"request_end","reqid":"req-002","start":1706285001000000000,"duration":5000000}
{"event":"request_end","reqid":"req-003","start":1706285001500000000,"duration":1000000}
{"event":"request_start","reqid":"req-004","timestamp":1706285002000000000}
{"event":"request_end","reqid":"req-004","start":1706285002000000000,"duration":10000000}
```

**Expected Results**:
- 4 spans created (req-001, req-002, req-003, req-004)
- Durations: 2.5ms, 5ms, 1ms, 10ms
- All spans should appear in Jaeger

## Manual Testing

### Step 1: Start Infrastructure

```bash
docker-compose up -d otel-collector jaeger
```

### Step 2: Build Test Exporter

```bash
cd app/usdt/exporter
go build -o bpftrace-exporter-test test-main.go
```

### Step 3: Run Test

```bash
cat test-data.json | ./bpftrace-exporter-test
```

**Expected Output**:
```
Processing line 1: {"event":"request_start","reqid":"req-001","timestamp":1706285000000000000}
✅ Started span for request: req-001 at 2024-01-26T...
Processing line 2: {"event":"request_end","reqid":"req-001","start":1706285000000000000,"duration":2500000}
✅ Ended span for request: req-001 (duration: 2.50ms)
...
✅ Processed 8 lines successfully
📊 Active spans remaining: 0 (should be 0)
Waiting 2 seconds for spans to be exported...
Shutting down tracer...
```

### Step 4: Verify in Jaeger

1. Open Jaeger UI: http://localhost:16686
2. Select service: `usdt-bpftrace-exporter-test`
3. Click "Find Traces"
4. Should see 4 traces

**Verify Each Span**:
- ✅ Operation name: `http.request`
- ✅ Attribute `request.id`: matches reqid (e.g., `req-001`)
- ✅ Attribute `duration_ms`: matches expected duration
- ✅ Attribute `span.kind`: `server`
- ✅ Service name: `usdt-bpftrace-exporter-test`

## Troubleshooting

### No spans in Jaeger

**Check OTel Collector logs**:
```bash
docker logs <otel-collector-container-id> 2>&1 | grep -i error
```

**Check if collector is receiving data**:
```bash
docker logs <otel-collector-container-id> 2>&1 | tail -20
```

### Exporter fails to connect

**Error**: `failed to create OTLP exporter: connection refused`

**Solution**: 
- Verify collector is running: `docker ps | grep otel-collector`
- Check endpoint: `export OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4318`
- Try with IP: `export OTEL_EXPORTER_OTLP_ENDPOINT=127.0.0.1:4318`

### JSON parsing errors

**Error**: `failed to parse JSON: invalid character...`

**Solution**: Verify test-data.json has valid JSON (one event per line, no trailing commas)

### Missing spans (active spans > 0)

**Issue**: Some request_end events missing or mismatched reqids

**Check**: Ensure every request_start has corresponding request_end with same reqid

## Testing Custom Events

Create your own test data:

```bash
cat > custom-test.json <<EOF
{"event":"request_start","reqid":"test-1","timestamp":1706285000000000000}
{"event":"request_end","reqid":"test-1","start":1706285000000000000,"duration":3000000}
EOF

cat custom-test.json | OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4318 ./bpftrace-exporter-test
```

## Automated Validation

Run the full test suite:

```bash
./test-exporter.sh
```

Then verify programmatically:

```bash
# Query Jaeger API
curl 'http://localhost:16686/api/traces?service=usdt-bpftrace-exporter-test&limit=10'
```

Expected: JSON response with 4 traces, each containing:
- `traceID`
- `spans` array with 1 span
- Span attributes matching our test data

## Success Criteria

- [x] Exporter builds without errors
- [x] Parses all 8 JSON lines successfully
- [x] Creates 4 spans (all request_start/end pairs matched)
- [x] No active spans remain (all properly closed)
- [x] Spans exported to OTel Collector
- [x] Spans visible in Jaeger UI
- [x] Span attributes correct (request.id, duration_ms)
- [x] Timestamps accurate

## Next Steps

Once isolated testing passes:

1. **Test with real bpftrace**: Run against actual USDT probes
2. **Integration test**: Full stack (app → bpftrace → exporter → collector)
3. **Performance test**: High-volume events (1000+ requests/sec)
4. **Error handling**: Test malformed JSON, missing fields, out-of-order events
