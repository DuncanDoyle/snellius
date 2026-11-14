//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"testing"
)

const uiNamespace = "snellius"

// forward port-forwards target (for example "pod/x") port 8080 to a free local
// port, waits until /api/v1/meta answers, and returns the base URL.
func forward(t *testing.T, target string, remote int) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "kubectl", "--kubeconfig", harnessKubeconfig(t), "-n", uiNamespace,
		"port-forward", target, fmt.Sprintf("%d:%d", port, remote))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stderr
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	t.Cleanup(func() { cancel(); <-exited })

	u := fmt.Sprintf("http://127.0.0.1:%d", port)
	eventually(t, func() error {
		select {
		case <-exited:
			t.Fatalf("port-forward %s exited: %s", target, stderr.String())
		default:
		}
		resp, err := http.Get(u + "/api/v1/meta")
		if err != nil {
			return fmt.Errorf("%w; kubectl: %s", err, stderr.String())
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("status %d", resp.StatusCode)
		}
		return nil
	})
	return u
}

// meta returns authMode from base's /api/v1/meta.
func meta(t *testing.T, u string) string {
	t.Helper()
	resp, err := http.Get(u + "/api/v1/meta")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var m struct {
		AuthMode string `json:"authMode"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m.AuthMode
}

func uiPod(t *testing.T) string {
	t.Helper()
	out, err := kubectlRun(t, "-n", uiNamespace, "get", "pod", "-l", "app.kubernetes.io/name=snellius",
		"-o", "jsonpath={.items[0].metadata.name}")
	if err != nil || len(out) == 0 {
		t.Fatalf("UI pod: %v %s", err, out)
	}
	return string(out)
}

// jsonpath reads one field of a UI-namespace object.
func jsonpath(t *testing.T, kind, name, path string) string {
	t.Helper()
	out, err := kubectlRun(t, "-n", uiNamespace, "get", kind, name, "-o", "jsonpath="+path)
	if err != nil || len(out) == 0 {
		t.Fatalf("get %s/%s %s: %v %s", kind, name, path, err, out)
	}
	return string(out)
}

func TestLocalModeReachableThroughForwards(t *testing.T) {
	pod := uiPod(t)
	for name, tc := range map[string]struct {
		target string
		port   int
	}{
		"service": {"svc/ui-snellius", 80},
		"pod":     {"pod/" + pod, 8080},
	} {
		t.Run(name, func(t *testing.T) {
			if got := meta(t, forward(t, tc.target, tc.port)); got != "local" {
				t.Fatalf("authMode = %q, want local", got)
			}
		})
	}
}

func TestLocalModeRejectsForeignHostAndOrigin(t *testing.T) {
	u := forward(t, "svc/ui-snellius", 80)
	for name, tc := range map[string]struct {
		header, value string
		want          int
	}{
		"foreign host":   {"Host", "evil.example", http.StatusMisdirectedRequest},
		"foreign origin": {"Origin", "http://evil.example", http.StatusForbidden},
	} {
		t.Run(name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, u+"/api/v1/meta", nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.header == "Host" {
				req.Host = tc.value // net/http ignores Header["Host"]
			} else {
				req.Header.Set(tc.header, tc.value)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
}

// probeScript prints one "R:<url>=<http code>" line per URL, 000 when the connection failed.
const probeScript = `for u in "$@"; do
  c=$(curl -s -m 5 -o /dev/null -w "%{http_code}" "$u"); echo "R:$u=$c"
done`

// TestLocalModeUnreachableInCluster checks from a throwaway pod that the main
// listener is not served on the pod IP or ClusterIP and the probe port serves only probes.
func TestLocalModeUnreachableInCluster(t *testing.T) {
	podIP := jsonpath(t, "pod", uiPod(t), "{.status.podIP}")
	svcIP := jsonpath(t, "svc", "ui-snellius", "{.spec.clusterIP}")
	urls := []string{
		"http://" + podIP + ":8080/api/v1/meta",
		"http://" + svcIP + ":80/api/v1/meta",
		"http://" + podIP + ":8081/healthz",
		"http://" + podIP + ":8081/readyz",
		"http://" + podIP + ":8081/metrics",
		"http://" + podIP + ":8081/api/v1/meta",
	}
	want := []string{"000", "000", "200", "200", "200", "404"}

	// A detached pod whose logs are read once it succeeds: attaching to a --rm pod loses output.
	kc := harnessKubeconfig(t)
	k := func(args ...string) ([]byte, error) {
		return exec.Command("kubectl", append([]string{"--kubeconfig", kc, "-n", uiNamespace}, args...)...).CombinedOutput()
	}
	del := func() { _, _ = k("delete", "pod", "probe-modes", "--ignore-not-found", "--wait=true") }
	del()
	t.Cleanup(del)
	args := append([]string{"run", "probe-modes", "--restart=Never", "--image=curlimages/curl:8.10.1",
		"--command", "--", "sh", "-c", probeScript, "sh"}, urls...)
	if out, err := k(args...); err != nil {
		t.Fatalf("run probe pod: %v: %s", err, out)
	}
	eventually(t, func() error {
		out, err := k("get", "pod", "probe-modes", "-o", "jsonpath={.status.phase}")
		if err != nil || string(out) != "Succeeded" {
			return fmt.Errorf("probe pod phase %q: %v", out, err)
		}
		return nil
	})
	out, err := k("logs", "probe-modes")
	if err != nil {
		t.Fatalf("probe logs: %v: %s", err, out)
	}
	got := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if u, code, ok := strings.Cut(strings.TrimSpace(line), "="); ok && strings.HasPrefix(u, "R:") {
			got[strings.TrimPrefix(u, "R:")] = code
		}
	}
	for i, u := range urls {
		if got[u] != want[i] {
			t.Errorf("GET %s = %q, want %q", u, got[u], want[i])
		}
	}
}
