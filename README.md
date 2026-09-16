# Managed Database operator
> Test task

## Description
Go module used inside Kubernetes clusters to manage a remote, unstable database provider.
Handles Create, Delete, and status tracking of the resource on the remote within the Kubernetes cluster.

## Getting Started

### Prerequisites
- go version v1.24.6+
- docker version 17.03+.
- kubectl version v1.11.3+.
- Access to a Kubernetes v1.11.3+ cluster.

### Implementation
All required code you can find inside [manageddatabase_controller.go](./internal/controller/manageddatabase_controller.go) file.

### Run

Start the cluster with the operator and the provisioner
```bash
make up
make manifests
make install
make run
```

In a separate terminal, enter:
```bash
kubectl apply -f - <<'EOF'
apiVersion: demo.example.com/v1alpha1
kind: ManagedDatabase
metadata:
  name: orders
spec:
  engine: postgres
  sizeGB: 20
EOF
```

To verify resource allocation use:
```bash
kubectl get manageddatabase orders
```

To stop and clean everything, run:
```bash
make reset
make down
```
