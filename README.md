# kgcr

A fast and efficient Kubernetes tool for discovering Custom Resources (CRs) in your cluster namespaces.

## Overview

`kgcr` (Kubernetes Get Custom Resources) is a command-line tool that scans a specified Kubernetes namespace and lists all custom resource instances found. It's designed to be fast and efficient by using concurrent workers to query multiple Custom Resource Definitions (CRDs) in parallel.

## Features

- **Fast parallel scanning** - Uses concurrent workers to query multiple CRDs simultaneously
- **Namespace-aware** - Automatically uses the current kubectl context's namespace or accepts a custom namespace
- **Filtered output** - Only shows namespaced resources (excludes cluster-scoped CRDs)
- **Clean tabular output** - Displays results in an easy-to-read table format
- **Performance optimized** - Pre-computes resource metadata and uses efficient batching strategies
- **Configurable timeout** - Prevents hanging on slow API responses

## Installation

### Prerequisites

- Go 1.19 or higher
- Access to a Kubernetes cluster
- Valid kubeconfig file

### Build from source

```bash
git clone https://github.com/yourusername/kgcr.git
cd kgcr
go build -o kgcr main.go
```

### Install with go install

```bash
go install github.com/yourusername/kgcr@latest
```

## Usage

### Basic usage

Scan the current namespace from your kubectl context:

```bash
kgcr
```

### Specify a namespace

Scan a specific namespace:

```bash
kgcr -n production
or
kgcr -namespace production
```
### All namespaces

Scan a specific namespace:

```bash
kgcr -A
or
kgcr -all-namespaces
```

### Set custom timeout

Set a custom timeout for the operation (default: 30s):

```bash
kgcr -n production -timeout 60s
```

### Example output

```
CRD                                    RESOURCE               NAME
certificates.cert-manager.io           certificates           api-cert
applications.argoproj.io	           applications	          root
```

## How it works

1. **CRD Discovery**: Lists all Custom Resource Definitions in the cluster
2. **Filtering**: Filters out cluster-scoped CRDs, keeping only namespaced resources
3. **Parallel Processing**: Distributes CRDs among concurrent workers
4. **Resource Listing**: Each worker queries the Kubernetes API for instances of assigned CRDs
5. **Result Aggregation**: Collects and sorts all found resources
6. **Output Formatting**: Displays results in a clean, tabular format

## Performance considerations

The tool is optimized for performance with:

- Concurrent processing with worker pool sized based on CPU cores
- Pre-computed resource metadata to avoid repeated calculations
- Buffered channels for efficient inter-goroutine communication
- Reusable memory allocations to reduce garbage collection pressure
- Configurable QPS and burst limits for API requests

## Configuration

The tool respects standard Kubernetes client configuration:

- Uses the default kubeconfig location (`~/.kube/config`)
- Respects `KUBECONFIG` environment variable
- Uses the current kubectl context
- Falls back to "default" namespace if none is specified

## Limitations

- Only lists resources in a single namespace at a time
- Requires appropriate RBAC permissions to list CRDs and custom resources
- API server rate limiting may affect performance in large clusters

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.