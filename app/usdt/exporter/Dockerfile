FROM golang:1.23-bookworm AS builder

WORKDIR /build

# Initialize Go module
RUN go mod init exporter

# Copy source
COPY exporter/main.go ./

# Download dependencies
RUN go mod tidy

# Build the exporter
RUN CGO_ENABLED=0 go build -o bpftrace-exporter .

# Final image with bpftrace
FROM quay.io/iovisor/bpftrace:latest

# Copy the exporter binary
COPY --from=builder /build/bpftrace-exporter /usr/local/bin/

# Copy the bpftrace script
COPY exporter/trace-json.bt /app/trace-json.bt

# Set environment defaults
ENV OTEL_EXPORTER_OTLP_ENDPOINT=otel-collector:4318
ENV TARGET_PID=1
ENV BPFTRACE_SCRIPT=/app/trace-json.bt

ENTRYPOINT ["/usr/local/bin/bpftrace-exporter"]
