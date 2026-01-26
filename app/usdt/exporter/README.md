# BPFTrace to OpenTelemetry Exporter

This directory contains a custom bridge that converts bpftrace USDT probe events into OpenTelemetry traces.

## Architecture

```
┌─────────────────────┐
│   Go App (usdt)     │
│   USDT Probes       │
│   - request_start   │
│   - request_end     │
└──────────┬──────────┘
           │
           │ uprobes attach
           │
┌──────────▼──────────┐
│   bpftrace          │
│   (trace-json.bt)   │
│   JSON output       │
└──────────┬──────────┘
           │
           │ stdout
           │
┌──────────▼──────────┐
│  Exporter (Go)      │
│  - Parse JSON       │
│  - Create spans     │
│  - Export OTLP      │
└──────────┬──────────┘
           │
           │ OTLP/HTTP
           │
┌──────────▼──────────┐
│  OTel Collector     │
│  (port 4318)        │
└─────────────────────┘
```

## Components

### 1. trace-json.bt

bpftrace script that:

- Attaches to `fosdem:request_start` and `fosdem:request_end` USDT probes
- Outputs events as JSON to stdout
- Runs with `-f json` flag for structured output

### 2. main.go

Go program that:

- Spawns bpftrace process
- Reads JSON events from stdout
- Converts events to OTel span lifecycle:
    - `request_start` → creates span with start time
    - `request_end` → ends span with duration
- Exports spans via OTLP to OTel Collector

### 3. Dockerfile

Multi-stage build that:

- Builds Go exporter binary (stage 1)
- Uses bpftrace base image (stage 2)
- Combines exporter + bpftrace in one container

## Why This Approach?

**No existing tool** converts bpftrace output to OpenTelemetry traces:

- `bpftrace_exporter` → Prometheus metrics only
- `ebpf-userspace-exporter` → USDT to Prometheus
- OBI (OpenTelemetry eBPF Instrumentation) → Different approach, not USDT-based

Our custom exporter fills this gap.

## Environment Variables

- `OTEL_EXPORTER_OTLP_ENDPOINT` - OTel Collector endpoint (default: `otel-collector:4318`)
- `TARGET_PID` - PID of process to trace (default: `1` for shared PID namespace)
- `BPFTRACE_SCRIPT` - Path to bpftrace script (default: `/app/trace-json.bt`)

## Building

```bash
cd app/usdt
docker build -t usdt-exporter -f exporter/Dockerfile .
```

## Running

The exporter runs as a sidecar container:

```bash
# Start the USDT-instrumented app
docker run -d --name usdt-app -p 8080:8080 usdt /app/inputs.json

# Start the exporter in the same PID namespace
docker run --privileged --pid=container:usdt-app \
  --network=fosdem2026 \
  -e OTEL_EXPORTER_OTLP_ENDPOINT=otel-collector:4318 \
  usdt-exporter
```

## Requirements

- Linux kernel 4.14+ (for USDT/uprobe support)
- Privileged container (for eBPF)
- Shared PID namespace with target app
- OTel Collector running and accessible

## How It Works

1. **USDT Probe Definition** (in app):

   ```go
   reqStart.Fire(reqID, startTime)
   reqEnd.Fire(reqID, startTime, duration)
   ```

2. **bpftrace Capture** (trace-json.bt):

   ```
   usdt::fosdem:request_start {
       printf("{\"event\":\"request_start\",\"reqid\":\"%s\",\"timestamp\":%lld}\n",
              str(arg0), arg1);
   }
   ```

3. **JSON Event** (stdout):

   ```json
   {"event":"request_start","reqid":"req-123","timestamp":1706285000000000000}
   ```

4. **Go Exporter Processing**:
   - Parse JSON
   - Create OTel span with start timestamp
   - Store in activeSpans map by reqID

5. **Span Completion**:

   ```json
   {"event":"request_end","reqid":"req-123","start":1706285000000000000,"duration":2500000}
   ```

   - Find span by reqID
   - Add duration attribute
   - End span with calculated end time

6. **OTLP Export**:
   - Batch spans
   - Send to OTel Collector via HTTP
   - Collector forwards to Jaeger/Prometheus

## Limitations

- Requires privileged container (standard eBPF limitation)
- Spans must complete in order (stateful matching by reqID)
- No support for distributed tracing context (could be added)

## Future Enhancements

1. **Distributed Tracing**: Extract trace context from HTTP headers
2. **Span Attributes**: Add more attributes from probe arguments
3. **Error Handling**: Detect failed requests and mark spans accordingly
4. **Metrics**: Export RED metrics alongside traces
5. **Multiple Probes**: Support additional custom probes beyond request_start/end
