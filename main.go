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
	"time"

	"github.com/libdns/cloudflare"
	"github.com/libdns/digitalocean"
	"github.com/libdns/libdns"
	"github.com/libdns/linode"
	"golang.org/x/term"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type Provider interface {
	libdns.RecordGetter
	libdns.RecordSetter
	libdns.RecordDeleter
}

func syncHostnameIPs(ctx context.Context, provider Provider, dnsZone string, dnsHostname string, addresses []string) error {
	records, err := provider.GetRecords(ctx, dnsZone)
	if err != nil {
		return err
	}

	var recordsToDelete []libdns.Record
	for _, record := range records {
		if record.Name == dnsHostname && record.Type == "A" && !slices.Contains(addresses, record.Value) {
			recordsToDelete = append(recordsToDelete, record)
		}
	}

	slog.Info("Deleting stale records", "count", len(recordsToDelete))
	_, err = provider.DeleteRecords(ctx, dnsZone, recordsToDelete)
	if err != nil {
		return fmt.Errorf("failed to delete records: %w", err)
	}

	var recordsToCreate []libdns.Record
	for _, address := range addresses {
		exists := slices.ContainsFunc(records, func(r libdns.Record) bool {
			return r.Name == dnsHostname && r.Type == "A" && r.Value == address
		})

		if !exists {
			recordsToCreate = append(recordsToCreate, libdns.Record{
				Type:  "A",
				Name:  dnsHostname,
				Value: address,
			})
		}
	}

	slog.Info("Creating new records", "count", len(recordsToCreate))
	_, err = provider.SetRecords(ctx, dnsZone, recordsToCreate)
	if err != nil {
		return fmt.Errorf("failed to create records: %w", err)
	}

	return nil
}

func getClusterExternalIPs(ctx context.Context, clientset *kubernetes.Clientset, labels string) (string, []string, error) {
	listOptions := metav1.ListOptions{LabelSelector: labels}
	nodes, err := clientset.CoreV1().Nodes().List(ctx, listOptions)
	if err != nil {
		return "", nil, fmt.Errorf("failed to list nodes: %w", err)
	}

	var addresses []string
	for _, node := range nodes.Items {
		for _, address := range node.Status.Addresses {
			if address.Type == corev1.NodeExternalIP {
				slog.Info("Found external IP", "node", node.Name, "address", address.Address)
				addresses = append(addresses, address.Address)
			}
		}
	}

	return nodes.ResourceVersion, addresses, nil
}

func watchNodes(ctx context.Context, clientset *kubernetes.Clientset, provider Provider, dnsZone, dnsHostname, labels string) error {
	// Get the external IPs of the cluster nodes
	_, addresses, err := getClusterExternalIPs(ctx, clientset, labels)
	if err != nil {
		return fmt.Errorf("failed to get cluster external IPs: %w", err)
	}

	// Sync the external IPs with the DNS provider
	err = syncHostnameIPs(ctx, provider, dnsZone, dnsHostname, addresses)
	if err != nil {
		return fmt.Errorf("failed to sync hostname IPs: %w", err)
	}

	return nil
}

func main() {
	if os.Args[1] == "version" {
		slog.Info("kube-dns-sync",
			slog.String("version", Tag),
			slog.String("commit", Revision),
			slog.Time("date", LastCommit),
			slog.Bool("clean_build", !Modified),
		)
		os.Exit(0)
	}

	isTerminal := term.IsTerminal(int(os.Stdout.Fd()))

	switch os.Getenv("LOG_FORMAT") {
	case "json":
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	case "logfmt":
	case "":
		if !isTerminal {
			slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
		}
	default:
		slog.Error("Invalid LOG_FORMAT environment variable")
		os.Exit(1)
	}

	// Get DNS provider and hostname from environment variables
	dnsProvider := os.Getenv("DNS_PROVIDER")
	dnsHostname := os.Getenv("DNS_HOSTNAME")
	dnsZone := os.Getenv("DNS_ZONE")

	if dnsProvider == "" || dnsHostname == "" || dnsZone == "" {
		slog.Error("Missing DNS_PROVIDER, DNS_HOST and DNS_ZONE environment variable")
		os.Exit(1)
	}

	var provider Provider

	switch dnsProvider {
	case "cloudflare":
		cloudflareAPIToken := os.Getenv("CLOUDFLARE_API_TOKEN")
		if cloudflareAPIToken == "" {
			slog.Error("Missing CLOUDFLARE_API_TOKEN environment variable")
			os.Exit(1)
		}
		provider = &cloudflare.Provider{APIToken: cloudflareAPIToken}
	case "digitalocean":
		digitaloceanToken := os.Getenv("DIGITALOCEAN_TOKEN")
		if digitaloceanToken == "" {
			slog.Error("Missing DIGITALOCEAN_TOKEN environment variable")
			os.Exit(1)
		}
		provider = &digitalocean.Provider{APIToken: digitaloceanToken}
	case "linode":
		linodeToken := os.Getenv("LINODE_TOKEN")
		if linodeToken == "" {
			slog.Error("Missing LINODE_TOKEN environment variable")
			os.Exit(1)
		}
		provider = &linode.Provider{APIToken: linodeToken}
	}

	kubeConfigPath := os.Getenv("KUBECONFIG")
	config, err := clientcmd.BuildConfigFromFlags("", kubeConfigPath)
	if err != nil {
		slog.Error("Failed to create Kubernetes config", "error", err)
		os.Exit(1)
	}

	var interval time.Duration
	intervalValue := os.Getenv("WATCH_INTERVAL")
	if intervalValue == "" {
		interval = 1 * time.Minute
	} else {
		parsedInterval, err := time.ParseDuration(intervalValue)
		if err != nil {
			slog.Error("Failed to parse WATCH_INTERVAL", "error", err)
			os.Exit(1)
		}

		interval = parsedInterval
	}

	labels := os.Getenv("NODE_LABELS")

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

	go func(ctx context.Context) {
		for {
			err := watchNodes(ctx, clientset, provider, dnsZone, dnsHostname, labels)
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
