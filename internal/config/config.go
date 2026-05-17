package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultPollInterval   = 15 * time.Second
	defaultMetricInterval = 15 * time.Second
	defaultServiceName    = "k8s-top"
)

type Config struct {
	ServiceName    string
	ClusterName    string
	Kubeconfig     string
	OTLPEndpoint   string
	OTLPInsecure   bool
	PollInterval   time.Duration
	MetricInterval time.Duration
	InstanceID     string
	Component      string
}

func Load() (Config, error) {
	component := strings.TrimSpace(filepath.Base(os.Args[0]))
	if component == "" {
		component = defaultServiceName
	}

	cfg := Config{
		ServiceName:    envFirst("K8S_TOP_OTEL_SERVICE_NAME", "OTEL_SERVICE_NAME"),
		ClusterName:    envFirst("K8S_TOP_CLUSTER_NAME", "KUBERNETES_CLUSTER_NAME"),
		Kubeconfig:     strings.TrimSpace(os.Getenv("K8S_TOP_KUBECONFIG")),
		OTLPEndpoint:   envFirst("K8S_TOP_OTEL_ENDPOINT", "OTEL_EXPORTER_OTLP_ENDPOINT"),
		OTLPInsecure:   true,
		PollInterval:   defaultPollInterval,
		MetricInterval: defaultMetricInterval,
		InstanceID:     envFirst("HOSTNAME"),
		Component:      component,
	}
	if cfg.ServiceName == "" {
		cfg.ServiceName = defaultServiceName
	}
	if cfg.InstanceID == "" {
		cfg.InstanceID = component
	}

	if value := envFirst("K8S_TOP_OTEL_INSECURE", "OTEL_EXPORTER_OTLP_INSECURE"); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("parse OTLP insecure flag: %w", err)
		}
		cfg.OTLPInsecure = parsed
	}

	pollInterval, err := parseDurationEnv(defaultPollInterval, "K8S_TOP_POLL_INTERVAL")
	if err != nil {
		return Config{}, err
	}
	cfg.PollInterval = pollInterval

	metricInterval, err := parseDurationEnv(defaultMetricInterval, "K8S_TOP_OTEL_METRIC_INTERVAL")
	if err != nil {
		return Config{}, err
	}
	cfg.MetricInterval = metricInterval

	return cfg, nil
}

func envFirst(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func parseDurationEnv(defaultValue time.Duration, key string) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return defaultValue, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	return parsed, nil
}
