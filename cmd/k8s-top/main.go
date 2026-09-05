package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rydzu/ainfra/k8s-top/internal/buildinfo"
	"github.com/rydzu/ainfra/k8s-top/internal/config"
	"github.com/rydzu/ainfra/k8s-top/internal/scrape"
	"github.com/rydzu/ainfra/k8s-top/internal/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	metricapi "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const scope = "ainfra/k8s-top"

type instruments struct {
	scrapeExecutions     metricapi.Int64Counter
	scrapeFailures       metricapi.Int64Counter
	scrapeDuration       metricapi.Float64Histogram
	observedPods         metricapi.Int64ObservableGauge
	observedContainers   metricapi.Int64ObservableGauge
	lastSuccessUnix      metricapi.Int64ObservableGauge
	podCPUMilli          metricapi.Int64ObservableGauge
	podMemoryBytes       metricapi.Int64ObservableGauge
	containerCPUMilli    metricapi.Int64ObservableGauge
	containerMemoryBytes metricapi.Int64ObservableGauge
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	telemetryHandle, err := telemetry.Setup(ctx, telemetry.Config{
		Endpoint:       cfg.OTLPEndpoint,
		ServiceName:    cfg.ServiceName,
		Component:      cfg.Component,
		InstanceID:     cfg.InstanceID,
		ClusterName:    cfg.ClusterName,
		Insecure:       cfg.OTLPInsecure,
		MetricInterval: cfg.MetricInterval,
	})
	if err != nil {
		log.Fatal(err)
	}
	if telemetryHandle.Enabled() {
		log.SetOutput(io.MultiWriter(os.Stderr, telemetry.NewStdLogWriter("k8s-top/stdlog")))
		telemetry.EmitInfo(ctx, scope, "k8s-top telemetry enabled")
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := telemetryHandle.Shutdown(shutdownCtx); err != nil {
				log.Printf("shutdown telemetry: %v", err)
			}
		}()
	}

	log.Printf("starting k8s-top version=%s commit=%s build_time=%s cluster=%s poll_interval=%s metric_interval=%s", buildinfo.Version, buildinfo.Commit, buildinfo.BuildTime, valueOrDefault(cfg.ClusterName, "unknown"), cfg.PollInterval, cfg.MetricInterval)

	scraper, err := scrape.New(cfg.Kubeconfig)
	if err != nil {
		log.Fatal(err)
	}

	state := &scrape.Store{}
	registration, inst, err := registerMetrics(state, cfg.ClusterName)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := registration.Unregister(); err != nil {
			log.Printf("unregister metrics callback: %v", err)
		}
	}()

	if err := runScrape(ctx, scraper, state, inst, cfg.ClusterName); err != nil {
		log.Printf("initial scrape failed: %v", err)
	}

	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("stopping k8s-top: %v", ctx.Err())
			return
		case <-ticker.C:
			if err := runScrape(ctx, scraper, state, inst, cfg.ClusterName); err != nil {
				log.Printf("scrape failed: %v", err)
			}
		}
	}
}

func registerMetrics(state *scrape.Store, clusterName string) (metricapi.Registration, *instruments, error) {
	meter := otel.Meter(scope)
	inst := &instruments{}
	var err error

	inst.scrapeExecutions, err = meter.Int64Counter(
		"k8s.top.scrape.executions",
		metricapi.WithDescription("Number of Kubernetes metrics API scrapes attempted."),
	)
	if err != nil {
		return nil, nil, err
	}
	inst.scrapeFailures, err = meter.Int64Counter(
		"k8s.top.scrape.failures",
		metricapi.WithDescription("Number of Kubernetes metrics API scrapes that failed."),
	)
	if err != nil {
		return nil, nil, err
	}
	inst.scrapeDuration, err = meter.Float64Histogram(
		"k8s.top.scrape.duration.seconds",
		metricapi.WithDescription("Duration of a Kubernetes metrics API scrape."),
		metricapi.WithUnit("s"),
	)
	if err != nil {
		return nil, nil, err
	}
	inst.observedPods, err = meter.Int64ObservableGauge(
		"k8s.top.observed.pods",
		metricapi.WithDescription("Number of pods observed in the latest successful scrape."),
	)
	if err != nil {
		return nil, nil, err
	}
	inst.observedContainers, err = meter.Int64ObservableGauge(
		"k8s.top.observed.containers",
		metricapi.WithDescription("Number of containers observed in the latest successful scrape."),
	)
	if err != nil {
		return nil, nil, err
	}
	inst.lastSuccessUnix, err = meter.Int64ObservableGauge(
		"k8s.top.scrape.last_success_unix",
		metricapi.WithDescription("Unix timestamp of the latest successful scrape."),
		metricapi.WithUnit("s"),
	)
	if err != nil {
		return nil, nil, err
	}
	inst.podCPUMilli, err = meter.Int64ObservableGauge(
		"k8s.top.pod.cpu.usage.millicores",
		metricapi.WithDescription("Latest observed pod CPU usage aggregated across containers."),
		metricapi.WithUnit("m"),
	)
	if err != nil {
		return nil, nil, err
	}
	inst.podMemoryBytes, err = meter.Int64ObservableGauge(
		"k8s.top.pod.memory.usage.bytes",
		metricapi.WithDescription("Latest observed pod memory working set aggregated across containers."),
		metricapi.WithUnit("By"),
	)
	if err != nil {
		return nil, nil, err
	}
	inst.containerCPUMilli, err = meter.Int64ObservableGauge(
		"k8s.top.container.cpu.usage.millicores",
		metricapi.WithDescription("Latest observed container CPU usage from the Kubernetes metrics API."),
		metricapi.WithUnit("m"),
	)
	if err != nil {
		return nil, nil, err
	}
	inst.containerMemoryBytes, err = meter.Int64ObservableGauge(
		"k8s.top.container.memory.usage.bytes",
		metricapi.WithDescription("Latest observed container memory working set from the Kubernetes metrics API."),
		metricapi.WithUnit("By"),
	)
	if err != nil {
		return nil, nil, err
	}

	baseAttrs := clusterAttrs(clusterName)
	registration, err := meter.RegisterCallback(func(_ context.Context, observer metricapi.Observer) error {
		snapshot := state.Snapshot()
		if snapshot.CollectedAt.IsZero() {
			return nil
		}

		observer.ObserveInt64(inst.observedPods, int64(snapshot.Summary.ObservedPods), metricapi.WithAttributes(baseAttrs...))
		observer.ObserveInt64(inst.observedContainers, int64(snapshot.Summary.ObservedContainers), metricapi.WithAttributes(baseAttrs...))
		observer.ObserveInt64(inst.lastSuccessUnix, snapshot.CollectedAt.Unix(), metricapi.WithAttributes(baseAttrs...))

		for _, podSample := range snapshot.PodSamples {
			attrs := withAttrs(baseAttrs,
				attribute.String("k8s.namespace.name", podSample.Namespace),
				attribute.String("k8s.pod.name", podSample.Pod),
			)
			observer.ObserveInt64(inst.podCPUMilli, podSample.CPUMilli, metricapi.WithAttributes(attrs...))
			observer.ObserveInt64(inst.podMemoryBytes, podSample.MemoryBytes, metricapi.WithAttributes(attrs...))
		}
		for _, containerSample := range snapshot.ContainerSamples {
			attrs := withAttrs(baseAttrs,
				attribute.String("k8s.namespace.name", containerSample.Namespace),
				attribute.String("k8s.pod.name", containerSample.Pod),
				attribute.String("k8s.container.name", containerSample.Container),
			)
			observer.ObserveInt64(inst.containerCPUMilli, containerSample.CPUMilli, metricapi.WithAttributes(attrs...))
			observer.ObserveInt64(inst.containerMemoryBytes, containerSample.MemoryBytes, metricapi.WithAttributes(attrs...))
		}
		return nil
	},
		inst.observedPods,
		inst.observedContainers,
		inst.lastSuccessUnix,
		inst.podCPUMilli,
		inst.podMemoryBytes,
		inst.containerCPUMilli,
		inst.containerMemoryBytes,
	)
	if err != nil {
		return nil, nil, err
	}

	return registration, inst, nil
}

func runScrape(ctx context.Context, scraper *scrape.Scraper, state *scrape.Store, inst *instruments, clusterName string) error {
	baseAttrs := clusterAttrs(clusterName)
	tracer := otel.Tracer(scope)
	ctx, span := tracer.Start(ctx, "k8s-top.scrape", trace.WithAttributes(baseAttrs...))
	defer span.End()

	startedAt := time.Now()
	inst.scrapeExecutions.Add(ctx, 1, metricapi.WithAttributes(baseAttrs...))

	result, err := scraper.Collect(ctx)
	duration := time.Since(startedAt)
	inst.scrapeDuration.Record(ctx, duration.Seconds(), metricapi.WithAttributes(baseAttrs...))
	if err != nil {
		inst.scrapeFailures.Add(ctx, 1, metricapi.WithAttributes(baseAttrs...))
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		telemetry.EmitError(ctx, scope, fmt.Sprintf("k8s scrape failed: %v", err))
		return err
	}

	state.Replace(result)
	span.SetAttributes(
		attribute.Int("k8s.top.observed_namespaces", result.Summary.ObservedNamespaces),
		attribute.Int("k8s.top.observed_pods", result.Summary.ObservedPods),
		attribute.Int("k8s.top.observed_containers", result.Summary.ObservedContainers),
		attribute.Float64("k8s.top.scrape.duration.seconds", duration.Seconds()),
	)
	if result.Summary.TopMemoryContainer != nil {
		span.AddEvent("top-memory-container", trace.WithAttributes(
			attribute.String("k8s.namespace.name", result.Summary.TopMemoryContainer.Namespace),
			attribute.String("k8s.pod.name", result.Summary.TopMemoryContainer.Pod),
			attribute.String("k8s.container.name", result.Summary.TopMemoryContainer.Container),
			attribute.Int64("k8s.memory.usage.bytes", result.Summary.TopMemoryContainer.MemoryBytes),
		))
	}
	if result.Summary.TopCPUContainer != nil {
		span.AddEvent("top-cpu-container", trace.WithAttributes(
			attribute.String("k8s.namespace.name", result.Summary.TopCPUContainer.Namespace),
			attribute.String("k8s.pod.name", result.Summary.TopCPUContainer.Pod),
			attribute.String("k8s.container.name", result.Summary.TopCPUContainer.Container),
			attribute.Int64("k8s.cpu.usage.millicores", result.Summary.TopCPUContainer.CPUMilli),
		))
	}

	log.Printf(
		"scrape ok namespaces=%d pods=%d containers=%d duration=%s top_memory=%s top_cpu=%s",
		result.Summary.ObservedNamespaces,
		result.Summary.ObservedPods,
		result.Summary.ObservedContainers,
		duration.Round(time.Millisecond),
		formatContainerSample(result.Summary.TopMemoryContainer, func(sample *scrape.ContainerSample) string {
			return formatBytes(sample.MemoryBytes)
		}),
		formatContainerSample(result.Summary.TopCPUContainer, func(sample *scrape.ContainerSample) string {
			return formatMillicores(sample.CPUMilli)
		}),
	)
	return nil
}

func clusterAttrs(clusterName string) []attribute.KeyValue {
	if clusterName == "" {
		return nil
	}
	return []attribute.KeyValue{attribute.String("k8s.cluster.name", clusterName)}
}

func withAttrs(base []attribute.KeyValue, extra ...attribute.KeyValue) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, len(base)+len(extra))
	attrs = append(attrs, base...)
	attrs = append(attrs, extra...)
	return attrs
}

func formatContainerSample(sample *scrape.ContainerSample, formatter func(*scrape.ContainerSample) string) string {
	if sample == nil {
		return "none"
	}
	return fmt.Sprintf("%s/%s/%s=%s", sample.Namespace, sample.Pod, sample.Container, formatter(sample))
}

func formatBytes(value int64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%dB", value)
	}
	divisor, exp := int64(unit), 0
	for n := value / unit; n >= unit; n /= unit {
		divisor *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(value)/float64(divisor), "KMGTPE"[exp])
}

func formatMillicores(value int64) string {
	return fmt.Sprintf("%dm", value)
}

func valueOrDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
