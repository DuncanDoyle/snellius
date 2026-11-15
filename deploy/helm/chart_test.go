// Package helm tests the rendered chart. It needs the helm CLI on PATH; tests
// skip without it unless REQUIRE_HELM=1.
package helm

import (
	"bytes"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"

	"github.com/DuncanDoyle/snellius/pkg/cache"
	"github.com/DuncanDoyle/snellius/pkg/kinds"
)

func render(t *testing.T, args ...string) []map[string]any {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		if os.Getenv("REQUIRE_HELM") == "1" {
			t.Fatalf("helm not on PATH and REQUIRE_HELM=1: %v", err)
		}
		t.Skip("helm not on PATH")
	}
	out, err := exec.Command("helm", append([]string{"template", "ui", "./snellius", "-n", "snellius"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, out)
	}
	var docs []map[string]any
	for _, d := range bytes.Split(out, []byte("\n---")) {
		var m map[string]any
		if err := yaml.Unmarshal(d, &m); err == nil && m != nil {
			docs = append(docs, m)
		}
	}
	return docs
}

func kind(docs []map[string]any, k string) []map[string]any {
	var out []map[string]any
	for _, d := range docs {
		if d["kind"] == k {
			out = append(out, d)
		}
	}
	return out
}

func clusterRole(t *testing.T, args ...string) rbacv1.ClusterRole {
	t.Helper()
	crs := kind(render(t, args...), "ClusterRole")
	if len(crs) != 1 {
		t.Fatalf("want 1 ClusterRole, got %d", len(crs))
	}
	b, _ := yaml.Marshal(crs[0])
	var cr rbacv1.ClusterRole
	if err := yaml.Unmarshal(b, &cr); err != nil {
		t.Fatal(err)
	}
	return cr
}

// grants reports whether a rule allows verb on group/resource cluster-wide,
// honouring "*" in groups, resources and verbs. Rules with resourceNames do not count.
func grants(rules []rbacv1.PolicyRule, group, resource, verb string) bool {
	match := func(list []string, v string) bool { return slices.Contains(list, v) || slices.Contains(list, "*") }
	for _, r := range rules {
		if len(r.ResourceNames) == 0 && match(r.APIGroups, group) && match(r.Resources, resource) && match(r.Verbs, verb) {
			return true
		}
	}
	return false
}

func TestGrantsHandlesWildcards(t *testing.T) {
	rules := []rbacv1.PolicyRule{
		{APIGroups: []string{"gateway.kgateway.dev"}, Resources: []string{"*"}, Verbs: []string{"get", "list", "watch"}},
		{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get"}},
		{APIGroups: []string{"apps"}, Resources: []string{"deployments"}, ResourceNames: []string{"one"}, Verbs: []string{"list"}},
	}
	for _, c := range []struct {
		group, resource, verb string
		want                  bool
	}{
		{"gateway.kgateway.dev", "gatewayparameters", "watch", true},
		{"gateway.kgateway.dev", "trafficpolicies", "delete", false},
		{"gateway.networking.k8s.io", "gateways", "get", false},
		{"", "pods", "get", true},
		{"", "pods", "list", false},
		{"apps", "deployments", "list", false},
	} {
		if got := grants(rules, c.group, c.resource, c.verb); got != c.want {
			t.Errorf("grants(%q, %q, %q) = %v, want %v", c.group, c.resource, c.verb, got, c.want)
		}
	}
}

// Checks the default render only; rbac.extraRules is the operator's choice.
func TestClusterRoleIsReadOnlyWithoutSecretsOrProxy(t *testing.T) {
	cr := clusterRole(t)
	for _, r := range cr.Rules {
		if slices.Contains(r.APIGroups, "*") {
			t.Errorf("apiGroup \"*\" must not be granted")
		}
		for _, v := range r.Verbs {
			if !slices.Contains([]string{"get", "list", "watch"}, v) {
				t.Errorf("verb %q is not read-only (D-001)", v)
			}
		}
		for _, res := range r.Resources {
			if res == "secrets" || res == "configmaps" || strings.Contains(res, "/proxy") || res == "*" && slices.Contains(r.APIGroups, "") {
				t.Errorf("resource %q must not be granted", res)
			}
		}
	}
	for _, res := range []string{"secrets", "configmaps", "pods/proxy", "services/proxy", "nodes/proxy"} {
		if grants(cr.Rules, "", res, "get") {
			t.Errorf("core %q must not be readable", res)
		}
	}
}

// TestClusterRoleGrantsEveryReadKind derives the kinds from the backend's
// registry, so a new kind without RBAC fails here.
func TestClusterRoleGrantsEveryReadKind(t *testing.T) {
	cr := clusterRole(t)
	want := []schema.GroupVersionResource{cache.NamespacesGVR, cache.PodsGVR, cache.DeploymentsGVR}
	for _, d := range kinds.Upstream() {
		want = append(want, schema.GroupVersionResource{Group: d.Group, Resource: d.Resource})
	}
	if !slices.ContainsFunc(want, func(g schema.GroupVersionResource) bool { return g.Resource == "gatewayparameters" }) {
		t.Fatal("kind registry lost gatewayparameters")
	}
	for _, g := range want {
		for _, v := range []string{"get", "list", "watch"} {
			if !grants(cr.Rules, g.Group, g.Resource, v) {
				t.Errorf("ClusterRole does not grant %s on %s (group %q)", v, g.Resource, g.Group)
			}
		}
	}
}

// TestClusterRoleCoversBackends: the explorer reads kgateway Backends.
func TestClusterRoleCoversBackends(t *testing.T) {
	cr := clusterRole(t)
	i := slices.IndexFunc(kinds.Upstream(), func(d kinds.Descriptor) bool { return d.Category == kinds.CategoryBackend })
	if i < 0 {
		t.Fatal("kind registry has no Backend")
	}
	d := kinds.Upstream()[i]
	for _, v := range []string{"get", "list", "watch"} {
		if !grants(cr.Rules, d.Group, d.Resource, v) {
			t.Errorf("ClusterRole does not grant %s on %s.%s", v, d.Resource, d.Group)
		}
	}
}

// TestClusterRoleCoversListOnlyKinds: ReferenceGrants, DirectResponses and
// GatewayExtensions are listed and shown, so they are readable (D-056).
func TestClusterRoleCoversListOnlyKinds(t *testing.T) {
	cr := clusterRole(t)
	for _, g := range []schema.GroupResource{{Group: "gateway.networking.k8s.io", Resource: "referencegrants"},
		{Group: "gateway.kgateway.dev", Resource: "directresponses"}, {Group: "gateway.kgateway.dev", Resource: "gatewayextensions"}} {
		for _, v := range []string{"get", "list", "watch"} {
			if !grants(cr.Rules, g.Group, g.Resource, v) {
				t.Errorf("ClusterRole does not grant %s on %s", v, g)
			}
		}
	}
}

// grantsAll reports whether the rules allow get, list and watch on group/resource.
func grantsAll(rules []rbacv1.PolicyRule, group, resource string) bool {
	for _, v := range []string{"get", "list", "watch"} {
		if !grants(rules, group, resource, v) {
			return false
		}
	}
	return true
}

func TestEventsRuleFollowsToggle(t *testing.T) {
	if !grantsAll(clusterRole(t).Rules, "", "events") {
		t.Error("events must be readable by default")
	}
	if grants(clusterRole(t, "--set", "diagnostics.events=false").Rules, "", "events", "get") {
		t.Error("diagnostics.events=false must drop the events rule")
	}
}

func TestCRDRuleFollowsToggle(t *testing.T) {
	if !grantsAll(clusterRole(t).Rules, "apiextensions.k8s.io", "customresourcedefinitions") {
		t.Error("CRDs must be readable by default")
	}
	if grants(clusterRole(t, "--set", "diagnostics.crds=false").Rules, "apiextensions.k8s.io", "customresourcedefinitions", "get") {
		t.Error("diagnostics.crds=false must drop the CRD rule")
	}
}

func TestEndpointsRulesFollowToggle(t *testing.T) {
	on := clusterRole(t).Rules
	if !grantsAll(on, "", "services") || !grantsAll(on, "discovery.k8s.io", "endpointslices") {
		t.Error("services and endpointslices must be readable by default")
	}
	off := clusterRole(t, "--set", "diagnostics.endpoints=false").Rules
	if grants(off, "", "services", "get") || grants(off, "discovery.k8s.io", "endpointslices", "get") {
		t.Error("diagnostics.endpoints=false must drop both rules")
	}
}

// Args every default (local mode) render starts with.
var localBase = []string{"--listen=127.0.0.1:8080", "--probe-listen=:8081", "--auth-mode=local",
	"--audit-levels=security,agent,reads,summaries"}

// Defaults match the binary's, so only a non-default toggle becomes a flag.
func TestTogglesPassedAsFlags(t *testing.T) {
	if got, want := containerArgs(t), localBase; !slices.Equal(got, want) {
		t.Errorf("default args = %v, want %v", got, want)
	}
	got := containerArgs(t,
		"--set", "diagnostics.events=false", "--set", "diagnostics.crds=false",
		"--set", "diagnostics.proxyStats=false", "--set", "diagnostics.endpoints=false",
		"--set", "diagnostics.controllerLogs=true")
	want := append(slices.Clone(localBase), "--diagnostics-events=false", "--diagnostics-crds=false",
		"--diagnostics-proxy-stats=false", "--diagnostics-endpoints=false", "--diagnostics-controller-logs=true")
	if !slices.Equal(got, want) {
		t.Errorf("args = %v, want %v", got, want)
	}
}

// pods/log is never in the ClusterRole; controllerLogs adds a Role and a
// RoleBinding to the UI's ServiceAccount only in each listed namespace.
func TestControllerLogsRoleIsOptInAndNamespaced(t *testing.T) {
	if r := kind(render(t), "Role"); len(r) != 0 {
		t.Errorf("no Role by default, got %d", len(r))
	}
	off := render(t, "--set", "diagnostics.controllerLogsNamespaces={kgateway-system,other}")
	if n := len(kind(off, "Role")) + len(kind(off, "RoleBinding")); n != 0 {
		t.Errorf("namespaces alone must not render a Role or RoleBinding, got %d", n)
	}
	args := []string{"--set", "diagnostics.controllerLogs=true", "--set", "diagnostics.controllerLogsNamespaces={kgateway-system,other}"}
	docs := render(t, args...)
	if grants(clusterRole(t, args...).Rules, "", "pods/log", "get") {
		t.Error("pods/log must not be in the ClusterRole")
	}
	var seen []string
	for _, d := range kind(docs, "Role") {
		b, _ := yaml.Marshal(d)
		var r rbacv1.Role
		if err := yaml.Unmarshal(b, &r); err != nil {
			t.Fatal(err)
		}
		if len(r.Rules) != 1 || !slices.Equal(r.Rules[0].Resources, []string{"pods/log"}) || !slices.Equal(r.Rules[0].Verbs, []string{"get"}) {
			t.Errorf("Role %s/%s must grant only get on pods/log: %+v", r.Namespace, r.Name, r.Rules)
		}
		seen = append(seen, r.Namespace)
	}
	slices.Sort(seen)
	if !slices.Equal(seen, []string{"kgateway-system", "other"}) {
		t.Errorf("Role namespaces = %v", seen)
	}
	var bound []string
	for _, d := range kind(docs, "RoleBinding") {
		b, _ := yaml.Marshal(d)
		var rb rbacv1.RoleBinding
		if err := yaml.Unmarshal(b, &rb); err != nil {
			t.Fatal(err)
		}
		if rb.RoleRef.Kind != "Role" || rb.RoleRef.Name != "ui-snellius-logs" {
			t.Errorf("RoleBinding %s/%s must bind the logs Role: %+v", rb.Namespace, rb.Name, rb.RoleRef)
		}
		if want := []rbacv1.Subject{{Kind: "ServiceAccount", Name: "ui-snellius", Namespace: "snellius"}}; !slices.Equal(rb.Subjects, want) {
			t.Errorf("RoleBinding %s/%s subjects = %+v", rb.Namespace, rb.Name, rb.Subjects)
		}
		bound = append(bound, rb.Namespace)
	}
	slices.Sort(bound)
	if !slices.Equal(bound, []string{"kgateway-system", "other"}) {
		t.Errorf("RoleBinding namespaces = %v", bound)
	}
}

func TestHTTPRouteIsOptIn(t *testing.T) {
	if n := len(kind(render(t), "HTTPRoute")); n != 0 {
		t.Fatalf("HTTPRoute must be off by default, got %d", n)
	}
	docs := render(t, append(secureSet, "--set", "httpRoute.enabled=true", "--set", "httpRoute.parentRefs[0].name=edge",
		"--set", "httpRoute.parentRefs[0].namespace=infra", "--set", "httpRoute.hostnames[0]=ui.example.com")...)
	routes := kind(docs, "HTTPRoute")
	if len(routes) != 1 {
		t.Fatalf("want HTTPRoute when enabled, got %d", len(routes))
	}
	b, _ := yaml.Marshal(routes[0])
	for _, want := range []string{"name: edge", "namespace: infra", "ui.example.com", "name: ui-snellius", "port: 80"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("HTTPRoute lacks %q:\n%s", want, b)
		}
	}
}

func TestHTTPRouteRequiresParentRefs(t *testing.T) {
	render(t) // skips without helm
	out, err := exec.Command("helm", append([]string{"template", "ui", "./snellius", "-n", "snellius"}, append(secureSet, "--set", "httpRoute.enabled=true")...)...).CombinedOutput()
	if err == nil {
		t.Fatalf("want render failure without parentRefs, got:\n%s", out)
	}
	if !strings.Contains(string(out), "httpRoute.parentRefs is required") {
		t.Fatalf("unclear error:\n%s", out)
	}
}

func TestExistingServiceAccountRequiresName(t *testing.T) {
	render(t) // skips without helm
	out, err := exec.Command("helm", "template", "ui", "./snellius", "-n", "snellius", "--set", "serviceAccount.create=false").CombinedOutput()
	if err == nil {
		t.Fatalf("want render failure without serviceAccount.name, got:\n%s", out)
	}
	if !strings.Contains(string(out), "serviceAccount.name is required") {
		t.Fatalf("unclear error:\n%s", out)
	}
	ds := kind(render(t, "--set", "serviceAccount.create=false", "--set", "serviceAccount.name=existing"), "Deployment")
	if len(ds) != 1 {
		t.Fatalf("want 1 Deployment, got %d", len(ds))
	}
	spec := ds[0]["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	if spec["serviceAccountName"] != "existing" {
		t.Fatalf("serviceAccountName = %v, want existing", spec["serviceAccountName"])
	}
}

func TestPinnedInstallIsPassedAsFlag(t *testing.T) {
	deploy := func(args ...string) string {
		ds := kind(render(t, args...), "Deployment")
		if len(ds) != 1 {
			t.Fatalf("want 1 Deployment, got %d", len(ds))
		}
		b, _ := yaml.Marshal(ds[0])
		return string(b)
	}
	if s := deploy(); strings.Contains(s, "--install") {
		t.Errorf("no pin by default:\n%s", s)
	}
	if s := deploy("--set", "install.pin=kgw/kgateway"); !strings.Contains(s, "--install=kgw/kgateway") {
		t.Fatalf("pin flag missing:\n%s", s)
	}
}

// Class-only installs are off by default, so a kgateway user sees no new installs.
func TestClassOnlyInstallsPassedAsFlag(t *testing.T) {
	deploy := func(args ...string) string {
		ds := kind(render(t, args...), "Deployment")
		if len(ds) != 1 {
			t.Fatalf("want 1 Deployment, got %d", len(ds))
		}
		b, _ := yaml.Marshal(ds[0])
		return string(b)
	}
	if s := deploy(); strings.Contains(s, "--class-only-installs") {
		t.Errorf("class-only installs must be off by default:\n%s", s)
	}
	if s := deploy("--set", "installs.classOnly=true"); !strings.Contains(s, "--class-only-installs=true") {
		t.Fatalf("class-only flag missing:\n%s", s)
	}
}

func TestPodIsHardened(t *testing.T) {
	ds := kind(render(t), "Deployment")
	if len(ds) != 1 {
		t.Fatalf("want 1 Deployment, got %d", len(ds))
	}
	b, _ := yaml.Marshal(ds[0])
	for _, want := range []string{"runAsNonRoot: true", "readOnlyRootFilesystem: true", "allowPrivilegeEscalation: false", "type: RuntimeDefault", "- ALL", "path: /readyz", "path: /healthz", "--listen=127.0.0.1:8080", "containerPort: 8080", "containerPort: 8081"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("deployment lacks %q", want)
		}
	}
}

// containerArgs is the UI container's args as rendered with args.
func containerArgs(t *testing.T, args ...string) []string {
	t.Helper()
	ds := kind(render(t, args...), "Deployment")
	if len(ds) != 1 {
		t.Fatalf("want 1 Deployment, got %d", len(ds))
	}
	b, _ := yaml.Marshal(ds[0])
	var d struct {
		Spec struct {
			Template struct {
				Spec struct{ Containers []struct{ Args []string } }
			}
		}
	}
	if err := yaml.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	return d.Spec.Template.Spec.Containers[0].Args
}

func TestExtraArgsAppended(t *testing.T) {
	if got, want := containerArgs(t), localBase; !slices.Equal(got, want) {
		t.Errorf("default args = %v, want %v", got, want)
	}
	got := containerArgs(t, "--set", "install.pin=kgw/kgateway", "--set", "extraArgs={--poll-interval=10m}")
	if want := append(slices.Clone(localBase), "--install=kgw/kgateway", "--poll-interval=10m"); !slices.Equal(got, want) {
		t.Errorf("args = %v, want %v", got, want)
	}
}

func TestDelegationEdgeCapPassedAsFlag(t *testing.T) {
	if got := containerArgs(t); slices.ContainsFunc(got, func(a string) bool { return strings.HasPrefix(a, "--delegation-edge-cap") }) {
		t.Errorf("no edge cap flag by default: %v", got)
	}
	got := containerArgs(t, "--set", "delegation.edgeCap=20000", "--set", "extraArgs={--poll-interval=10m}")
	if want := append(slices.Clone(localBase), "--delegation-edge-cap=20000", "--poll-interval=10m"); !slices.Equal(got, want) {
		t.Errorf("args = %v, want %v", got, want)
	}
}

// containerEnv is the UI container's env as rendered with args.
func containerEnv(t *testing.T, args ...string) []corev1.EnvVar {
	t.Helper()
	ds := kind(render(t, args...), "Deployment")
	if len(ds) != 1 {
		t.Fatalf("want 1 Deployment, got %d", len(ds))
	}
	b, _ := yaml.Marshal(ds[0])
	var d appsv1.Deployment
	if err := yaml.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	return d.Spec.Template.Spec.Containers[0].Env
}

// The container's memory limit reaches the binary as SNELLIUS_MEMORY_LIMIT, and
// the chart sets GOMEMLIMIT only from the opt-in memoryLimit (D-025, D-043).
func TestMemoryLimitEnv(t *testing.T) {
	named := func(n string) func(corev1.EnvVar) bool { return func(e corev1.EnvVar) bool { return e.Name == n } }
	env := containerEnv(t)
	i := slices.IndexFunc(env, named("SNELLIUS_MEMORY_LIMIT"))
	if i < 0 {
		t.Fatalf("no SNELLIUS_MEMORY_LIMIT with the default 512Mi limit: %+v", env)
	}
	f := env[i].ValueFrom
	if env[i].Value != "" || f == nil || f.ResourceFieldRef == nil || f.ResourceFieldRef.Resource != "limits.memory" ||
		f.ResourceFieldRef.ContainerName != "snellius" || f.ResourceFieldRef.Divisor.String() != "1" {
		t.Fatalf("SNELLIUS_MEMORY_LIMIT must be the container's limits.memory in bytes: %+v", env[i])
	}
	if slices.ContainsFunc(env, named("GOMEMLIMIT")) {
		t.Fatalf("the chart must not set GOMEMLIMIT: %+v", env)
	}
	env = containerEnv(t, "--set", "resources.limits.memory=null")
	if slices.ContainsFunc(env, named("SNELLIUS_MEMORY_LIMIT")) {
		t.Fatalf("SNELLIUS_MEMORY_LIMIT must be absent without a memory limit: %+v", env)
	}
	for _, args := range [][]string{{"--set", "memoryLimit=300MiB"}, {"--set", "memoryLimit=300MiB", "--set", "resources.limits.memory=null"}} {
		env = containerEnv(t, args...)
		if i := slices.IndexFunc(env, named("GOMEMLIMIT")); i < 0 || env[i].Value != "300MiB" {
			t.Fatalf("%v: want GOMEMLIMIT=300MiB: %+v", args, env)
		}
	}
}

// TestExtraRulesAppended: rbac.extraRules is the operator's choice and can
// widen RBAC; the default render stays read-only (see the read-only test).
func TestExtraRulesAppended(t *testing.T) {
	base := len(clusterRole(t).Rules)
	docs := render(t, "--set-json", `rbac.extraRules=[{"apiGroups":["example.dev"],"resources":["widgets"],"verbs":["get","delete"]}]`)
	crs := kind(docs, "ClusterRole")
	if len(crs) != 1 {
		t.Fatalf("want 1 ClusterRole, got %d", len(crs))
	}
	b, _ := yaml.Marshal(crs[0])
	var cr rbacv1.ClusterRole
	if err := yaml.Unmarshal(b, &cr); err != nil {
		t.Fatal(err)
	}
	if len(cr.Rules) != base+1 {
		t.Fatalf("rules = %d, want %d", len(cr.Rules), base+1)
	}
	last := cr.Rules[len(cr.Rules)-1]
	if !slices.Equal(last.Resources, []string{"widgets"}) || !slices.Equal(last.Verbs, []string{"get", "delete"}) {
		t.Errorf("extra rule not appended last: %+v", last)
	}
}

func TestExtraEnvVolumesAndContainers(t *testing.T) {
	deployment := func(args ...string) appsv1.Deployment {
		ds := kind(render(t, args...), "Deployment")
		if len(ds) != 1 {
			t.Fatalf("want 1 Deployment, got %d", len(ds))
		}
		b, _ := yaml.Marshal(ds[0])
		var d appsv1.Deployment
		if err := yaml.Unmarshal(b, &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	def := deployment().Spec.Template.Spec
	if len(def.Containers) != 1 || len(def.Volumes) != 0 || len(def.Containers[0].VolumeMounts) != 0 {
		t.Fatalf("defaults must add no containers, volumes or mounts: %+v", def)
	}
	d := deployment(
		"--set-json", `extraEnv=[{"name":"ADDON","value":"1"}]`,
		"--set-json", `extraVolumes=[{"name":"data","emptyDir":{}}]`,
		"--set-json", `extraVolumeMounts=[{"name":"data","mountPath":"/data"}]`,
		"--set-json", `extraContainers=[{"name":"addon","image":"example/addon:1"}]`,
	).Spec.Template.Spec
	if len(d.Containers) != 2 || d.Containers[0].Name != "snellius" || d.Containers[1].Name != "addon" {
		t.Fatalf("containers = %+v", d.Containers)
	}
	if len(d.Containers[1].VolumeMounts) != 0 {
		t.Errorf("the sidecar must get no volumeMounts: %+v", d.Containers[1].VolumeMounts)
	}
	ui := d.Containers[0]
	if !slices.ContainsFunc(ui.Env, func(e corev1.EnvVar) bool { return e.Name == "ADDON" && e.Value == "1" }) ||
		!slices.ContainsFunc(ui.Env, func(e corev1.EnvVar) bool { return e.Name == "SNELLIUS_MEMORY_LIMIT" }) {
		t.Errorf("extraEnv must join the memory-limit env: %+v", ui.Env)
	}
	if len(ui.VolumeMounts) != 1 || ui.VolumeMounts[0].MountPath != "/data" {
		t.Errorf("volumeMounts = %+v", ui.VolumeMounts)
	}
	if len(d.Volumes) != 1 || d.Volumes[0].Name != "data" {
		t.Errorf("volumes = %+v", d.Volumes)
	}
	// extraEnv alone, without a memory limit, follows the two downward-API vars.
	env := containerEnv(t, "--set", "resources.limits.memory=null", "--set-json", `extraEnv=[{"name":"ADDON","value":"1"}]`)
	if len(env) != 3 || env[2].Name != "ADDON" {
		t.Errorf("env = %+v", env)
	}
}

// deployment is the rendered Deployment, decoded.
func deployment(t *testing.T, args ...string) appsv1.Deployment {
	t.Helper()
	ds := kind(render(t, args...), "Deployment")
	if len(ds) != 1 {
		t.Fatalf("want 1 Deployment, got %d", len(ds))
	}
	b, _ := yaml.Marshal(ds[0])
	var d appsv1.Deployment
	if err := yaml.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestLocalModeDefaults(t *testing.T) {
	d := deployment(t)
	c := d.Spec.Template.Spec.Containers[0]
	if !slices.Equal(c.Args, localBase) {
		t.Errorf("args = %v, want %v", c.Args, localBase)
	}
	ports := map[string]int32{}
	for _, p := range c.Ports {
		ports[p.Name] = p.ContainerPort
	}
	if ports["http"] != 8080 || ports["probe"] != 8081 {
		t.Errorf("ports = %v, want http 8080 and probe 8081", ports)
	}
	if c.ReadinessProbe.HTTPGet.Port.String() != "probe" || c.ReadinessProbe.HTTPGet.Path != "/readyz" {
		t.Errorf("readiness = %+v", c.ReadinessProbe.HTTPGet)
	}
	if c.LivenessProbe.HTTPGet.Port.String() != "probe" || c.LivenessProbe.HTTPGet.Path != "/healthz" {
		t.Errorf("liveness = %+v", c.LivenessProbe.HTTPGet)
	}
	field := map[string]string{}
	for _, e := range c.Env {
		if e.ValueFrom != nil && e.ValueFrom.FieldRef != nil {
			field[e.Name] = e.ValueFrom.FieldRef.FieldPath
		}
	}
	if field["POD_IP"] != "status.podIP" || field["HOST_IP"] != "status.hostIP" {
		t.Errorf("downward env = %v", field)
	}
	svc := kind(render(t), "Service")
	b, _ := yaml.Marshal(svc[0])
	if !strings.Contains(string(b), "port: 80") || !strings.Contains(string(b), "targetPort: http") {
		t.Errorf("service keeps port 80 -> http:\n%s", b)
	}
}

func TestLocalModeRefusesHTTPRoute(t *testing.T) {
	render(t) // skips without helm
	out, err := exec.Command("helm", "template", "ui", "./snellius", "-n", "snellius", "--set", "httpRoute.enabled=true",
		"--set", "httpRoute.parentRefs[0].name=edge").CombinedOutput()
	if err == nil {
		t.Fatalf("want render failure in local mode, got:\n%s", out)
	}
	if !strings.Contains(string(out), "auth.mode: secure") {
		t.Fatalf("error does not name auth.mode: secure:\n%s", out)
	}
}

func TestSecureModeArgs(t *testing.T) {
	got := containerArgs(t, append(secureSet, "--set", "extraArgs={--poll-interval=10m}")...)
	want := []string{"--listen=:8080", "--probe-listen=:8081", "--auth-mode=secure",
		"--audit-levels=security,agent,reads,summaries",
		"--oidc-issuer-url=https://idp.example.com", "--oidc-client-id=snellius",
		"--oidc-redirect-url=https://ui.example.com/auth/callback", "--poll-interval=10m"}
	if !slices.Equal(got, want) {
		t.Errorf("args = %v, want %v", got, want)
	}
}

// The mesh opt-out is on the pod template only; the selector must not change.
func TestMeshOptOutInLocalMode(t *testing.T) {
	d := deployment(t)
	pod := d.Spec.Template.ObjectMeta
	if pod.Annotations["sidecar.istio.io/inject"] != "false" || pod.Labels["istio.io/dataplane-mode"] != "none" {
		t.Errorf("local pod meta = %v %v", pod.Annotations, pod.Labels)
	}
	if _, ok := d.Spec.Selector.MatchLabels["istio.io/dataplane-mode"]; ok {
		t.Errorf("selector must not carry the mesh label: %v", d.Spec.Selector.MatchLabels)
	}
	s := deployment(t, secureSet...).Spec.Template.ObjectMeta
	if _, ok := s.Annotations["sidecar.istio.io/inject"]; ok {
		t.Errorf("secure mode must not opt out: %v", s.Annotations)
	}
	if _, ok := s.Labels["istio.io/dataplane-mode"]; ok {
		t.Errorf("secure mode must not opt out: %v", s.Labels)
	}
}

func TestValuesWiredToFlags(t *testing.T) {
	got := containerArgs(t, "--set", "auth.allowedHosts={a.example.com,b.example.com}",
		"--set", "audit.levels={security,reads}")
	want := []string{"--listen=127.0.0.1:8080", "--probe-listen=:8081", "--auth-mode=local",
		"--allowed-hosts=a.example.com,b.example.com", "--audit-levels=security,reads"}
	if !slices.Equal(got, want) {
		t.Errorf("args = %v, want %v", got, want)
	}
}

// --reuse-values from an older release has no auth or audit keys at all.
func TestRendersWithoutAuthAndAuditValues(t *testing.T) {
	got := containerArgs(t, "--set", "auth=null", "--set", "audit=null")
	if !slices.Equal(got, localBase) {
		t.Errorf("args = %v, want %v", got, localBase)
	}
}

// secureSet is the minimum that renders secure mode.
var secureSet = []string{"--set", "auth.mode=secure",
	"--set", "auth.oidc.issuerURL=https://idp.example.com",
	"--set", "auth.oidc.clientID=snellius",
	"--set", "auth.oidc.redirectURL=https://ui.example.com/auth/callback"}

func withSecure(extra ...string) []string { return append(slices.Clone(secureSet), extra...) }

func TestSecureModeRendersEveryFlag(t *testing.T) {
	got := containerArgs(t, withSecure(
		"--set", "auth.oidc.scopes={openid,email}",
		"--set", "auth.oidc.clientSecret.existingSecret=idp",
		"--set", "auth.oidc.ca.existingConfigMap=idp-ca",
		"--set", "auth.session.idle=15m", "--set", "auth.session.absolute=4h",
		"--set", "auth.session.cookieSecure=false",
		"--set", "auth.trustedProxyCIDRs={10.0.0.0/8,192.168.0.0/16}")...)
	for _, want := range []string{
		"--oidc-issuer-url=https://idp.example.com", "--oidc-client-id=snellius",
		"--oidc-redirect-url=https://ui.example.com/auth/callback", "--oidc-scopes=openid,email",
		"--oidc-client-secret-file=/etc/snellius/oidc/secret/client-secret", "--oidc-ca-file=/etc/snellius/oidc/ca/ca.pem",
		"--session-idle=15m", "--session-absolute=4h", "--cookie-secure=false",
		"--trusted-proxy-cidrs=10.0.0.0/8,192.168.0.0/16"} {
		if !slices.Contains(got, want) {
			t.Errorf("args lack %q: %v", want, got)
		}
	}
	def := containerArgs(t, secureSet...)
	for _, a := range def {
		for _, no := range []string{"--oidc-scopes", "--oidc-client-secret-file", "--oidc-ca-file", "--session-", "--cookie-secure", "--trusted-proxy-cidrs"} {
			if strings.HasPrefix(a, no) {
				t.Errorf("unset value rendered %q", a)
			}
		}
	}
}

func TestLocalModeIgnoresSecureValues(t *testing.T) {
	got := containerArgs(t, "--set", "auth.oidc.issuerURL=https://idp.example.com", "--set", "auth.session.idle=15m",
		"--set", "auth.trustedProxyCIDRs={10.0.0.0/8}", "--set", "auth.oidc.clientSecret.existingSecret=idp")
	if !slices.Equal(got, localBase) {
		t.Errorf("args = %v, want %v", got, localBase)
	}
	if v := deployment(t, "--set", "auth.oidc.clientSecret.existingSecret=idp").Spec.Template.Spec.Volumes; len(v) != 0 {
		t.Errorf("local mode must not mount the secret: %+v", v)
	}
}

func TestOIDCSecretAndCAMounts(t *testing.T) {
	spec := deployment(t, secureSet...).Spec.Template.Spec
	if len(spec.Volumes) != 0 || len(spec.Containers[0].VolumeMounts) != 0 {
		t.Fatalf("no secret or CA set: want no volumes, got %+v %+v", spec.Volumes, spec.Containers[0].VolumeMounts)
	}
	spec = deployment(t, withSecure(
		"--set", "auth.oidc.clientSecret.existingSecret=idp", "--set", "auth.oidc.clientSecret.key=sec",
		"--set", "auth.oidc.ca.existingConfigMap=idp-ca",
		"--set-json", `extraVolumes=[{"name":"data","emptyDir":{}}]`,
		"--set-json", `extraVolumeMounts=[{"name":"data","mountPath":"/data"}]`)...).Spec.Template.Spec
	mounts := map[string]corev1.VolumeMount{}
	for _, m := range spec.Containers[0].VolumeMounts {
		mounts[m.MountPath] = m
	}
	if len(mounts) != 3 || mounts["/data"].Name != "data" {
		t.Fatalf("mounts must join extraVolumeMounts: %+v", spec.Containers[0].VolumeMounts)
	}
	sm := mounts["/etc/snellius/oidc/secret"]
	if !sm.ReadOnly || sm.SubPath != "" {
		t.Errorf("secret mount must be read-only and a directory without subPath: %+v", sm)
	}
	if cm := mounts["/etc/snellius/oidc/ca"]; !cm.ReadOnly || cm.SubPath != "" {
		t.Errorf("CA mount must be read-only without subPath: %+v", mounts)
	}
	vols := map[string]corev1.Volume{}
	for _, v := range spec.Volumes {
		vols[v.Name] = v
	}
	sv := vols[sm.Name]
	if sv.Secret == nil || sv.Secret.SecretName != "idp" || len(sv.Secret.Items) != 1 || sv.Secret.Items[0].Key != "sec" {
		t.Errorf("secret volume = %+v", sv)
	}
	cv := vols[mounts["/etc/snellius/oidc/ca"].Name]
	if cv.ConfigMap == nil || cv.ConfigMap.Name != "idp-ca" || len(cv.ConfigMap.Items) != 1 || cv.ConfigMap.Items[0].Key != "ca.pem" {
		t.Errorf("CA volume = %+v", cv)
	}
	if len(spec.Volumes) != 3 || vols["data"].Name == "" {
		t.Errorf("volumes must join extraVolumes: %+v", spec.Volumes)
	}
}

func TestSecureModeRequiresOIDCValues(t *testing.T) {
	render(t) // skips without helm
	for _, name := range []string{"issuerURL", "clientID", "redirectURL"} {
		var args []string
		for i := 0; i < len(secureSet); i += 2 {
			if !strings.Contains(secureSet[i+1], "auth.oidc."+name+"=") {
				args = append(args, secureSet[i], secureSet[i+1])
			}
		}
		out, err := exec.Command("helm", append([]string{"template", "ui", "./snellius", "-n", "snellius"}, args...)...).CombinedOutput()
		if err == nil || !strings.Contains(string(out), "auth.oidc."+name+" is required") {
			t.Errorf("%s: want a failure naming the value, got err=%v\n%s", name, err, out)
		}
	}
}

// TokenReview and SubjectAccessReview create are granted in secure mode only, each in its own narrow rule.
func TestReviewRulesOnlyInSecureMode(t *testing.T) {
	reviews := map[string]string{"tokenreviews": "authentication.k8s.io", "subjectaccessreviews": "authorization.k8s.io"}
	local, cr := clusterRole(t), clusterRole(t, secureSet...)
	for res, group := range reviews {
		if grants(local.Rules, group, res, "create") {
			t.Errorf("local mode must not grant %s", res)
		}
		if !grants(cr.Rules, group, res, "create") {
			t.Errorf("secure mode must grant %s create", res)
		}
	}
	for _, r := range cr.Rules {
		if len(r.Resources) == 1 && reviews[r.Resources[0]] != "" {
			if !slices.Equal(r.Verbs, []string{"create"}) || !slices.Equal(r.APIGroups, []string{reviews[r.Resources[0]]}) {
				t.Errorf("%s rule too wide: %+v", r.Resources[0], r)
			}
			continue
		}
		for _, v := range r.Verbs {
			if !slices.Contains([]string{"get", "list", "watch"}, v) {
				t.Errorf("verb %q outside the review rules: %+v", v, r)
			}
		}
		if slices.Contains(r.Resources, "secrets") {
			t.Errorf("secrets must not be granted")
		}
	}
}

func TestHostAliases(t *testing.T) {
	for _, args := range [][]string{nil, secureSet} {
		if h := deployment(t, args...).Spec.Template.Spec.HostAliases; len(h) != 0 {
			t.Errorf("default hostAliases = %+v", h)
		}
		h := deployment(t, append(slices.Clone(args), "--set-json", `hostAliases=[{"ip":"10.1.2.3","hostnames":["idp.example.com"]}]`)...).Spec.Template.Spec.HostAliases
		if len(h) != 1 || h[0].IP != "10.1.2.3" || h[0].Hostnames[0] != "idp.example.com" {
			t.Errorf("hostAliases = %+v", h)
		}
	}
}

func TestSecureNotes(t *testing.T) {
	render(t) // skips without helm
	out, err := exec.Command("helm", append([]string{"install", "ui", "./snellius", "-n", "snellius", "--dry-run=client"}, secureSet...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm install --dry-run: %v\n%s", err, out)
	}
	notes := string(out[strings.Index(string(out), "NOTES:"):])
	for _, want := range []string{"https://ui.example.com/auth/callback", "https://ui.example.com/", "client ID \"snellius\"", "kubectl auth whoami",
		"reused for 60s (auth.authz.cacheTTL)", "Denied checks are reused for 5m"} {
		if !strings.Contains(notes, want) {
			t.Errorf("NOTES lack %q:\n%s", want, notes)
		}
	}
}

// --reuse-values from an older release has none of the new keys.
func TestRendersWithoutOIDCValues(t *testing.T) {
	got := containerArgs(t, "--set", "auth.oidc=null", "--set", "auth.session=null", "--set", "auth.authz=null", "--set", "auth.trustedProxyCIDRs=null",
		"--set", "hostAliases=null")
	if !slices.Equal(got, localBase) {
		t.Errorf("args = %v, want %v", got, localBase)
	}
}

// --reuse-values from a release without auth.authz still renders secure mode, with the binary's TTL.
func TestRendersWithoutAuthzValues(t *testing.T) {
	got := containerArgs(t, withSecure("--set", "auth.authz=null")...)
	if !slices.Equal(got, containerArgs(t, secureSet...)) {
		t.Errorf("args = %v", got)
	}
	for _, a := range got {
		if strings.HasPrefix(a, "--authz-cache-ttl") {
			t.Errorf("unset TTL rendered %q", a)
		}
	}
}

// auth.authz.cacheTTL becomes --authz-cache-ttl in secure mode only.
func TestAuthzCacheTTLValue(t *testing.T) {
	if got := containerArgs(t, withSecure("--set", "auth.authz.cacheTTL=5m")...); !slices.Contains(got, "--authz-cache-ttl=5m") {
		t.Errorf("secure args lack --authz-cache-ttl=5m: %v", got)
	}
	if got := containerArgs(t, "--set", "auth.authz.cacheTTL=5m"); !slices.Equal(got, localBase) {
		t.Errorf("local args = %v, want %v", got, localBase)
	}
}

// auth.authz.namedGetScan renders --authz-named-get-scan only when true and in secure mode.
func TestAuthzNamedGetScanValue(t *testing.T) {
	if got := containerArgs(t, withSecure("--set", "auth.authz.namedGetScan=true")...); !slices.Contains(got, "--authz-named-get-scan") {
		t.Errorf("secure args lack --authz-named-get-scan: %v", got)
	}
	for _, args := range [][]string{secureSet, withSecure("--set", "auth.authz.namedGetScan=false"), withSecure("--set", "auth.authz=null"),
		{"--set", "auth.authz.namedGetScan=true"}} {
		if got := containerArgs(t, args...); slices.Contains(got, "--authz-named-get-scan") {
			t.Errorf("%v renders --authz-named-get-scan: %v", args, got)
		}
	}
}

// auth.authz.maxInFlight, rate and burst become their flags in secure mode only; unset, the binary's defaults apply.
func TestAuthzLimitValues(t *testing.T) {
	want := []string{"--authz-max-in-flight=16", "--authz-rate=50", "--authz-burst=8"}
	got := containerArgs(t, withSecure("--set", "auth.authz.maxInFlight=16", "--set", "auth.authz.rate=50", "--set", "auth.authz.burst=8")...)
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("secure args lack %s: %v", w, got)
		}
	}
	for _, args := range [][]string{secureSet, withSecure("--set", "auth.authz=null"), {"--set", "auth.authz.rate=50"}} {
		for _, a := range containerArgs(t, args...) {
			if strings.HasPrefix(a, "--authz-max-in-flight") || strings.HasPrefix(a, "--authz-rate") || strings.HasPrefix(a, "--authz-burst") {
				t.Errorf("%v renders %q", args, a)
			}
		}
	}
}
