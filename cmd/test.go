package cmd

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/docker/docker/api/types/container"
	types "github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"
	"github.com/joho/godotenv"
	"golang.org/x/sync/errgroup"
)

var (
	dockerCommand  = "docker-compose"
	dockerClient   *Client
	serverPID      *os.Process
	allScenarios   = []string{"default", "manual", "obi", "ebpf", "orchestrion", "usdt"}
	containerNames = []string{"go-auto", "go-obi", "collector", "go-usdt"}
	networkName    = "fosdem2026"
)

// Many runs multiple test scenarios and returns results.
func Many(ctx context.Context, opts *RunManyOpts) ([]*TestResult, error) {
	log := opts.Logger
	results := []*TestResult{}

	// Stop and clean up environment
	if opts.Scenario[0] == "stop" {
		err := cleanup(log)
		return nil, err
	}

	if opts.Scenario[0] == "all" {
		opts.Scenario = allScenarios
	}

	err := setupEnvironment(ctx, opts)
	if err != nil {
		log.Debug("Failed to setup environment", "error", err)
		return nil, err
	}
	for _, s := range opts.Scenario {
		log.Info("Running scenario", "scenario", s)
		failures := 0
		for i := range opts.Num {
			log.Info("Running test run", "run", i+1, "of", opts.Num)
			r, err := runOne(ctx, opts, s)
			if err != nil {
				log.Warn("⚠️ Test run failed", "error", err)
				failures++
				continue
			}
			results = append(results, r)
		}
		log.Info("Scenario completed", "scenario", s, "failures", failures)
	}
	return results, nil
}

func runOne(ctx context.Context, opts *RunManyOpts, scenario string) (*TestResult, error) {
	log := opts.Logger
	log.Info("Starting test run")
	start := time.Now()
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	out := &TestResult{
		Start:      start,
		RunnerOS:   runtime.GOOS,
		RunnerArch: runtime.GOARCH,
		RunnerCPU:  runtime.NumCPU(),
	}
	inputs := opts.Inputs

	cleanupFunctions := []func(container.StopOptions) error{}
	cleanup, err := buildGoEnvironment(ctx, opts, scenario)
	if err != nil {
		log.Debug("Failed to build Go environment", "error", err)
		return nil, err
	}
	if scenario != "default" {
		err := setupOTelEnvironment(ctx, opts)
		if err != nil {
			return nil, err
		}
	}
	if scenario == "obi" {
		cleanupOBI, err := setupOBIEnvironment(ctx, opts)
		if err != nil {
			return nil, err
		}
		cleanupFunctions = append(cleanupFunctions, cleanupOBI)
	}
	if scenario == "ebpf" {
		cleanupEBPF, err := setupEBPFEnvironment(ctx, opts)
		if err != nil {
			return nil, err
		}
		cleanupFunctions = append(cleanupFunctions, cleanupEBPF)
	}
	if scenario == "usdt" {
		cleanupUSDT, err := setupUSDTEnvironment(ctx, opts)
		if err != nil {
			return nil, err
		}
		cleanupFunctions = append(cleanupFunctions, cleanupUSDT)
	}
	cleanupFunctions = append(cleanupFunctions, cleanup)

	log.Info("✅ app build done")
	appStop := make(chan struct{}, 1)
	go func() {
		resCh, errCh := dockerClient.ContainerWait(ctx, scenario, container.WaitConditionNotRunning)
		select {
		case <-appStop:
			log.Debug("🖥 ️ app container stopped")
		case <-ctx.Done():
			log.Debug("🖥️  app container context done", "err", ctx.Err())
		case res := <-resCh:
			cancel(fmt.Errorf("app container exited unexpectedly with status %d", res.StatusCode))
		case err := <-errCh:
			cancel(fmt.Errorf("app container wait failure: %w", err))
		}
	}()

	// Wait for the app server to be healthy
	log.Info("⌛ app server waiting to be healthy")
	waitStart := time.Now()
	if err := waitForAppHealth(ctx, inputs.Port, "health"); err != nil {
		log.Debug("Timed out waiting for app server to be healthy", "error", err)
		return nil, err
	}
	log.Info("✅ app server is healthy", "duration", time.Since(waitStart))
	out.AppReady = time.Now()

	// generate load
	out.LoadStart = time.Now()
	stats := startStats(ctx, scenario)

	// Send requests
	client := &http.Client{
		Timeout: time.Duration(inputs.Timeout * 1e9),
		Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			TLSClientConfig:     &tls.Config{InsecureSkipVerify: false},
			MaxIdleConnsPerHost: inputs.RPS,
		},
	}
	requests, err := Generate(ctx, &Config{
		Client:      client,
		Log:         log,
		URL:         fmt.Sprintf("http://localhost:%d/load", inputs.Port),
		RPS:         inputs.RPS,
		Clients:     inputs.Clients,
		Duration:    inputs.Duration,
		ExpectError: inputs.Exceptions,
		Endpoints:   1,
	})
	if err != nil {
		log.Debug("Failed to generate requests", "error", err)
		return nil, err
	}
	out.Requests = requests

	out.LoadEnd = time.Now()
	out.LoadStats, err = stats()
	if err != nil {
		log.Debug("Failed to get load stats", "error", err)
		return nil, err
	}
	time.Sleep(5 * time.Second) // wait for the app to finish processing

	stopStats := startStats(ctx, scenario)

	out.StopStart = time.Now()
	signal := ""
	if !inputs.Flush {
		signal = "SIGKILL"
	}
	appStop <- struct{}{}
	for _, cleanupFunc := range cleanupFunctions {
		err = cleanupFunc(container.StopOptions{Signal: signal})
		if err != nil {
			log.Debug("Failed to cleanup", "error", err)
			return nil, err
		}
	}
	out.StopEnd = time.Now()

	out.StopStats, err = stopStats()
	if err != nil {
		log.Debug("Failed to get end stats", "error", err)
		return nil, err
	}
	return out, nil
}

func waitForAppHealth(ctx context.Context, port int, endpoint string) error {
	healthCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	for {
		select {
		case <-healthCtx.Done():
			return context.Cause(healthCtx)
		case <-time.After(100 * time.Millisecond):
		}

		if healthy, _ := httpHealthCheck(healthCtx, port, endpoint); healthy {
			break
		}
	}
	return nil
}

func httpHealthCheck(ctx context.Context, port int, endpoint string) (bool, error) {
	client := &http.Client{
		Timeout: 1 * time.Second,
	}
	url := fmt.Sprintf("http://localhost:%d/%s", port, endpoint)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()

	return resp.StatusCode == http.StatusOK, nil
}

func setupEnvironment(ctx context.Context, opts *RunManyOpts) error {
	log := opts.Logger

	// Check if Docker is installed
	err := exec.Command("docker", "compose", "version").Run()
	if cmdExists("docker-compose") {
		dockerCommand = "docker-compose"
	} else if err == nil {
		dockerCommand = "docker compose"
	} else {
		return errors.New("neither docker-compose nor docker compose was found")
	}

	dockerClient, err = NewClient(ctx)
	if err != nil {
		log.Debug("Failed to create Docker client", "error", err)
		return err
	}

	if err := checkNetwork(ctx, opts); err != nil {
		return err
	}

	// Setup Docker services
	if err := run(dockerCommand, "up", "-d", "--remove-orphans"); err != nil {
		return err
	}
	time.Sleep(3 * time.Second)
	_ = run(dockerCommand, "ps") // best-effort status display
	log.Info("✅ Services started!")
	log.Info("   - Grafana: http://localhost:3000")
	log.Info("   - Jaeger: http://localhost:16686")
	log.Info("   - Prometheus: http://localhost:9090")

	log.Info("✅ Docker environment setup complete")
	return nil
}

func buildGoEnvironment(ctx context.Context, opts *RunManyOpts, scenario string) (func(container.StopOptions) error, error) {
	// Build the Go application
	log := opts.Logger
	buildArgs := map[string]string{}

	// First, load all variables from .env file
	envFile := filepath.Join(".env")
	if envBuildArgs, err := godotenv.Read(envFile); err == nil {
		for k, v := range envBuildArgs {
			buildArgs[k] = v
		}
	}

	if opts.Inputs.RuntimeVersion != "" {
		buildArgs["runtime_version"] = opts.Inputs.RuntimeVersion
	} else {
		buildArgs["runtime_version"] = "1.25.5"
		log.Warn("no runtime version specified, using default", "version", "1.25.5")
	}

	// Build the Dockerfile for the given scenario
	build := &BuildOpts{
		Dir:     filepath.Join(getRoot(), "app", scenario),
		Args:    buildArgs,
		Secrets: map[string]string{},
	}
	var eg errgroup.Group
	eg.Go(func() error {
		log.Info("⌛ image build starting", "scenario", scenario)
		start := time.Now()
		cmdLog := log.With("scenario", scenario)
		buildCmd := dockerClient.BuildCommand(ctx, build, scenario)
		buildCmd.Stdout = os.Stdout
		buildCmd.Stderr = os.Stderr
		log.Info("executing", "command", buildCmd.String())
		buildCmd.Env = os.Environ()
		if err := buildCmd.Run(); err != nil {
			log.Debug("Failed to build image", "error", err)
			return err
		}
		cmdLog.Info("✅ image build done", "duration", time.Since(start))
		return nil
	})

	if err := eg.Wait(); err != nil {
		return nil, err
	}

	// Create the container
	port := opts.Inputs.Port
	hostCfg := &container.HostConfig{
		PortBindings: nat.PortMap{
			nat.Port(fmt.Sprintf("%d/tcp", port)): []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: strconv.Itoa(port)}},
		},
	}

	if scenario != "default" {
		opts.Inputs.OtelEndpoint = "otel-collector:4318"
	}

	_, err := dockerClient.ContainerCreate(ctx, &container.Config{
		Image: scenario,
		Cmd:   []string{"/app/inputs.json"},
		ExposedPorts: nat.PortSet{
			nat.Port(fmt.Sprintf("%d/tcp", port)): struct{}{},
		},
		Env: []string{
			fmt.Sprintf("OTEL_EXPORTER_OTLP_ENDPOINT=%s", opts.Inputs.OtelEndpoint),
		},
	}, hostCfg, nil, nil, scenario)

	if err := dockerClient.NetworkConnect(ctx, networkName, scenario, nil); err != nil {
		log.Error("❌ Failed to connect container to network", "error", err)
		return nil, err
	}

	if err != nil {
		log.Debug("Failed to create container", "error", err)
		return nil, err
	}

	// Handle inputs.json before starting the container
	data, err := json.MarshalIndent(opts.Inputs, "", "  ")
	if err != nil {
		return nil, err
	}

	buf := new(bytes.Buffer)
	tw := tar.NewWriter(buf)
	if err := tw.WriteHeader(&tar.Header{
		Name: "inputs.json",
		Mode: 0o644,
		Size: int64(len(data)),
	}); err != nil {
		return nil, err
	}
	if _, err := tw.Write(data); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}

	if err := dockerClient.CopyToContainer(ctx, scenario, "/app", buf, container.CopyToContainerOptions{}); err != nil {
		return nil, err
	}
	cleanup := func(opts container.StopOptions) error {
		return dockerClient.ContainerStop(ctx, scenario, opts)
	}

	// Start the container ONLY if not ebpf/obi/usdt (they start in their setup functions)
	if scenario == "ebpf" || scenario == "obi" || scenario == "usdt" {
		return cleanup, nil
	}

	if err := dockerClient.ContainerStart(ctx, scenario, container.StartOptions{}); err != nil {
		log.Debug("Failed to start container", "error", err)
		return nil, err
	}
	log.Info("✅ app build done")

	return cleanup, nil
}

func setupOTelEnvironment(_ context.Context, opts *RunManyOpts) error {
	log := opts.Logger
	// TODO: should this go here?
	log.Info("✅ OTel environment setup complete")
	return nil
}

func setupEBPFEnvironment(ctx context.Context, opts *RunManyOpts) (func(container.StopOptions) error, error) {
	// Setup eBPF environment
	log := opts.Logger

	log.Info("⌛ Pulling eBPF image...")
	if err := run("docker", "pull", "otel/autoinstrumentation-go"); err != nil {
		log.Error("❌ Failed to pull eBPF image", "error", err)
		return nil, err
	}

	_, err := dockerClient.ContainerCreate(ctx, &container.Config{
		Image: "otel/autoinstrumentation-go",
		Env: []string{
			"OTEL_GO_AUTO_TARGET_EXE=/app/main",
			"OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318",
			"OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf",
			"OTEL_SERVICE_NAME=fosdem-ebpf",
			"OTEL_PROPAGATORS=tracecontext,baggage",
		},
	}, &container.HostConfig{
		PidMode:    container.PidMode("container:ebpf"),
		Privileged: true,
		Binds:      []string{"/proc:/host/proc"},
	}, nil, nil, "go-auto")
	if err != nil {
		log.Error("❌ Failed to create eBPF container", "error", err)
		return nil, err
	}

	if err := dockerClient.ContainerStart(ctx, "ebpf", container.StartOptions{}); err != nil {
		log.Debug("❌ Failed to start container", "error", err)
		return nil, err
	}

	if err := dockerClient.NetworkConnect(ctx, networkName, "go-auto", nil); err != nil {
		log.Error("❌ Failed to connect go-auto to network", "error", err)
		return nil, err
	}
	if err := waitForAppHealth(ctx, opts.Inputs.Port, "13133"); err != nil {
		log.Warn("⚠️ health check failed during eBPF setup", "error", err)
	}
	if err := dockerClient.ContainerStart(ctx, "go-auto", container.StartOptions{}); err != nil {
		log.Error("❌ Failed to start eBPF sidecar", "error", err)
		return nil, err
	}

	cleanup := func(opts container.StopOptions) error {
		return dockerClient.ContainerStop(ctx, "go-auto", opts)
	}

	log.Info("✅ eBPF environment setup complete")
	return cleanup, nil
}

func setupOBIEnvironment(ctx context.Context, opts *RunManyOpts) (func(container.StopOptions) error, error) {
	log := opts.Logger

	log.Info("⌛ Pulling OBI image...")
	if err := run("docker", "pull", "otel/ebpf-instrument:main"); err != nil {
		log.Error("❌ Failed to pull OBI image", "error", err)
		return nil, err
	}

	_, err := dockerClient.ContainerCreate(ctx, &container.Config{
		Image: "otel/ebpf-instrument:main",
		Env: []string{
			"OBI_CONFIG_PATH=/etc/obi/config.yaml",
			"OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318",
			"OTEL_SERVICE_NAME=fosdem-obi",
			"OTEL_EBPF_OPEN_PORT=" + strconv.Itoa(opts.Inputs.Port),
			"OTEL_EBPF_PROMETHEUS_PORT=9090",
		},
	}, &container.HostConfig{
		PidMode:    container.PidMode("container:obi"),
		Privileged: true,
		Binds: []string{
			"/proc:/host/proc",
			filepath.Join(getRoot(), "obi-config.yaml") + ":/etc/obi/config.yaml:ro",
		},
	}, nil, nil, "go-obi")
	if err != nil {
		log.Error("❌ Failed to create OBI container", "error", err)
		return nil, err
	}

	if err := dockerClient.ContainerStart(ctx, "obi", container.StartOptions{}); err != nil {
		log.Debug("❌ Failed to start container", "error", err)
		return nil, err
	}
	if err := dockerClient.NetworkConnect(ctx, networkName, "go-obi", nil); err != nil {
		log.Error("❌ Failed to connect go-obi to network", "error", err)
		return nil, err
	}
	if err := waitForAppHealth(ctx, opts.Inputs.Port, "13133"); err != nil {
		log.Warn("⚠️ health check failed during OBI setup", "error", err)
	}
	if err := dockerClient.ContainerStart(ctx, "go-obi", container.StartOptions{}); err != nil {
		log.Error("❌ Failed to start OBI sidecar", "error", err)
		return nil, err
	}

	cleanup := func(opts container.StopOptions) error {
		return dockerClient.ContainerStop(ctx, "go-obi", opts)
	}

	log.Info("✅ OBI environment setup complete")
	return cleanup, nil
}

func setupUSDTEnvironment(ctx context.Context, opts *RunManyOpts) (func(container.StopOptions) error, error) {
	log := opts.Logger

	log.Info("⌛ Setting up USDT tracing environment...")

	// Build the exporter image first
	log.Info("⌛ Building USDT exporter image...")
	exporterBuild := &BuildOpts{
		Dir:  filepath.Join(getRoot(), "app/usdt"),
		Args: map[string]string{},
		Secrets: map[string]string{},
	}
	buildCmd := dockerClient.BuildCommand(ctx, exporterBuild, "usdt-exporter")
	buildCmd.Args = append(buildCmd.Args, "-f", filepath.Join(getRoot(), "app/usdt/exporter/Dockerfile"))
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr
	buildCmd.Env = os.Environ()
	if err := buildCmd.Run(); err != nil {
		log.Error("❌ Failed to build exporter image", "error", err)
		return nil, err
	}
	log.Info("✅ Exporter image built")

	// Start the application container first
	if err := dockerClient.ContainerStart(ctx, "usdt", container.StartOptions{}); err != nil {
		log.Debug("❌ Failed to start USDT app container", "error", err)
		return nil, err
	}

	// Wait for app to be ready
	if err := waitForAppHealth(ctx, opts.Inputs.Port, "health"); err != nil {
		log.Warn("⚠️ health check failed during USDT app startup", "error", err)
	}

	// Create bpftrace exporter container
	log.Info("⌛ Creating USDT exporter container...")
	_, err := dockerClient.ContainerCreate(ctx, &container.Config{
		Image: "usdt-exporter",
		Env: []string{
			"OTEL_EXPORTER_OTLP_ENDPOINT=otel-collector:4318",
			"TARGET_PID=1",
			"BPFTRACE_SCRIPT=/app/trace-json.bt",
		},
	}, &container.HostConfig{
		PidMode:    container.PidMode("container:usdt"),
		Privileged: true,
		Binds: []string{
			"/proc:/host/proc",
			"/sys:/sys:ro",
		},
	}, nil, nil, "go-usdt")
	if err != nil {
		log.Error("❌ Failed to create USDT exporter container", "error", err)
		return nil, err
	}

	if err := dockerClient.NetworkConnect(ctx, networkName, "go-usdt", nil); err != nil {
		log.Error("❌ Failed to connect go-usdt to network", "error", err)
		return nil, err
	}

	// Start the exporter container
	if err := dockerClient.ContainerStart(ctx, "go-usdt", container.StartOptions{}); err != nil {
		log.Error("❌ Failed to start USDT exporter", "error", err)
		return nil, err
	}

	// Give exporter a moment to attach
	time.Sleep(3 * time.Second)

	cleanup := func(opts container.StopOptions) error {
		return dockerClient.ContainerStop(ctx, "go-usdt", opts)
	}

	log.Info("✅ USDT environment setup complete")
	return cleanup, nil
}

// Cleanup running services and ports. Only run when requested by the user, since this will
// kill any local services (ie Grafana)
func cleanup(log *slog.Logger) error {
	log.Info("🧹 Cleaning up...")

	_ = run(dockerCommand, "down", "--remove-orphans") // best-effort cleanup

	// Make sure that all containers are stopped and removed, or else re-running
	// will cause conflicts with existing container names.
	for _, s := range allScenarios {
		_ = runSilently("docker", "rm", "-f", s)
	}

	for _, s := range containerNames {
		_ = runSilently("docker", "rm", "-f", s)
	}

	// Kill ports
	if serverPID != nil {
		_ = serverPID.Kill() // best-effort process cleanup
		_, _ = serverPID.Wait()
	}

	log.Info("✅ Clean up complete! ✨")
	return nil
}

func cmdExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func runSilently(cmd string, args ...string) error {
	if cmd == "docker compose" {
		cmd = "docker"
		args = append([]string{"compose"}, args...)
	}
	c := exec.Command(cmd, args...)
	return c.Run()
}

func run(cmd string, args ...string) error {
	if cmd == "docker compose" {
		cmd = "docker"
		args = append([]string{"compose"}, args...)
	}
	c := exec.Command(cmd, args...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

func getRoot() string {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		panic("Failed to get caller information")
	}
	return filepath.Join(filepath.Dir(filename), "..")
}

func startStats(ctx context.Context, containerID string) func() ([]*container.StatsResponse, error) {
	resultCh := make(chan []*container.StatsResponse, 1)
	firstCh := make(chan struct{})
	stopCh := make(chan struct{})
	var err error
	go func() {
		snapshots := []*container.StatsResponse{}
		next := time.Now()
		for running := true; running; {
			select {
			case <-stopCh:
				// capture one last stats snapshot before exiting
				running = false
			case <-ctx.Done():
				running = false
			case <-time.After(time.Until(next)):
			}
			var stats container.StatsResponse
			stats, err = getContainerStats(ctx, containerID)
			if err == nil {
				snapshots = append(snapshots, &stats)
			}
			next = next.Add(100 * time.Millisecond)
			select {
			case <-firstCh:
			default:
				close(firstCh)
			}
		}
		resultCh <- snapshots
	}()

	// Wait for the first stats snapshot to be taken before returning.
	<-firstCh

	return func() ([]*container.StatsResponse, error) {
		stopCh <- struct{}{}
		res := <-resultCh
		return res, err
	}
}

func getContainerStats(ctx context.Context, containerID string) (container.StatsResponse, error) {
	statsReader, err := dockerClient.ContainerStatsOneShot(ctx, containerID)
	if err != nil {
		return container.StatsResponse{}, err
	}
	var stats container.StatsResponse
	if err := json.NewDecoder(statsReader.Body).Decode(&stats); err != nil {
		return container.StatsResponse{}, err
	}
	return stats, nil
}

func checkNetwork(ctx context.Context, opts *RunManyOpts) error {
	log := opts.Logger
	networks, err := dockerClient.NetworkList(ctx, types.ListOptions{})
	if err != nil {
		return err
	}
	for _, network := range networks {
		if network.Name == networkName {
			log.Info("✅ Network found", "network", network.Name)
			return nil
		}
	}
	log.Info("Network not found, creating...")
	_, err = dockerClient.NetworkCreate(ctx, networkName, types.CreateOptions{
		Driver: "bridge",
	})
	if err != nil {
		log.Error("❌ Failed to create network", "error", err)
		return err
	}
	log.Info("✅ Network created", "network", networkName)
	return nil
}
