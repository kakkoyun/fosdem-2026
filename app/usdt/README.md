# USDT Instrumentation Demo

This directory contains a Go application instrumented with USDT (User Statically-Defined Tracing) probes using the `salp` library.

## Overview

USDT probes provide a way to add zero-overhead tracepoints to applications that can be attached to at runtime using tools like bpftrace, systemtap, or other eBPF-based tracers.

## Architecture

The USDT approach uses two components:

1. **Application Container** (`usdt`): Go app with embedded USDT probes via salp library
2. **Tracer Container** (`go-usdt`): bpftrace process that attaches to the probes

```
┌─────────────────────────┐
│  App Container (usdt)   │
│  ┌──────────────────┐   │
│  │ Go Application   │   │
│  │ with salp probes │   │
│  └──────────────────┘   │
└─────────────────────────┘
           │
           │ PID namespace sharing
           │
┌─────────────────────────┐
│ Tracer (go-usdt)        │
│  ┌──────────────────┐   │
│  │ bpftrace         │   │
│  │ (reads probes)   │   │
│  └──────────────────┘   │
└─────────────────────────┘
```

## Implementation Details

### Probe Points

The application defines two USDT probes:

- **`fosdem:request_start`**: Fires at the beginning of each request
    - Arguments: request_id (string), timestamp (int64)

- **`fosdem:request_end`**: Fires at the end of each request
    - Arguments: request_id (string), start_timestamp (int64), duration (int64)

### Key Features

1. **Zero overhead when not tracing**: Probes only fire when bpftrace is attached
2. **Dynamic attachment**: No need to restart the application to enable/disable tracing
3. **Rich data collection**: Can capture request IDs, timestamps, and durations
4. **Compatible with eBPF tools**: Works with bpftrace, bcc, systemtap

## Files

- `main.go`: Application with USDT probes
- `Dockerfile`: Builds the app with libstapsdt dependency
- `trace.bt`: bpftrace script for consuming the probes
- `README.md`: This file

## Usage

### Building

```bash
docker build -t usdt --build-arg runtime_version=1.23 -f app/usdt/Dockerfile .
```

### Running with bpftrace

The test infrastructure (`cmd/test.go`) handles the orchestration:

```bash
go run . run --scenario usdt --num 5
```

This will:

1. Build and start the USDT-instrumented app
2. Launch a bpftrace sidecar container
3. Attach bpftrace to the USDT probes
4. Generate load and collect trace data
5. Report metrics

### Manual Testing

To manually test the USDT probes:

1. Start the app container:

   ```bash
   docker run --name usdt-app -p 8080:8080 usdt /app/inputs.json
   ```

2. In another terminal, attach bpftrace:

   ```bash
   docker run --privileged --pid=container:usdt-app \
     -v $(pwd)/app/usdt/trace.bt:/app/trace.bt:ro \
     quay.io/iovisor/bpftrace:latest /app/trace.bt -p 1
   ```

3. Generate load:

   ```bash
   curl http://localhost:8080/load
   ```

4. Observe trace output from bpftrace showing request start/end events and latencies

## Dependencies

- **libstapsdt**: C library for creating USDT probes dynamically
- **salp**: Go wrapper around libstapsdt
- **bpftrace**: Tool for attaching to and reading USDT probes

## Comparison with Other Approaches

| Approach | Overhead | Flexibility | Ease of Use |
|----------|----------|-------------|-------------|
| USDT | Near-zero when not attached | High - can attach/detach anytime | Medium - requires bpftrace |
| Manual OTel | Constant (always on) | Medium - needs code changes | High - standard tooling |
| eBPF Auto | Low | Low - limited customization | High - automatic |
| OBI | Low | Medium | High - automatic |
| Orchestrion | Low | Medium - compile-time | High - automatic |

USDT shines when you need:

- Surgical precision for specific code paths
- Dynamic enable/disable without restart
- Minimal overhead when not actively tracing
- Custom trace points in your application

## Limitations

1. Requires privileged container for bpftrace
2. Linux-only (uses libstapsdt)
3. Needs kernel support for uprobes (kernel 4.14+)
4. Manual probe point placement in code

## References

- [salp library](https://github.com/mmcshane/salp)
- [libstapsdt](https://github.com/sthima/libstapsdt)
- [bpftrace](https://github.com/iovisor/bpftrace)
- [USDT documentation](https://lwn.net/Articles/753601/)
