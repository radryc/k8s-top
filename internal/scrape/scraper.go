package scrape

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
)

type ContainerSample struct {
	Namespace   string
	Pod         string
	Container   string
	CPUMilli    int64
	MemoryBytes int64
}

type PodSample struct {
	Namespace      string
	Pod            string
	CPUMilli       int64
	MemoryBytes    int64
	ContainerCount int
}

type Summary struct {
	ObservedNamespaces int
	ObservedPods       int
	ObservedContainers int
	TopCPUContainer    *ContainerSample
	TopMemoryContainer *ContainerSample
}

type Result struct {
	CollectedAt      time.Time
	PodSamples       []PodSample
	ContainerSamples []ContainerSample
	Summary          Summary
}

type Scraper struct {
	metricsClient metricsclient.Interface
}

type Store struct {
	mu     sync.RWMutex
	result Result
}

func New(kubeconfig string) (*Scraper, error) {
	restConfig, err := loadRestConfig(kubeconfig)
	if err != nil {
		return nil, err
	}
	client, err := metricsclient.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("create metrics client: %w", err)
	}
	return &Scraper{metricsClient: client}, nil
}

func (s *Scraper) Collect(ctx context.Context) (Result, error) {
	list, err := s.metricsClient.MetricsV1beta1().PodMetricses(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return Result{}, fmt.Errorf("list pod metrics: %w", err)
	}

	namespaces := make(map[string]struct{})
	podSamples := make([]PodSample, 0, len(list.Items))
	containerSamples := make([]ContainerSample, 0)
	var topCPU *ContainerSample
	var topMemory *ContainerSample

	for _, podMetrics := range list.Items {
		namespaces[podMetrics.Namespace] = struct{}{}

		podCPU := int64(0)
		podMemory := int64(0)
		for _, containerMetrics := range podMetrics.Containers {
			sample := ContainerSample{
				Namespace:   podMetrics.Namespace,
				Pod:         podMetrics.Name,
				Container:   containerMetrics.Name,
				CPUMilli:    containerMetrics.Usage.Cpu().MilliValue(),
				MemoryBytes: containerMetrics.Usage.Memory().Value(),
			}
			containerSamples = append(containerSamples, sample)
			podCPU += sample.CPUMilli
			podMemory += sample.MemoryBytes

			if topCPU == nil || sample.CPUMilli > topCPU.CPUMilli {
				current := sample
				topCPU = &current
			}
			if topMemory == nil || sample.MemoryBytes > topMemory.MemoryBytes {
				current := sample
				topMemory = &current
			}
		}

		podSamples = append(podSamples, PodSample{
			Namespace:      podMetrics.Namespace,
			Pod:            podMetrics.Name,
			CPUMilli:       podCPU,
			MemoryBytes:    podMemory,
			ContainerCount: len(podMetrics.Containers),
		})
	}

	sort.Slice(podSamples, func(i, j int) bool {
		if podSamples[i].Namespace != podSamples[j].Namespace {
			return podSamples[i].Namespace < podSamples[j].Namespace
		}
		return podSamples[i].Pod < podSamples[j].Pod
	})
	sort.Slice(containerSamples, func(i, j int) bool {
		if containerSamples[i].Namespace != containerSamples[j].Namespace {
			return containerSamples[i].Namespace < containerSamples[j].Namespace
		}
		if containerSamples[i].Pod != containerSamples[j].Pod {
			return containerSamples[i].Pod < containerSamples[j].Pod
		}
		return containerSamples[i].Container < containerSamples[j].Container
	})

	return Result{
		CollectedAt:      time.Now().UTC(),
		PodSamples:       podSamples,
		ContainerSamples: containerSamples,
		Summary: Summary{
			ObservedNamespaces: len(namespaces),
			ObservedPods:       len(podSamples),
			ObservedContainers: len(containerSamples),
			TopCPUContainer:    topCPU,
			TopMemoryContainer: topMemory,
		},
	}, nil
}

func (s *Store) Replace(result Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.result = result
}

func (s *Store) Snapshot() Result {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.result
}

func loadRestConfig(kubeconfig string) (*rest.Config, error) {
	if trimmed := strings.TrimSpace(kubeconfig); trimmed != "" {
		config, err := clientcmd.BuildConfigFromFlags("", trimmed)
		if err != nil {
			return nil, fmt.Errorf("load kubeconfig %s: %w", trimmed, err)
		}
		return config, nil
	}

	if config, err := rest.InClusterConfig(); err == nil {
		return config, nil
	}

	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{})
	config, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load default kubeconfig: %w", err)
	}
	return config, nil
}
