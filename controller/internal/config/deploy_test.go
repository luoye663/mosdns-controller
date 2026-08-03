package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMosdnsProfilesRenderDynamicUpstreamRegistry(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "deploy", "render-mosdns-config.sh")
	for _, profile := range []string{"compose", "local", "integration", "binary"} {
		t.Run(profile, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "mosdns.yaml")
			command := exec.Command("bash", script, profile, output)
			command.Dir = root
			if data, err := command.CombinedOutput(); err != nil {
				t.Fatalf("render: %v: %s", err, data)
			}
			data, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			value := string(data)
			for _, required := range []string{"tag: dynamic_upstreams", "type: dynamic_upstream_registry", "initial_snapshot:", "default_group_id: remote_dns", "legacy_groups:", "forward_snapshot_file:", "id: local_dns", "id: remote_dns", "exec: $dynamic_upstreams"} {
				if !strings.Contains(value, required) {
					t.Fatalf("rendered config does not contain %q", required)
				}
			}
			for _, forbidden := range []string{"tag: cache_local", "tag: cache_remote", "tag: ecs_local", "tag: ecs_remote", "type: dynamic_forward", "tag: route_local", "tag: route_remote", "type: dynamic_domain_set", "tag: subscription_allow", "tag: subscription_block", "tag: subscription_local", "tag: subscription_remote", "qname $subscription_"} {
				if strings.Contains(value, forbidden) {
					t.Fatalf("rendered config still contains %q", forbidden)
				}
			}
		})
	}
}
