#!/usr/bin/env bash
set -euo pipefail

CERT_MANAGER_VERSION=v1.20.0
BARMAN_PLUGIN_VERSION=v0.14.0   # check the plugin releases page for the newest

# 1. cluster
kind create cluster --config kind-config.yaml

# 2. cert-manager (required by the Barman plugin)
kubectl apply -f "https://github.com/cert-manager/cert-manager/releases/download/${CERT_MANAGER_VERSION}/cert-manager.yaml"
kubectl rollout status deploy/cert-manager -n cert-manager --timeout=180s
kubectl rollout status deploy/cert-manager-webhook -n cert-manager --timeout=180s
kubectl rollout status deploy/cert-manager-cainjector -n cert-manager --timeout=180s

# 3. CNPG operator
helm repo add cnpg https://cloudnative-pg.github.io/charts
helm repo update
helm upgrade --install cnpg cnpg/cloudnative-pg \
  --namespace cnpg-system --create-namespace
kubectl rollout status deploy/cnpg-cloudnative-pg -n cnpg-system --timeout=180s

# 4. Barman Cloud plugin (must be in cnpg-system)
kubectl apply -f "https://github.com/cloudnative-pg/plugin-barman-cloud/releases/download/${BARMAN_PLUGIN_VERSION}/manifest.yaml"
kubectl rollout status deploy/barman-cloud -n cnpg-system --timeout=180s

# 5. MinIO + bucket
kubectl apply -f minio.yaml
kubectl rollout status deploy/minio -n minio --timeout=180s
kubectl wait --for=condition=complete job/minio-create-bucket -n minio --timeout=120s

echo "done"
