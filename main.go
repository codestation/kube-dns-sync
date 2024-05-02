// Copyright 2024 codestation. All rights reserved.
// Use of this source code is governed by a MIT-license
// that can be found in the LICENSE file.

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"strings"
	"time"

	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/posflag"
	"github.com/knadh/koanf/v2"
	"github.com/libdns/cloudflare"
	"github.com/libdns/digitalocean"
	"github.com/libdns/libdns"
	"github.com/libdns/linode"
	flag "github.com/spf13/pflag"
	"golang.org/x/term"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
)

type Provider interface {
	libdns.RecordGetter
	libdns.RecordSetter
	libdns.RecordDeleter
}

type DNSConfig struct {
	Hostname string
	Zone     string
	TTL      time.Duration
}

func syncHostnameIPs(ctx context.Context, provider Provider, config DNSConfig, addresses []string) error {
	records, err := provider.GetRecords(ctx, config.Zone)
	if err != nil {
		return err
	}

	var recordsToDelete []libdns.Record
	for _, record := range records {
		if record.Name == config.Hostname && record.Type == "A" && !slices.Contains(addresses, record.Value) {
			recordsToDelete = append(recordsToDelete, record)
		}
	}

	slog.Info("Deleting stale records", "count", len(recordsToDelete))
	_, err = provider.DeleteRecords(ctx, config.Zone, recordsToDelete)
	if err != nil {
		return fmt.Errorf("failed to delete records: %w", err)
	}

	var recordsToCreate []libdns.Record
	for _, address := range addresses {
		exists := slices.ContainsFunc(records, func(r libdns.Record) bool {
			return r.Name == config.Hostname && r.Type == "A" && r.Value == address
		})

		if !exists {
			recordsToCreate = append(recordsToCreate, libdns.Record{
				Type:  "A",
				Name:  config.Hostname,
				Value: address,
				TTL:   config.TTL,
			})
		}
	}

	slog.Info("Creating new records", "count", len(recordsToCreate))
	_, err = provider.SetRecords(ctx, config.Zone, recordsToCreate)
	if err != nil {
		return fmt.Errorf("failed to create records: %w", err)
	}

	return nil
}

func isNodeReady(node corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func getClusterExternalIPs(ctx context.Context, clientset *kubernetes.Clientset, labels string) (string, []string, error) {
	listOptions := metav1.ListOptions{LabelSelector: labels}
	nodes, err := clientset.CoreV1().Nodes().List(ctx, listOptions)
	if err != nil {
		return "", nil, fmt.Errorf("failed to list nodes: %w", err)
	}

	var addresses []string
	for _, node := range nodes.Items {
		if !isNodeReady(node) {
			continue
		}
		for _, address := range node.Status.Addresses {
			if address.Type == corev1.NodeExternalIP {
				slog.Info("Found external IP", "node", node.Name, "address", address.Address)
				addresses = append(addresses, address.Address)
			}
		}
	}

	return nodes.ResourceVersion, addresses, nil
}

func watchNodes(ctx context.Context, clientset *kubernetes.Clientset, provider Provider, config DNSConfig, labels string) error {
	// Get the external IPs of the cluster nodes
	_, addresses, err := getClusterExternalIPs(ctx, clientset, labels)
	if err != nil {
		return fmt.Errorf("failed to get cluster external IPs: %w", err)
	}

	// Sync the external IPs with the DNS provider
	err = syncHostnameIPs(ctx, provider, config, addresses)
	if err != nil {
		return fmt.Errorf("failed to sync hostname IPs: %w", err)
	}

	return nil
}

var k = koanf.New(".")

func main() {
	err := k.Load(env.Provider("APP_", ".", func(s string) string {
		return strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(s, "APP_")), "_", ".")
	}), nil)
	if err != nil {
		slog.Error("Failed to load environment variables", "error", err)
		os.Exit(1)
	}

	f := flag.NewFlagSet("config", flag.ContinueOnError)
	f.Usage = func() {
		fmt.Println(f.FlagUsages())
		os.Exit(0)
	}

	f.String("dns-provider", "", "DNS provider (cloudflare, digitalocean, linode)")
	f.String("dns-hostname", "", "DNS hostname")
	f.String("dns-zone", "", "DNS zone")
	f.Duration("dns-ttl", 0, "DNS TTL")
	f.String("dns-token", "", "DNS Provider API token")
	f.String("kubeconfig", "", "Path to the kubeconfig file")
	f.Duration("watch-interval", time.Minute, "Interval to watch nodes")
	f.String("node-labels", "", "Labels to filter nodes")
	f.String("log-format", "", "Log format (logfmt, json)")
	f.Bool("version", false, "Print version information")

	err = f.Parse(os.Args[1:])
	if err != nil {
		slog.Error("Failed to parse flags", "error", err)
		os.Exit(1)
	}

	err = k.Load(posflag.ProviderWithValue(f, ".", k, func(key string, value string) (string, any) {
		return strings.ReplaceAll(key, "-", "."), value
	}), nil)
	if err != nil {
		slog.Error("Failed to load flags", "error", err)
		os.Exit(1)
	}

	if k.Bool("version") {
		slog.Info("kube-dns-sync",
			slog.String("version", Tag),
			slog.String("commit", Revision),
			slog.Time("date", LastCommit),
			slog.Bool("clean_build", !Modified),
		)
		os.Exit(0)
	}

	isTerminal := term.IsTerminal(int(os.Stdout.Fd()))

	switch k.String("log.format") {
	case "json":
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	case "logfmt":
	case "":
		if !isTerminal {
			slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
		}
	default:
		slog.Error("Invalid log format specified")
		os.Exit(1)
	}

	// Get DNS provider and hostname from environment variables
	dnsProvider := k.String("dns.provider")
	dnsHostname := k.String("dns.hostname")
	dnsZone := k.String("dns.zone")
	dnsToken := k.String("dns.token")

	if dnsProvider == "" || dnsHostname == "" || dnsZone == "" || dnsToken == "" {
		slog.Error("Missing DNS provider, host, zone and token values")
		os.Exit(1)
	}

	var provider Provider

	switch dnsProvider {
	case "cloudflare":
		provider = &cloudflare.Provider{APIToken: dnsToken}
	case "digitalocean":
		provider = &digitalocean.Provider{APIToken: dnsToken}
	case "linode":
		provider = &linode.Provider{APIToken: dnsToken}
	}

	kubeConfigPath := k.String("kubeconfig")

	klog.SetSlogLogger(slog.Default())
	config, err := clientcmd.BuildConfigFromFlags("", kubeConfigPath)
	if err != nil {
		slog.Error("Failed to create Kubernetes config", "error", err)
		os.Exit(1)
	}

	// creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		slog.Error("Failed to create Kubernetes client", "error", err)
		os.Exit(1)
	}

	slog.Info("kube-dns-sync started",
		slog.String("version", Tag),
		slog.String("commit", Revision),
		slog.Time("date", LastCommit),
		slog.Bool("clean_build", !Modified),
	)

	ctx, cancel := context.WithCancel(context.Background())
	finishChan := make(chan struct{})
	termChan := make(chan os.Signal, 1)
	signal.Notify(termChan, os.Interrupt)

	dnsConfig := DNSConfig{
		Hostname: dnsHostname,
		Zone:     dnsZone,
		TTL:      k.Duration("dns.ttl"),
	}

	interval := k.Duration("watch.interval")
	labels := k.String("node.labels")

	go func(ctx context.Context) {
		for {
			err := watchNodes(ctx, clientset, provider, dnsConfig, labels)
			if err != nil {
				slog.Error("Failed to watch nodes", "error", err)
			}
			select {
			case <-ctx.Done():
				slog.Info("Exiting...")
				close(finishChan)
				return
			case <-time.After(interval):
			}
		}
	}(ctx)

	<-termChan
	cancel()
	<-finishChan
}
