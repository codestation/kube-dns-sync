# README

## Project Overview

This project is a Go application that synchronizes DNS records with Kubernetes nodes. It supports multiple DNS providers
such as Cloudflare and Linode. The application watches for changes in the Kubernetes nodes and updates the DNS records
accordingly.

## Environment Variables

The application uses the following environment variables:

- `LOG_FORMAT`: Determines the format of the logs. It can be set to `json` or `logfmt`. If it's not set and the output 
is not an interactive terminal, it defaults to `json`.

- `DNS_PROVIDER`: Specifies the DNS provider to use. It can be set to `cloudflare`, `linode` or `digitalocean`.

- `DNS_HOSTNAME`: Specifies the hostname for the DNS records.

- `DNS_ZONE`: Specifies the DNS zone.

- `DNS_TTL`: Specifies the TTL for the DNS records. If not set, it defaults to the provider's default TTL.

- `CLOUDFLARE_API_TOKEN`: Required if `DNS_PROVIDER` is set to `cloudflare`. It's the API token for Cloudflare.

- `LINODE_TOKEN`: Required if `DNS_PROVIDER` is set to `linode`. It's the API token for Linode.

- `DIGITALOCEAN_TOKEN`: Required if `DNS_PROVIDER` is set to `digitalocean`. It's the API token for DigitalOcean.

- `KUBECONFIG`: Path to the kubeconfig file. If not set, it defaults to the internal cluster config.

- `WATCH_INTERVAL`: Specifies the interval for watching the Kubernetes nodes. It's a duration string
(e.g., "5m" for 5 minutes). If not set, it defaults to 1 minute.

- `NODE_LABELS`: Specifies the labels of the Kubernetes nodes to watch.

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
