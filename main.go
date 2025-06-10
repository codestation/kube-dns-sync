// Copyright 2025 codestation. All rights reserved.
// Use of this source code is governed by a MIT-license
// that can be found in the LICENSE file.

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"os/signal"
	"slices"
	"strings"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/posflag"
	"github.com/knadh/koanf/v2"
	"github.com/libdns/cloudflare"
	"github.com/libdns/digitalocean"
	"github.com/libdns/libdns"
	flag "github.com/spf13/pflag"
	linode "go.megpoid.dev/libdns-linode"
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
	Hostname string            `yaml:"hostname"`
	Zone     string            `yaml:"zone"`
	Labels   map[string]string `yaml:"labels"`
	TTL      time.Duration     `yaml:"ttl"`
}

type GlobalConfig struct {
	Provider string        `yaml:"provider"`
	Token    string        `yaml:"token"`
	Interval time.Duration `yaml:"interval"`
}

type Config struct {
	Global GlobalConfig `yaml:"global"`
	DNS    []DNSConfig  `yaml:"dns"`
}

var ErrNotAddressRecord = errors.New("the type must be an A/AAAA record")

func parseAddress(record libdns.Record) (libdns.Address, error) {
	r, err := record.RR().Parse()
	if err != nil {
		return libdns.Address{}, fmt.Errorf("failed to parse record: %w", err)
	}

	if v, ok := r.(libdns.Address); ok {
		return v, nil
	}

	return libdns.Address{}, ErrNotAddressRecord
}

func syncHostnameIPs(ctx context.Context, provider Provider, config DNSConfig, addresses []netip.Addr) error {
	records, err := provider.GetRecords(ctx, config.Zone)
	if err != nil {
		return err
	}

	var recordsToDelete []libdns.Record
	for _, record := range records {
		address, err := parseAddress(record)
		if err != nil && !errors.Is(err, ErrNotAddressRecord) {
			slog.Error("Failed to parse record", "name", record.RR().Name, "error", err)
			continue
		}

		if address.Name == config.Hostname && !slices.Contains(addresses, address.IP) {
			recordsToDelete = append(recordsToDelete, record)
		}
	}

	if len(recordsToDelete) > 0 {
		slog.Info("Deleting stale records", "count", len(recordsToDelete))
		_, err = provider.DeleteRecords(ctx, config.Zone, recordsToDelete)
		if err != nil {
			return fmt.Errorf("failed to delete records: %w", err)
		}
	}

	var recordsToCreate []libdns.Record
	for _, address := range addresses {
		exists := slices.ContainsFunc(records, func(nodeRecord libdns.Record) bool {
			rAddress, err := parseAddress(nodeRecord)
			if err != nil && !errors.Is(err, ErrNotAddressRecord) {
				slog.Error("Failed to parse record", "name", nodeRecord.RR().Name, "error", err)
				return false
			}

			return rAddress.Name == config.Hostname && rAddress.IP == address
		})

		if !exists {
			recordsToCreate = append(recordsToCreate, libdns.Address{
				Name: config.Hostname,
				IP:   address,
				TTL:  config.TTL,
			})
		}
	}

	if len(recordsToCreate) > 0 {
		slog.Info("Creating new records", "count", len(recordsToCreate))
		_, err = provider.SetRecords(ctx, config.Zone, recordsToCreate)
		if err != nil {
			return fmt.Errorf("failed to create records: %w", err)
		}
	}

	slog.Info("Sync complete")

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

func getClusterExternalIPs(ctx context.Context, clientSet *kubernetes.Clientset, labels string) (string, []netip.Addr, error) {
	listOptions := metav1.ListOptions{LabelSelector: labels}
	nodes, err := clientSet.CoreV1().Nodes().List(ctx, listOptions)
	if err != nil {
		return "", nil, fmt.Errorf("failed to list nodes: %w", err)
	}

	var addresses []netip.Addr
	for _, node := range nodes.Items {
		if !isNodeReady(node) {
			continue
		}
		for _, address := range node.Status.Addresses {
			if address.Type == corev1.NodeExternalIP {
				slog.Info("Found external IP", "node", node.Name, "address", address.Address)
				addr, err := netip.ParseAddr(address.Address)
				if err != nil {
					slog.Error("Failed to parse address", "address", address.Address, "error", err)
					continue
				}
				addresses = append(addresses, addr)
			}
		}
	}

	return nodes.ResourceVersion, addresses, nil
}

func watchNodes(ctx context.Context, clientSet *kubernetes.Clientset, provider Provider, config DNSConfig) error {
	var result []string
	for k, v := range config.Labels {
		result = append(result, fmt.Sprintf("%s=%s", k, v))
	}
	// Get the external IPs of the cluster nodes
	_, addresses, err := getClusterExternalIPs(ctx, clientSet, strings.Join(result, ","))
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
	f := flag.NewFlagSet("config", flag.ContinueOnError)
	f.Usage = func() {
		fmt.Println(f.FlagUsages())
		os.Exit(0)
	}

	f.String("conf", "config.yaml", "Config file")
	f.String("token", "", "DNS Provider API token")
	f.String("kubeconfig", "", "Path to the kubeconfig file")
	f.String("log-format", "", "Log format (logfmt, json)")
	f.Bool("version", false, "Print version information")

	err := f.Parse(os.Args[1:])
	if err != nil {
		slog.Error("Failed to parse flags", "error", err)
		os.Exit(1)
	}

	configFile, err := f.GetString("conf")
	if err != nil {
		slog.Error("Failed to get config file", "error", err)
		os.Exit(1)
	}

	if err := k.Load(file.Provider(configFile), yaml.Parser()); err != nil {
		slog.Error("Failed to load config file", "file", configFile, "error", err)
		os.Exit(1)
	}

	err = k.Load(env.Provider("APP_", ".", func(s string) string {
		return strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(s, "APP_")), "_", ".")
	}), nil)
	if err != nil {
		slog.Error("Failed to load environment variables", "error", err)
		os.Exit(1)
	}

	var dnsConfig Config
	if err := k.Unmarshal("", &dnsConfig); err != nil {
		slog.Error("Failed to parse config", "error", err)
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

	if len(dnsConfig.DNS) == 0 {
		slog.Error("No DNS provider configuration specified")
		os.Exit(1)
	}

	// Get DNS provider and hostname from environment variables
	dnsProvider := k.String("global.provider")
	dnsToken := k.String("global.token")

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

	go func(ctx context.Context) {
		for {
			for _, cfg := range dnsConfig.DNS {
				slog.Info("Processing host", "name", cfg.Hostname)
				err := watchNodes(ctx, clientset, provider, cfg)
				if err != nil {
					slog.Error("Failed to watch nodes", "error", err)
				}

				select {
				case <-ctx.Done():
					slog.Info("Exiting...")
					close(finishChan)
					return
				default:
				}
			}

			select {
			case <-ctx.Done():
				slog.Info("Exiting...")
				close(finishChan)
				return
			case <-time.After(dnsConfig.Global.Interval):
			}
		}
	}(ctx)

	<-termChan
	cancel()
	<-finishChan
}
