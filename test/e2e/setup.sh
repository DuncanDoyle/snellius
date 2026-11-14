#!/usr/bin/env bash
# Creates a kind cluster with kgateway, the fixtures and the UI, then
# port-forwards the UI to 127.0.0.1:18080. Usage:
#   KGW_VERSION=v2.4.7 GWAPI_VERSION=v1.6.1 [SECOND_INSTALL=1] [OBSERVE_XLISTENERSET=1] [EXPLORER=0] [NACK=1] [CONTROLLER_LOGS=1] [UI_EXTRA_ARGS=...] [KGW_HELM_ARGS=...] test/e2e/setup.sh
#   test/e2e/setup.sh down    # stop the port-forward and delete the cluster
# EXPLORER=0 skips fixtures/explorer.yaml, analyzer.yaml, backends.yaml and traffic.yaml (the D-021 observations need a cluster without them).
# NACK=1 applies fixtures/nack.yaml: Gateway infra/nack, whose proxies reject a route config and a cluster.
# CONTROLLER_LOGS=1 installs the UI with diagnostics.controllerLogs on and its pods/log Role in kgw-a.
# UI_EXTRA_ARGS are space-separated UI flags for the chart's extraArgs, e.g. --poll-interval=10m;
# a value must not contain a comma (helm's --set list syntax splits on it).
# KGW_HELM_ARGS are space-separated extra helm flags for install A, e.g. --set controller.xds.scoping.mode=REFERENCED.
# SECURE=1 installs the UI in secure mode against test/oidc-issuer, run on this host at
# https://oidc.snellius.test:$ISSUER_PORT (default 18443). The API server trusts it through
# kind-auth.yaml; the node and the UI pod reach it on the kind network's gateway, or
# ISSUER_HOST_IP when the host is elsewhere (Docker Desktop). Linux runners need no override.
# The cluster's kubeconfig is written to $E2E_DIR/kubeconfig; the current
# kubeconfig context is left alone.
set -euo pipefail
cd "$(dirname "$0")/../.."

CLUSTER=${CLUSTER:-kgwui-e2e}
tmp=${TMPDIR:-/tmp}
E2E_DIR=${E2E_DIR:-${tmp%/}/$CLUSTER}
PORT=${PORT:-18080}
export KUBECONFIG=$E2E_DIR/kubeconfig
ISSUER_HOST=oidc.snellius.test
ISSUER_PORT=${ISSUER_PORT:-18443}
ISSUER_URL=https://$ISSUER_HOST:$ISSUER_PORT
OIDC_DIR=$E2E_DIR/oidc
CLIENT_ID=snellius

if [[ "$CLUSTER" == kgwui-dev ]]; then
  echo "refusing to manage the development cluster kgwui-dev" >&2
  exit 1
fi

down() {
  if [[ -f "$E2E_DIR/port-forward.pid" ]]; then
    kill "$(cat "$E2E_DIR/port-forward.pid")" 2>/dev/null || true
    rm -f "$E2E_DIR/port-forward.pid"
  fi
  if [[ -f "$E2E_DIR/oidc-issuer.pid" ]]; then
    kill "$(cat "$E2E_DIR/oidc-issuer.pid")" 2>/dev/null || true
    rm -f "$E2E_DIR/oidc-issuer.pid"
  fi
  if kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
    kind delete cluster --name "$CLUSTER" --kubeconfig "$KUBECONFIG"
  fi
}

# poll runs "$@" every second until it succeeds or $1 seconds pass.
poll() {
  local timeout=$1; shift
  local deadline=$((SECONDS + timeout))
  until "$@" >/dev/null 2>&1; do
    if ((SECONDS >= deadline)); then
      echo "timed out after ${timeout}s: $*" >&2
      return 1
    fi
    sleep 1
  done
}

# start_issuer runs test/oidc-issuer on all interfaces, so the kind node and pods reach it too.
start_issuer() {
  mkdir -p "$OIDC_DIR"
  rm -f "$OIDC_DIR/ca.pem"
  openssl rand -hex 24 >"$OIDC_DIR/client-secret"
  go build -o "$OIDC_DIR/oidc-issuer" ./test/oidc-issuer
  nohup "$OIDC_DIR/oidc-issuer" --issuer "$ISSUER_URL" --listen "0.0.0.0:$ISSUER_PORT" --ca-out "$OIDC_DIR/ca.pem" \
    --client-id "$CLIENT_ID" --client-secret-file "$OIDC_DIR/client-secret" \
    --redirect-uri "http://127.0.0.1:$PORT/auth/callback" \
    --user alice=team-a,team-b --user bob=team-b >"$OIDC_DIR/issuer.log" 2>&1 &
  echo $! >"$E2E_DIR/oidc-issuer.pid"
  if ! poll 60 curl -fsS --cacert "$OIDC_DIR/ca.pem" --resolve "$ISSUER_HOST:$ISSUER_PORT:127.0.0.1" \
    "$ISSUER_URL/.well-known/openid-configuration"; then
    cat "$OIDC_DIR/issuer.log" >&2
    exit 1
  fi
}

# render_auth prints kind-auth.yaml for API version $1, with the issuer's CA inline.
render_auth() {
  awk -v api="$1" -v url="$ISSUER_URL" -v client="$CLIENT_ID" -v ca="$OIDC_DIR/ca.pem" '
    /^ *#/ { print; next }
    /certificateAuthority: CA_PEM/ {
      indent = $0; sub(/[^ ].*/, "", indent)
      sub(/CA_PEM/, "|"); print
      while ((getline line < ca) > 0) print indent "  " line
      next
    }
    { gsub(/API_VERSION/, api); gsub(/ISSUER_URL/, url); gsub(/CLIENT_ID/, client); print }
  ' test/e2e/kind-auth.yaml
}

# patch_apiserver adds the authentication config flag and its hostPath volume to
# kubeadm's kube-apiserver manifest on stdin; it fails if an anchor line moved.
patch_apiserver() {
  awk '
    { print }
    $0 == "    - kube-apiserver" { print "    - --authentication-config=/etc/snellius-oidc/auth.yaml"; n++ }
    $0 == "    volumeMounts:" { print "    - mountPath: /etc/snellius-oidc"; print "      name: snellius-oidc"; print "      readOnly: true"; n++ }
    $0 == "  volumes:" { print "  - hostPath:"; print "      path: /etc/snellius-oidc"; print "      type: Directory"; print "    name: snellius-oidc"; n++ }
    END { if (n != 3) { print "kube-apiserver manifest layout changed: " n " of 3 anchors" > "/dev/stderr"; exit 1 } }
  '
}

apiserver_has_auth() {
  kubectl -n kube-system get pod "kube-apiserver-$CLUSTER-control-plane" -o jsonpath='{.spec.containers[0].command}' |
    grep -q authentication-config && kubectl get --raw /readyz
}

# trust_issuer points the API server at the test issuer. The node's /etc/hosts maps the
# issuer name to the host; kubelet copies it into the restarted host-network API server Pod.
trust_issuer() {
  local node=$CLUSTER-control-plane minor api=v1beta1
  ISSUER_IP=${ISSUER_HOST_IP:-$(docker network inspect kind -f '{{range .IPAM.Config}}{{println .Gateway}}{{end}}' | grep -v : | grep . | head -n1)}
  if [[ -z "$ISSUER_IP" ]]; then
    echo "no IPv4 gateway on the kind network; set ISSUER_HOST_IP" >&2
    exit 1
  fi
  # Structured authentication config is beta from 1.30 and v1 from 1.34.
  minor=$(kubectl get --raw /version | sed -n 's/.*"minor": *"\([0-9]*\).*/\1/p')
  if ((minor < 30)); then
    echo "SECURE=1 needs Kubernetes 1.30 or later, got 1.$minor" >&2
    exit 1
  elif ((minor >= 34)); then
    api=v1
  fi
  render_auth "$api" >"$OIDC_DIR/auth.yaml"
  docker exec "$node" mkdir -p /etc/snellius-oidc
  docker cp "$OIDC_DIR/auth.yaml" "$node:/etc/snellius-oidc/auth.yaml"
  docker exec "$node" sh -c "echo '$ISSUER_IP $ISSUER_HOST' >>/etc/hosts"
  docker cp "$node:/etc/kubernetes/manifests/kube-apiserver.yaml" "$OIDC_DIR/kube-apiserver.orig.yaml"
  patch_apiserver <"$OIDC_DIR/kube-apiserver.orig.yaml" >"$OIDC_DIR/kube-apiserver.yaml"
  # Stage outside the manifests directory, then move: kubelet reads every file in it.
  docker cp "$OIDC_DIR/kube-apiserver.yaml" "$node:/tmp/kube-apiserver.yaml"
  docker exec "$node" mv /tmp/kube-apiserver.yaml /etc/kubernetes/manifests/kube-apiserver.yaml
  if ! poll 180 apiserver_has_auth; then
    docker exec "$node" sh -c 'crictl ps -a --name kube-apiserver; crictl logs --tail 50 $(crictl ps -a -q --name kube-apiserver | head -n1)' >&2 || true
    exit 1
  fi
}

if [[ "${1:-}" == down ]]; then
  down
  exit 0
fi

: "${KGW_VERSION:?}" "${GWAPI_VERSION:?}"
mkdir -p "$E2E_DIR"
down # idempotent: start from a fresh cluster
if [[ "${SECURE:-}" == 1 ]]; then
  start_issuer
fi
kind create cluster --name "$CLUSTER" --kubeconfig "$KUBECONFIG" --wait 120s
if [[ "${SECURE:-}" == 1 ]]; then
  trust_issuer
fi
# The experimental channel adds TCPRoute, TLSRoute and ListenerSet for the computed-view e2e.
kubectl apply --server-side -f "https://github.com/kubernetes-sigs/gateway-api/releases/download/${GWAPI_VERSION}/experimental-install.yaml"
if [[ "${OBSERVE_XLISTENERSET:-}" == 1 ]]; then
  # Legacy XListenerSet for the D-021 observations only; v1.4.1 is the last release that ships it.
  kubectl apply --server-side -f "https://raw.githubusercontent.com/kubernetes-sigs/gateway-api/v1.4.1/config/crd/experimental/gateway.networking.x-k8s.io_xlistenersets.yaml"
fi
helm install kgateway-crds oci://cr.kgateway.dev/kgateway-dev/charts/kgateway-crds --version "$KGW_VERSION" -n kgw-a --create-namespace
# Release "kgateway" in each namespace: the controller Deployment is <ns>/kgateway (kgateway.fullname).
kgw_args=()
if [[ -n "${KGW_HELM_ARGS:-}" ]]; then
  read -ra kgw_args <<<"$KGW_HELM_ARGS"
fi
helm install kgateway oci://cr.kgateway.dev/kgateway-dev/charts/kgateway --version "$KGW_VERSION" -n kgw-a \
  --set-json 'discoveryNamespaceSelectors=[{"matchLabels":{"team":"a"}}]' ${kgw_args[@]+"${kgw_args[@]}"} --wait
kubectl -n kgw-a rollout status deploy/kgateway --timeout=180s # fails fast if the rendered name changes
kubectl apply -f test/e2e/fixtures/base.yaml
if [[ "${EXPLORER:-1}" == 1 ]]; then
  # Self-signed certificate for edge's https listener; the key never outlives this block.
  d=$(mktemp -d)
  openssl req -x509 -newkey rsa:2048 -nodes -days 30 -subj "/CN=*.example.com" \
    -keyout "$d/tls.key" -out "$d/tls.crt" >/dev/null 2>&1
  kubectl -n infra create secret tls edge-tls --cert "$d/tls.crt" --key "$d/tls.key" >/dev/null
  rm -rf "$d"
  kubectl apply -f test/e2e/fixtures/explorer.yaml
  # The TCPRoute on v1 where the bundle serves it, else v1alpha2.
  served=$(kubectl get crd tcproutes.gateway.networking.k8s.io -o jsonpath='{.spec.versions[?(@.served==true)].name}')
  tcp=v1alpha2
  if grep -qw v1 <<<"$served"; then
    tcp=v1
  fi
  kubectl apply -f "test/e2e/fixtures/explorer-tcproute-$tcp.yaml"
  kubectl apply -f test/e2e/fixtures/analyzer.yaml
  kubectl apply -f test/e2e/fixtures/backends.yaml
  sed "s|/TCPROUTE_VERSION$|/$tcp|" test/e2e/fixtures/traffic.yaml | kubectl apply -f -
fi
if [[ "${NACK:-}" == 1 ]]; then
  kubectl apply -f test/e2e/fixtures/nack.yaml
fi
if [[ "${SECOND_INSTALL:-}" == 1 ]]; then
  helm install kgateway oci://cr.kgateway.dev/kgateway-dev/charts/kgateway --version "$KGW_VERSION" -n kgw-b --create-namespace \
    --set-json 'discoveryNamespaceSelectors=[{"matchLabels":{"team":"b"}}]' --wait
  kubectl -n kgw-b rollout status deploy/kgateway --timeout=180s
  kubectl apply -f test/e2e/fixtures/second-install.yaml
fi
# The Gateway must exist before kubectl wait can watch it.
poll 60 kubectl -n infra get gateway/edge
kubectl -n infra wait --for=condition=Programmed gateway/edge --timeout=180s
if [[ "${NACK:-}" == 1 ]]; then
  kubectl -n infra wait --for=condition=Programmed gateway/nack --timeout=180s
  kubectl -n infra rollout status deploy/nack --timeout=180s
fi
kubectl -n shop rollout status deploy/httpbin --timeout=180s
if [[ "${EXPLORER:-1}" == 1 ]]; then
  kubectl -n traffic rollout status deploy/echo --timeout=180s
fi

docker build -t snellius:e2e --build-arg VERSION=e2e .
kind load docker-image snellius:e2e --name "$CLUSTER"
ui_args=()
if [[ -n "${UI_EXTRA_ARGS:-}" ]]; then
  read -ra extra <<<"$UI_EXTRA_ARGS"
  ui_args=(--set "extraArgs={$(IFS=,; echo "${extra[*]}")}")
fi
if [[ "${CONTROLLER_LOGS:-}" == 1 ]]; then
  ui_args+=(--set diagnostics.controllerLogs=true --set "diagnostics.controllerLogsNamespaces={kgw-a}")
fi
if [[ "${SECURE:-}" == 1 ]]; then
  kubectl create namespace snellius
  kubectl -n snellius create secret generic snellius-oidc --from-file=client-secret="$OIDC_DIR/client-secret" >/dev/null
  kubectl -n snellius create configmap snellius-oidc-ca --from-file=ca.pem="$OIDC_DIR/ca.pem"
  # Plain-HTTP redirect on loopback, so the browser and the Go client use the port-forward.
  ui_args+=(--set auth.mode=secure --set "auth.oidc.issuerURL=$ISSUER_URL" --set "auth.oidc.clientID=$CLIENT_ID"
    --set "auth.oidc.redirectURL=http://127.0.0.1:$PORT/auth/callback"
    --set auth.oidc.clientSecret.existingSecret=snellius-oidc --set auth.oidc.ca.existingConfigMap=snellius-oidc-ca
    --set auth.session.cookieSecure=false
    --set-json "hostAliases=[{\"ip\":\"$ISSUER_IP\",\"hostnames\":[\"$ISSUER_HOST\"]}]")
fi
# ${a[@]+...} keeps an empty array safe under set -u on bash 3.
helm install ui deploy/helm/snellius -n snellius --create-namespace \
  --set image.repository=snellius --set image.tag=e2e --set image.pullPolicy=Never ${ui_args[@]+"${ui_args[@]}"} --wait
kubectl -n snellius rollout status deploy/ui-snellius --timeout=120s

nohup kubectl -n snellius port-forward svc/ui-snellius "$PORT:80" >"$E2E_DIR/port-forward.log" 2>&1 &
echo $! >"$E2E_DIR/port-forward.pid"
if ! poll 60 curl -fsS "http://127.0.0.1:$PORT/readyz"; then
  cat "$E2E_DIR/port-forward.log" >&2
  exit 1
fi
echo "UI at http://127.0.0.1:$PORT (kubeconfig: $KUBECONFIG)"
