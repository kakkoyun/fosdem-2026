// Package main provides a bpftrace to OpenTelemetry exporter bridge.
// It runs bpftrace, captures USDT probe events, and exports them as OTLP traces.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// BPFTraceEvent represents a JSON event from bpftrace output
type BPFTraceEvent struct {
	Type      string                 `json:"type"`
	Data      map[string]interface{} `json:"data"`
	Timestamp int64                  `json:"timestamp_ns"`
}

// SpanContext tracks active spans by request ID
type SpanContext struct {
	Span      oteltrace.Span
	StartTime time.Time
}

var (
	tracer       oteltrace.Tracer
	activeSpans  = make(map[string]*SpanContext)
	otelEndpoint string
	targetPID    string
	bpfScript    string
)

func init() {
	// Get configuration from environment
	otelEndpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if otelEndpoint == "" {
		otelEndpoint = "otel-collector:4318"
	}

	targetPID = os.Getenv("TARGET_PID")
	if targetPID == "" {
		targetPID = "1" // Default to PID 1 in shared namespace
	}

	bpfScript = os.Getenv("BPFTRACE_SCRIPT")
	if bpfScript == "" {
		bpfScript = "/app/trace-json.bt"
	}
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize OpenTelemetry
	shutdown, err := initTracer(ctx)
	if err != nil {
		log.Fatalf("Failed to initialize tracer: %v", err)
	}
	defer func() {
		if err := shutdown(ctx); err != nil {
			log.Printf("Error shutting down tracer: %v", err)
		}
	}()

	tracer = otel.Tracer("bpftrace-exporter")

	// Handle shutdown gracefully
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("Received shutdown signal, cleaning up...")
		cancel()
	}()

	// Run bpftrace and process output
	if err := runBPFTrace(ctx); err != nil {
		log.Fatalf("BPFTrace error: %v", err)
	}

	log.Println("Exporter shutting down")
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
			semconv.ServiceName("usdt-bpftrace-exporter"),
			semconv.ServiceVersion("1.0.0"),
			attribute.String("exporter.type", "bpftrace"),
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

func runBPFTrace(ctx context.Context) error {
	log.Printf("Starting bpftrace with script: %s, target PID: %s", bpfScript, targetPID)

	// Construct bpftrace command with JSON output
	cmd := exec.CommandContext(ctx, "bpftrace", "-f", "json", "-p", targetPID, bpfScript)
	cmd.Stderr = os.Stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start bpftrace: %w", err)
	}

	log.Println("BPFTrace started, processing events...")

	// Process bpftrace JSON output line by line
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		// Parse bpftrace event
		if err := processEvent(ctx, line); err != nil {
			log.Printf("Warning: Failed to process event: %v", err)
			continue
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading bpftrace output: %w", err)
	}

	return cmd.Wait()
}

func processEvent(ctx context.Context, jsonLine string) error {
	// Parse the custom event format from our bpftrace script
	// Expected format: {"event":"request_start","reqid":"...", "timestamp":...}
	//                  {"event":"request_end","reqid":"...", "start":..., "duration":...}

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

	// Create a new span
	_, span := tracer.Start(ctx, "http.request",
		oteltrace.WithTimestamp(startTime),
		oteltrace.WithAttributes(
			attribute.String("request.id", reqID),
			attribute.String("span.kind", "server"),
		),
	)

	// Store the span context
	activeSpans[reqID] = &SpanContext{
		Span:      span,
		StartTime: startTime,
	}

	log.Printf("Started span for request: %s", reqID)
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

	// Add duration as attribute
	spanCtx.Span.SetAttributes(
		attribute.Int64("duration_ns", int64(duration)),
		attribute.Float64("duration_ms", duration/1e6),
	)

	// End the span
	spanCtx.Span.End(oteltrace.WithTimestamp(endTime))

	// Clean up
	delete(activeSpans, reqID)

	log.Printf("Ended span for request: %s (duration: %.2fms)", reqID, duration/1e6)
	return nil
}
