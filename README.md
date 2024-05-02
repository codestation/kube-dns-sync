# README

## Project Overview

This project is a Go application that synchronizes DNS records with Kubernetes nodes. It supports multiple DNS providers
such as Cloudflare and Linode. The application watches for changes in the Kubernetes nodes and updates the DNS records
accordingly.

## Environment Variables

The application uses the following environment variables:

- `APP_LOG_FORMAT`: Determines the format of the logs. It can be set to `json` or `logfmt`. If it's not set and the 
  output 
is not an interactive terminal, it defaults to `json`.

- `APP_DNS_PROVIDER`: Specifies the DNS provider to use. It can be set to `cloudflare`, `linode` or `digitalocean`.

- `APP_DNS_HOSTNAME`: Specifies the hostname for the DNS records.

- `APP_DNS_ZONE`: Specifies the DNS zone.

- `APP_DNS_TTL`: Specifies the TTL for the DNS records. If not set, it defaults to the provider's default TTL.

- `APP_DNS_TOKEN`: Specifies the API token for the DNS provider.

- `APP_KUBECONFIG`: Path to the kubeconfig file. If not set, it defaults to the internal cluster config.

- `APP_WATCH_INTERVAL`: Specifies the interval for watching the Kubernetes nodes. It's a duration string
(e.g., "5m" for 5 minutes). If not set, it defaults to 1 minute.

- `APP_NODE_LABELS`: Specifies the labels of the Kubernetes nodes to watch.

## Kubernetes configuration

The application requires the following permissions to watch the Kubernetes nodes:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: dns-syncer
rules:
- apiGroups:
  - ""
  resources:
  - nodes
  verbs:
  - get
  - list
```

## License

This project is licensed under the MIT License. For more details, please see the `LICENSE` file.

## Contributing

Contributions are welcome. Please submit a pull request on GitHub.

## Contact

For any questions or issues, please open an issue on GitHub.
