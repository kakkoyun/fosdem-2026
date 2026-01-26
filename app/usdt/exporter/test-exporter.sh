#!/bin/bash
set -e

echo "=== Testing USDT Exporter in Isolation ==="
echo

# Configuration
COLLECTOR_ENDPOINT="${OTEL_EXPORTER_OTLP_ENDPOINT:-localhost:4318}"
TEST_DATA="test-data.json"

echo "1. Checking if OTel Collector is running..."
if ! curl -s http://${COLLECTOR_ENDPOINT}/v1/traces > /dev/null 2>&1; then
    echo "⚠️  Warning: OTel Collector may not be running at ${COLLECTOR_ENDPOINT}"
    echo "   Start it with: docker-compose up -d otel-collector"
    echo "   Or set OTEL_EXPORTER_OTLP_ENDPOINT to point to your collector"
    echo
fi

echo "2. Building exporter..."
go build -o bpftrace-exporter . || {
    echo "❌ Build failed"
    exit 1
}
echo "✅ Build successful"
echo

echo "3. Test data:"
cat ${TEST_DATA}
echo
echo "Expected: 4 request_start/end pairs → 4 spans"
echo

echo "4. Running exporter with test data..."
echo "   (Exporter will read from stdin instead of running bpftrace)"
echo

# Create a modified version of the exporter for testing
cat > test-main.go <<'EOF'
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	oteltrace "go.opentelemetry.io/otel/trace"
)

type SpanContext struct {
	Span      oteltrace.Span
	StartTime time.Time
}

var (
	tracer       oteltrace.Tracer
	activeSpans  = make(map[string]*SpanContext)
	otelEndpoint string
)

func init() {
	otelEndpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if otelEndpoint == "" {
		otelEndpoint = "localhost:4318"
	}
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	shutdown, err := initTracer(ctx)
	if err != nil {
		log.Fatalf("Failed to initialize tracer: %v", err)
	}
	defer func() {
		log.Println("Shutting down tracer...")
		if err := shutdown(ctx); err != nil {
			log.Printf("Error shutting down tracer: %v", err)
		}
		log.Println("Tracer shutdown complete")
	}()

	tracer = otel.Tracer("bpftrace-exporter-test")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("Received shutdown signal")
		cancel()
	}()

	log.Printf("Reading test data from stdin...")
	log.Printf("OTel endpoint: %s", otelEndpoint)

	scanner := bufio.NewScanner(os.Stdin)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if line == "" {
			continue
		}

		log.Printf("Processing line %d: %s", lineNum, line)
		if err := processEvent(ctx, line); err != nil {
			log.Printf("❌ Error processing line %d: %v", lineNum, err)
		}
	}

	if err := scanner.Err(); err != nil {
		log.Fatalf("Error reading input: %v", err)
	}

	log.Printf("✅ Processed %d lines successfully", lineNum)
	log.Printf("📊 Active spans remaining: %d (should be 0)", len(activeSpans))
	
	// Give time for spans to be exported
	log.Println("Waiting 2 seconds for spans to be exported...")
	time.Sleep(2 * time.Second)
}

func initTracer(ctx context.Context) (func(context.Context) error, error) {
	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithInsecure(),
		otlptracehttp.WithEndpoint(otelEndpoint),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create OTLP exporter: %w", err)
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName("usdt-bpftrace-exporter-test"),
			semconv.ServiceVersion("1.0.0"),
			attribute.String("exporter.type", "bpftrace"),
			attribute.String("test.mode", "isolated"),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	tp := trace.NewTracerProvider(
		trace.WithBatcher(exporter),
		trace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	return tp.Shutdown, nil
}

func processEvent(ctx context.Context, jsonLine string) error {
	var event map[string]interface{}
	if err := json.Unmarshal([]byte(jsonLine), &event); err != nil {
		return fmt.Errorf("failed to parse JSON: %w", err)
	}

	eventType, ok := event["event"].(string)
	if !ok {
		return fmt.Errorf("missing or invalid event type")
	}

	reqID, ok := event["reqid"].(string)
	if !ok {
		return fmt.Errorf("missing or invalid request ID")
	}

	switch eventType {
	case "request_start":
		return handleRequestStart(ctx, reqID, event)
	case "request_end":
		return handleRequestEnd(reqID, event)
	default:
		return fmt.Errorf("unknown event type: %s", eventType)
	}
}

func handleRequestStart(ctx context.Context, reqID string, event map[string]interface{}) error {
	timestamp, ok := event["timestamp"].(float64)
	if !ok {
		return fmt.Errorf("missing timestamp")
	}

	startTime := time.Unix(0, int64(timestamp))

	_, span := tracer.Start(ctx, "http.request",
		oteltrace.WithTimestamp(startTime),
		oteltrace.WithAttributes(
			attribute.String("request.id", reqID),
			attribute.String("span.kind", "server"),
		),
	)

	activeSpans[reqID] = &SpanContext{
		Span:      span,
		StartTime: startTime,
	}

	log.Printf("✅ Started span for request: %s at %s", reqID, startTime.Format(time.RFC3339Nano))
	return nil
}

func handleRequestEnd(reqID string, event map[string]interface{}) error {
	spanCtx, ok := activeSpans[reqID]
	if !ok {
		return fmt.Errorf("no active span found for request: %s", reqID)
	}

	duration, ok := event["duration"].(float64)
	if !ok {
		return fmt.Errorf("missing duration")
	}

	endTime := spanCtx.StartTime.Add(time.Duration(duration))

	spanCtx.Span.SetAttributes(
		attribute.Int64("duration_ns", int64(duration)),
		attribute.Float64("duration_ms", duration/1e6),
	)

	spanCtx.Span.End(oteltrace.WithTimestamp(endTime))

	delete(activeSpans, reqID)

	log.Printf("✅ Ended span for request: %s (duration: %.2fms)", reqID, duration/1e6)
	return nil
}
EOF

echo "5. Building test exporter..."
go build -o bpftrace-exporter-test test-main.go || {
    echo "❌ Test build failed"
    exit 1
}

echo "6. Running test..."
cat ${TEST_DATA} | ./bpftrace-exporter-test

echo
echo "=== Test Complete ==="
echo
echo "Next steps to verify:"
echo "1. Check Jaeger UI: http://localhost:16686"
echo "   - Service: usdt-bpftrace-exporter-test"
echo "   - Should see 4 spans (req-001, req-002, req-003, req-004)"
echo
echo "2. Check OTel Collector logs:"
echo "   docker logs <otel-collector-container>"
echo
echo "3. Verify span attributes:"
echo "   - request.id should match req-XXX"
echo "   - duration_ms should match expected values"
echo "   - Timestamps should be correct"

# Cleanup
rm -f test-main.go bpftrace-exporter-test
