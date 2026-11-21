package docker

import (
	"testing"

	"lazymage/internal/config"
)

const psNDJSON = `{"Command":"\"/docker-entrypoint…\"","CreatedAt":"2026-11-20 10:00:00 +0000 UTC","Health":"healthy","Name":"shop-app-1","Service":"app","State":"running","Status":"Up 3 minutes (healthy)","Image":"markoshust/magento-nginx:1.28-0"}
{"Health":"","Name":"shop-phpfpm-1","Service":"phpfpm","State":"running","Status":"Up 3 minutes","Image":"markoshust/magento-php:8.4-fpm-3"}
{"Health":"starting","Name":"shop-opensearch-1","Service":"opensearch","State":"running","Status":"Up 10 seconds (health: starting)"}
`

func TestParsePSJSONForms(t *testing.T) {
	cs, err := ParsePSJSON(psNDJSON)
	if err != nil || len(cs) != 3 {
		t.Fatalf("ndjson: %v %d", err, len(cs))
	}
	arr, err := ParsePSJSON(`[{"Name":"a-1","Service":"a","State":"running","Status":"Up 1 minute (unhealthy)"}]`)
	if err != nil || len(arr) != 1 || arr[0].Health != "unhealthy" {
		t.Fatalf("array: %v %+v", err, arr)
	}
	s := Summarize([]string{"app", "phpfpm", "opensearch", "db"}, cs)
	if s.Health != HealthSomeDown || s.Up != 2 || s.Total != 4 || s.Starting != 1 {
		t.Fatalf("summary %+v", s)
	}
	all := Summarize([]string{"app", "phpfpm"}, []Container{cs[0], cs[2]}) // sorted: app, opensearch, phpfpm
	if all.Health != HealthAllUp {
		t.Fatalf("want all up: %+v", all)
	}
}

func TestParsePSText(t *testing.T) {
	out := `NAME                IMAGE                              COMMAND                  SERVICE      CREATED         STATUS                   PORTS
shop-app-1          markoshust/magento-nginx:1.28-0    "/docker-entrypoint.…"   app          5 minutes ago   Up 5 minutes (healthy)   0.0.0.0:80->8000/tcp
shop-db-1           mariadb:11.4                       "docker-entrypoint.s…"   db           5 minutes ago   Exited (0) 1 minute ago
`
	cs := ParsePSText(out)
	if len(cs) != 2 || cs[0].Service != "app" || cs[0].State != "running" || cs[0].Health != "healthy" || cs[1].State != "exited" {
		t.Fatalf("%+v", cs)
	}
}

func TestClassify(t *testing.T) {
	cases := []struct{ os, name, ctx, host, want string }{
		{"OrbStack", "orbstack", "orbstack", "unix:///Users/me/.orbstack/run/docker.sock", config.RuntimeOrbStack},
		{"Docker Desktop", "docker-desktop", "desktop-linux", "unix:///Users/me/.docker/run/docker.sock", config.RuntimeDockerDesktop},
		{"Ubuntu 24.04.1 LTS", "box", "default", "unix:///var/run/docker.sock", config.RuntimeDockerEngine},
		{"", "", "", "", ""},
	}
	for _, c := range cases {
		if got := Classify(c.os, c.name, c.ctx, c.host); got != c.want {
			t.Errorf("%v: got %q", c, got)
		}
	}
}

func TestEngineDown(t *testing.T) {
	if !IsEngineDown("Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?") {
		t.Fatal("expected engine down")
	}
	if IsEngineDown("no such service: foo") {
		t.Fatal("false positive")
	}
}
