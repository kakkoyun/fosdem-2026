FROM golang:1.22-bookworm AS builder

WORKDIR /build

# Set GOTOOLCHAIN to prevent auto-upgrade
ENV GOTOOLCHAIN=local

# Initialize Go module
RUN go mod init exporter

# Copy source
COPY app/usdt/exporter/main.go ./

# Pin to known-good OTel versions for Go 1.22
RUN go get go.opentelemetry.io/otel@v1.21.0 && \
    go get go.opentelemetry.io/otel/trace@v1.21.0 && \
    go get go.opentelemetry.io/otel/sdk@v1.21.0 && \
    go get go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp@v1.21.0

# Download remaining dependencies
RUN go mod tidy

# Build the exporter
RUN CGO_ENABLED=0 go build -o bpftrace-exporter .

# Final image with bpftrace
FROM quay.io/iovisor/bpftrace:latest

# Copy the exporter binary
COPY --from=builder /build/bpftrace-exporter /usr/local/bin/

# Copy the bpftrace script
COPY app/usdt/exporter/trace-json.bt /app/trace-json.bt

# Set environment defaults
ENV OTEL_EXPORTER_OTLP_ENDPOINT=otel-collector:4318
ENV TARGET_PID=1
ENV BPFTRACE_SCRIPT=/app/trace-json.bt

ENTRYPOINT ["/usr/local/bin/bpftrace-exporter"]
