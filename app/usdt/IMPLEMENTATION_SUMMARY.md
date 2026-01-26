# USDT Implementation Summary

## Overview

Successfully implemented a USDT (User Statically-Defined Tracing) instrumentation PoC for the FOSDEM 2026 Go instrumentation comparison project using the `salp` library.

## Implementation Complete ✅

### Files Created

1. **`app/usdt/main.go`** (4.8 KB)
   - Go application with USDT probes using salp library
   - Two probe points: `request_start` and `request_end`
   - Tracks request IDs, timestamps, and durations
   - Based on existing app structure for consistency

2. **`app/usdt/Dockerfile`** (766 bytes)
   - Builds Go app with libstapsdt dependencies
   - Installs libstapsdt from source (GitHub)
   - CGO-enabled build for C library integration

3. **`app/usdt/trace.bt`** (1.5 KB)
   - bpftrace script for consuming USDT probes
   - Calculates request latencies
   - Generates histogram and summary statistics
   - Production-ready logging format

4. **`app/usdt/README.md`** (4.7 KB)
   - Comprehensive documentation
   - Architecture overview with diagram
   - Usage instructions
   - Comparison with other approaches
   - Limitations and references

5. **`app/usdt/TESTING.md`** (5.5 KB)
   - Complete testing guide
   - Pre-flight checklist
   - Step-by-step manual testing procedures
   - Validation criteria
   - Troubleshooting section

6. **`app/usdt/binaries/`** (directory)
   - Created for optional pre-built binaries

### Code Changes

1. **`cmd/test.go`** - Updated test infrastructure
   - Added `usdt` to `allScenarios` slice
   - Added `go-usdt` to `containerNames` slice
   - Implemented `setupUSDTEnvironment()` function
   - Modified container startup logic to handle usdt scenario

2. **`cmd/run.go`** - Updated CLI
   - Added usdt to scenario description in help text

3. **`CLAUDE.md`** - Updated project documentation
   - Added usdt to available scenarios list
   - Added usdt row to instrumentation approaches table

## Library Selection

**Selected: `mmcshane/salp`**

### Rationale

- ✅ Linux-native (project runs in Docker on Linux)
- ✅ Proper Go module support (go.mod present)
- ✅ Uses libstapsdt (SystemTap-compatible ELF notes)
- ✅ Compatible with bpftrace, bcc, and eBPF tools
- ✅ Zero overhead when probes not attached
- ✅ Most stars (33) among viable options
- ✅ Active maintenance

### Rejected Alternatives

- ❌ `ecin/go-dtrace`: macOS only, no go.mod
- ❌ `jen20/go-usdt`: macOS/SmartOS only, no Linux support
- ❌ `gcjenkinson/go-usdt`: Fork of jen20 with no improvements

## Architecture

```
┌─────────────────────────────────┐
│   App Container (usdt)          │
│   ┌─────────────────────────┐   │
│   │ Go App + salp probes    │   │
│   │ (fosdem:request_start)  │   │
│   │ (fosdem:request_end)    │   │
│   └─────────────────────────┘   │
└─────────────────────────────────┘
              │
              │ PID namespace sharing
              │ (--pid=container:usdt)
              │
┌─────────────────────────────────┐
│  Tracer Container (go-usdt)     │
│   ┌─────────────────────────┐   │
│   │ bpftrace                │   │
│   │ - Attaches to probes    │   │
│   │ - Collects trace data   │   │
│   │ - Exports metrics       │   │
│   └─────────────────────────┘   │
└─────────────────────────────────┘
```

## Key Features

1. **Zero Overhead When Not Tracing**
   - Probes check `Enabled()` before firing
   - Near-zero cost when bpftrace not attached
   - No constant overhead like always-on instrumentation

2. **Dynamic Attachment**
   - Can attach/detach bpftrace without restarting app
   - Surgical precision for specific code paths
   - No code recompilation needed

3. **Rich Data Collection**
   - Request IDs for correlation
   - Timestamps for latency calculation
   - Duration metrics computed in kernel space

4. **Standard Tooling**
   - Uses standard bpftrace syntax
   - Compatible with eBPF ecosystem
   - Works with existing observability tools

## Probe Points

### fosdem:request_start

- **When**: Beginning of each HTTP request
- **Arguments**:
    - `arg0` (string): Request ID
    - `arg1` (int64): Timestamp (nanoseconds)

### fosdem:request_end

- **When**: End of each HTTP request
- **Arguments**:
    - `arg0` (string): Request ID
    - `arg1` (int64): Start timestamp
    - `arg2` (int64): Duration (nanoseconds)

## Integration with Existing Infrastructure

The USDT implementation follows the established patterns:

1. **Test Infrastructure** (`cmd/test.go`)
   - Similar to `setupEBPFEnvironment()` and `setupOBIEnvironment()`
   - Starts app container first
   - Launches tracer sidecar with PID namespace sharing
   - Requires `--privileged` flag like other eBPF approaches

2. **CLI** (`cmd/run.go`)
   - Added to scenario list
   - Works with `--scenario usdt` flag
   - Integrates with `--num` for repeated runs

3. **Docker Build**
   - Follows existing Dockerfile patterns
   - Uses same base image and build args
   - Consistent with other scenarios

## Verification

### Code Quality ✅

- Go code compiles without errors
- No linter errors in updated files
- CLI help displays usdt scenario correctly

### Integration ✅

- `usdt` appears in `--scenario all`
- Test infrastructure updated
- Documentation complete

### Pending (Requires Docker Daemon)

- Docker image build
- Runtime probe verification
- End-to-end trace collection
- Benchmarking against other scenarios

## Usage

```bash
# Run USDT scenario
go run . run --scenario usdt --num 5

# Run all scenarios including USDT
go run . run --scenario all

# Build just the USDT image
docker build -t usdt --build-arg runtime_version=1.23 -f app/usdt/Dockerfile .
```

## Next Steps

To fully validate the implementation:

1. Build Docker image in environment with Docker daemon
2. Run application and verify USDT probes are present
3. Attach bpftrace and verify probes fire
4. Generate load and collect trace data
5. Compare performance with other scenarios
6. Integrate with presentation slides

See `TESTING.md` for detailed testing procedures.

## Benefits for FOSDEM Talk

The USDT approach adds a unique perspective:

- **Low overhead**: Near-zero when not tracing
- **Dynamic**: No restart required
- **Flexible**: Custom probe points
- **Powerful**: Full eBPF/bpftrace capabilities
- **Educational**: Shows kernel-userspace interaction

This complements the other approaches nicely:

- Manual OTel: Always-on, high-level spans
- eBPF Auto: Automatic, limited customization
- OBI: eBPF-based, network-focused
- Orchestrion: Compile-time, comprehensive
- USDT: Runtime, surgical precision

## References

- salp library: <https://github.com/mmcshane/salp>
- libstapsdt: <https://github.com/sthima/libstapsdt>
- bpftrace: <https://github.com/iovisor/bpftrace>
- USDT documentation: <https://lwn.net/Articles/753601/>
