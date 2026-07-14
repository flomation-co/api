package http

// Guards for the Infrastructure ▸ Kubernetes / ▸ Helm wiring.
//
// Two invariants are easy to break and silent when broken:
//
//   - The "opentofu" and "infrastructure" categoryMetadata entries both emit
//     Key "infrastructure" and so feed the same editor group header. If their
//     Name/Icon/Description drift apart, the header text changes depending on
//     which action the editor read first.
//   - Every dropdown registered in dynamicOptionsMetadata must point at a route
//     the service actually serves, or the editor fetches a 404 and silently falls
//     back to manual entry.

import (
	"net"
	"strings"
	"syscall"
	"testing"

	. "github.com/onsi/gomega"
)

func TestInfrastructureCategoryEntriesDoNotDrift(t *testing.T) {
	g := NewWithT(t)

	tofu, ok := categoryMetadata["opentofu"]
	g.Expect(ok).To(BeTrue(), "opentofu categoryMetadata entry is missing")
	infra, ok := categoryMetadata["infrastructure"]
	g.Expect(ok).To(BeTrue(), "infrastructure categoryMetadata entry is missing — 3-segment infrastructure/* IDs resolve no category without it")

	g.Expect(infra.Key).To(Equal(tofu.Key))
	g.Expect(infra.Name).To(Equal(tofu.Name))
	g.Expect(infra.Icon).To(Equal(tofu.Icon))
	g.Expect(infra.Description).To(Equal(tofu.Description))

	// The top-level entry must carry no Sub* fields: getCategoryForAction
	// overwrites them for 3-segment IDs, so any value here is a lie.
	g.Expect(infra.SubKey).To(BeEmpty())
	g.Expect(infra.SubName).To(BeEmpty())
}

func TestKubernetesActionsResolveTheirSubCategory(t *testing.T) {
	g := NewWithT(t)

	for _, tc := range []struct{ actionID, wantSubKey, wantSubName string }{
		{"infrastructure/kubernetes/pod_list", "infrastructure/kubernetes", "Kubernetes"},
		{"infrastructure/helm/release_install", "infrastructure/helm", "Helm"},
	} {
		cat := getCategoryForAction(tc.actionID)
		g.Expect(cat).ToNot(BeNil(), tc.actionID)
		g.Expect(cat.Key).To(Equal("infrastructure"), tc.actionID)
		g.Expect(cat.Name).To(Equal("Infrastructure"), tc.actionID)
		g.Expect(cat.SubKey).To(Equal(tc.wantSubKey), tc.actionID)
		g.Expect(cat.SubName).To(Equal(tc.wantSubName), tc.actionID)
	}

	// OpenTofu's 2-segment IDs must keep the inline sub-group they always had.
	tofu := getCategoryForAction("opentofu/apply")
	g.Expect(tofu).ToNot(BeNil())
	g.Expect(tofu.Key).To(Equal("infrastructure"))
	g.Expect(tofu.SubKey).To(Equal("opentofu"))
	g.Expect(tofu.SubName).To(Equal("OpenTofu"))
}

func TestKubernetesDynamicOptionsAreRegistered(t *testing.T) {
	g := NewWithT(t)

	// A representative sample across the shapes: namespace picker, name picker
	// for a namespaced kind, name picker for a cluster-scoped kind, the container
	// picker, and the Helm release picker.
	for marker, wantEndpoint := range map[string]string{
		"infrastructure/kubernetes/deployment_restart#namespace": "/api/v1/action/options/kubernetes-namespaces",
		"infrastructure/kubernetes/deployment_restart#name":      "/api/v1/action/options/kubernetes-deployments",
		"infrastructure/kubernetes/node_cordon#name":             "/api/v1/action/options/kubernetes-nodes",
		"infrastructure/kubernetes/namespace_delete#name":        "/api/v1/action/options/kubernetes-namespaces",
		"infrastructure/kubernetes/pod_logs#container":           "/api/v1/action/options/kubernetes-containers",
		"infrastructure/kubernetes/secret_get#name":              "/api/v1/action/options/kubernetes-secrets",
		"infrastructure/helm/release_uninstall#name":             "/api/v1/action/options/helm-releases",
		"infrastructure/helm/release_list#namespace":             "/api/v1/action/options/kubernetes-namespaces",
	} {
		got, ok := dynamicOptionsMetadata[marker]
		g.Expect(ok).To(BeTrue(), "missing dynamic-options marker: %s", marker)
		g.Expect(got.Endpoint).To(Equal(wantEndpoint), marker)
		g.Expect(got.Params).To(ContainElement("api_server_url"), marker)
		g.Expect(got.Params).To(ContainElement("service_account_token"), marker)
		// environment is appended by the editor, never declared.
		g.Expect(got.Params).ToNot(ContainElement("environment"), marker)
	}

	// Cluster-scoped kinds must not forward a namespace they do not have.
	g.Expect(dynamicOptionsMetadata["infrastructure/kubernetes/node_cordon#name"].Params).ToNot(ContainElement("namespace"))
	g.Expect(dynamicOptionsMetadata["infrastructure/kubernetes/namespace_delete#name"].Params).ToNot(ContainElement("namespace"))

	// Namespaced kinds must.
	g.Expect(dynamicOptionsMetadata["infrastructure/kubernetes/deployment_restart#name"].Params).To(ContainElement("namespace"))

	// The container picker needs the pod name too.
	g.Expect(dynamicOptionsMetadata["infrastructure/kubernetes/pod_logs#container"].Params).To(ContainElement("name"))
}

// Every kubernetes-* endpoint a marker points at must exist in k8sOptionResources
// (the generic handler panics on an unknown slug at route-registration time, so a
// typo here would take the api down on boot rather than degrade a dropdown).
func TestKubernetesOptionEndpointsHaveResources(t *testing.T) {
	g := NewWithT(t)

	for marker, opts := range dynamicOptionsMetadata {
		if !strings.HasPrefix(marker, "infrastructure/") {
			continue
		}
		slug := strings.TrimPrefix(opts.Endpoint, "/api/v1/action/options/")
		switch {
		case slug == "kubernetes-containers", slug == "helm-releases":
			continue // bespoke handlers, not table-driven
		case strings.HasPrefix(slug, "awx-"):
			// Infrastructure ▸ AAP / AWX is a third sub-group under the same
			// top-level category. It has its own resource table and its own guard
			// (TestAWXOptionEndpointsHaveRoutes, awx_options_test.go).
			continue
		}
		g.Expect(slug).To(HavePrefix("kubernetes-"), marker)
		_, ok := k8sOptionResources[strings.TrimPrefix(slug, "kubernetes-")]
		g.Expect(ok).To(BeTrue(), "marker %s points at %s, which has no k8sOptionResource", marker, slug)
	}
}

func TestKubernetesServerURLNormalisation(t *testing.T) {
	g := NewWithT(t)

	for in, want := range map[string]string{
		"https://192.168.80.189:6443":            "https://192.168.80.189:6443",
		"192.168.80.189:6443":                    "https://192.168.80.189:6443",
		"https://cluster.example.com:6443/":      "https://cluster.example.com:6443",
		"https://user:pw@cluster.example:6443/x": "https://cluster.example:6443",
	} {
		got, err := kubernetesServerURL(in)
		g.Expect(err).ToNot(HaveOccurred(), in)
		g.Expect(got).To(Equal(want), in)
	}

	for _, bad := range []string{"", "ftp://host", "${var.cluster}"} {
		_, err := kubernetesServerURL(bad)
		g.Expect(err).To(HaveOccurred(), bad)
	}
}

// The SSRF guard must block metadata endpoints while leaving the private ranges
// a self-hosted cluster actually lives on reachable.
func TestKubernetesOptionsDialControl(t *testing.T) {
	g := NewWithT(t)

	blocked := []string{"169.254.169.254:80", "[fd00:ec2::254]:80", "100.100.100.200:80"}
	for _, addr := range blocked {
		g.Expect(k8sOptionsDialControl("tcp", addr, nil)).To(HaveOccurred(), addr)
	}

	allowed := []string{"192.168.80.189:6443", "10.0.0.5:6443", "127.0.0.1:6443", "172.16.4.4:6443"}
	for _, addr := range allowed {
		g.Expect(k8sOptionsDialControl("tcp", addr, nil)).ToNot(HaveOccurred(), addr)
	}

	// A hostname that has not been resolved to an IP is passed through; the
	// Control hook runs again on the resolved address.
	g.Expect(k8sOptionsDialControl("tcp", net.JoinHostPort("cluster.internal", "6443"), nil)).ToNot(HaveOccurred())
}

var _ syscall.RawConn // keep the syscall import honest for the Control signature
