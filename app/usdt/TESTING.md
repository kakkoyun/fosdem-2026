# USDT Implementation Testing Guide

## Pre-flight Checklist

- [x] Directory structure created (`app/usdt/`)
- [x] Main application with USDT probes (`main.go`)
- [x] Dockerfile with libstapsdt dependencies
- [x] bpftrace script for probe consumption (`trace.bt`)
- [x] Test infrastructure updated (`cmd/test.go`)
- [x] CLI help updated with usdt scenario
- [x] Documentation created (README.md)
- [x] Go code compiles without errors
- [x] No linter errors

## Manual Testing Steps

Since Docker daemon is not available in the development environment, here are the steps to test the USDT implementation in a proper environment:

### 1. Build the Image

```bash
cd /path/to/fosdem-2026-go
docker build -t usdt --build-arg runtime_version=1.23 -f app/usdt/Dockerfile .
```

**Expected**: Image builds successfully with libstapsdt installed

### 2. Create Test Input

```bash
cat > /tmp/inputs.json <<EOF
{
  "port": 8080,
  "off_cpu": 0.0,
  "loops_cpu": 0.0,
  "loops_num": 1000,
  "allocs_cpu": 0.0,
  "allocs_num": 10,
  "alloc_size": 1024,
  "tracing": true,
  "profiling": false,
  "workers": 1,
  "otel_endpoint": "localhost:4318"
}
EOF
```

### 3. Run the Application

```bash
docker run --name usdt-test -p 8080:8080 \
  -v /tmp/inputs.json:/app/inputs.json:ro \
  usdt
```

**Expected**: Application starts and listens on port 8080

### 4. Verify USDT Probes are Present

In another terminal:

```bash
# List USDT probes in the running container
docker exec usdt-test bpftrace -l 'usdt:*fosdem*'
```

**Expected output**:

```
usdt:/app/main:fosdem:request_start
usdt:/app/main:fosdem:request_end
```

### 5. Attach bpftrace

```bash
docker run --privileged --pid=container:usdt-test \
  -v $(pwd)/app/usdt/trace.bt:/app/trace.bt:ro \
  quay.io/iovisor/bpftrace:latest \
  /app/trace.bt -p 1
```

**Expected**: bpftrace attaches successfully and displays:

```
Tracing USDT probes for FOSDEM app...
Provider: fosdem
Hit Ctrl-C to end.
```

### 6. Generate Load

In another terminal:

```bash
# Single request
curl http://localhost:8080/load

# Multiple requests
for i in {1..10}; do curl http://localhost:8080/load; done
```

**Expected**: bpftrace output shows probe firings:

```
start: reqID=req-1234567890 timestamp=1234567890123456789
end: reqID=req-1234567890 start=1234567890123456789 duration=2345678 latency_ms=2
```

### 7. Test via CLI

```bash
# Ensure Docker daemon and docker-compose are running
docker-compose up -d

# Run USDT scenario
go run . run --scenario usdt --num 3
```

**Expected**:

- USDT container builds successfully
- bpftrace sidecar attaches
- Load is generated
- Trace data is collected
- Results are displayed

### 8. Run Full Benchmark

```bash
go run . run --scenario all
```

**Expected**: All scenarios run including usdt, with comparative results

## Validation Criteria

### Code Quality

- [x] Go code follows project conventions
- [x] Dockerfile follows existing patterns
- [x] No linter errors
- [x] Code compiles successfully

### Functionality

- [ ] Docker image builds successfully
- [ ] Application starts without errors
- [ ] USDT probes are visible in process
- [ ] bpftrace can attach to probes
- [ ] Probes fire on request handling
- [ ] Trace data is collected accurately

### Integration

- [x] CLI includes usdt in help text
- [x] Test infrastructure supports usdt scenario
- [x] Documentation is complete
- [ ] End-to-end test passes

## Known Limitations

1. **Requires Linux kernel 4.14+**: USDT probes use uprobes which require modern kernel
2. **Privileged container needed**: bpftrace requires `--privileged` flag
3. **PID namespace sharing**: Tracer must share PID namespace with app
4. **libstapsdt build**: May take longer due to building from source

## Troubleshooting

### "Cannot find USDT probes"

- Verify libstapsdt is installed: `docker exec usdt-test ldconfig -p | grep stapsdt`
- Check if probes are loaded: `docker exec usdt-test cat /proc/self/maps | grep libstapsdt`

### "bpftrace fails to attach"

- Ensure container is running with `--privileged`
- Verify PID namespace sharing: `--pid=container:usdt-test`
- Check kernel version: `uname -r` (should be 4.14+)

### "Probes not firing"

- Verify app is receiving requests: `curl http://localhost:8080/health`
- Check if probes are enabled: Look for "Enabled()" checks in code
- Examine bpftrace output for errors

## Success Criteria

The implementation is considered complete when:

1. ✅ All code is written and compiles
2. ✅ Documentation is complete
3. ⏳ Docker image builds successfully (requires Docker daemon)
4. ⏳ USDT probes are visible and attachable (requires Docker daemon)
5. ⏳ Probes fire and collect data accurately (requires Docker daemon)
6. ⏳ Integration with test infrastructure works (requires Docker daemon)

**Note**: Items marked with ⏳ require a running Docker daemon for verification.
