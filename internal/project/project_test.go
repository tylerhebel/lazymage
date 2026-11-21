package project

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func makeProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"bin", "src/app/etc", "src/app/code/Vendor"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	os.WriteFile(filepath.Join(root, "compose.yaml"), []byte("services:\n  phpfpm:\n    image: markoshust/magento-php:8.3-fpm-2\n  opensearch:\n    image: markoshust/magento-opensearch:2.12-0\n"), 0o644)
	os.WriteFile(filepath.Join(root, "compose.healthcheck.yaml"), []byte("services: {}\n"), 0o644)
	os.WriteFile(filepath.Join(root, "bin", "magento"), []byte("#!/bin/sh\n"), 0o755)
	return root
}

func TestFindRootFromNested(t *testing.T) {
	root := makeProject(t)
	r, _ := filepath.EvalSymlinks(root)
	a, ok := FindRoot(root)
	b, ok2 := FindRoot(filepath.Join(root, "src", "app", "code", "Vendor"))
	if !ok || !ok2 || a.Root != r || b.Root != r {
		t.Fatalf("root mismatch: %v %v %v", a, b, r)
	}
}

func TestFindRootOutside(t *testing.T) {
	if p, ok := FindRoot(t.TempDir()); ok {
		t.Fatalf("unexpected project %v", p)
	}
}

func TestFindRootNeedsExecutableMagento(t *testing.T) {
	root := makeProject(t)
	os.Chmod(filepath.Join(root, "bin", "magento"), 0o644)
	if _, ok := FindRoot(root); ok {
		t.Fatal("non-executable bin/magento should not match")
	}
}

func TestStaticFacts(t *testing.T) {
	root := makeProject(t)
	os.WriteFile(filepath.Join(root, "src/app/etc/env.php"), []byte(`<?php
return [
    'backend' => [
        'frontName' => 'admin_k3x'
    ],
    'MAGE_MODE' => 'developer',
];`), 0o644)
	os.WriteFile(filepath.Join(root, "src/composer.lock"), []byte(`{"packages":[{"name":"magento/magento2-base","version":"2.4.7-p3"}]}`), 0o644)
	os.WriteFile(filepath.Join(root, ".env"), []byte("export DOMAIN=\"https://shop.test/\"\n"), 0o644)
	p, _ := FindRoot(root)
	f := p.StaticFacts()
	want := Facts{Domain: "shop.test", DomainSource: ".env DOMAIN", MagentoVersion: "2.4.7-p3", VersionSource: "composer",
		DeployMode: "developer", ModeSource: "env.php", PHPVersion: "8.3", SearchEngine: "OpenSearch", AdminPath: "admin_k3x", PHPService: "phpfpm"}
	if !reflect.DeepEqual(f, want) {
		t.Fatalf("got  %+v\nwant %+v", f, want)
	}
	if args := p.ComposeArgs(); !reflect.DeepEqual(args, []string{"-f", "compose.yaml", "-f", "compose.healthcheck.yaml"}) {
		t.Fatalf("compose args %v", args)
	}
}

func TestParsers(t *testing.T) {
	if v := ParseCLIVersion("Magento CLI 2.4.7-p3\n"); v != "2.4.7-p3" {
		t.Errorf("cli version %q", v)
	}
	if m := ParseModeShow("Current application mode: developer. (Note: Environment variables may override this value.)"); m != "developer" {
		t.Errorf("mode %q", m)
	}
	if u := ParseBaseURL("https://magento.test/\n"); u != "https://magento.test/" {
		t.Errorf("base url %q", u)
	}
	svcs := ComposeServicesFromText("services:\n  app:\n    image: x\n  phpfpm:\n    image: y\n  #db:\nvolumes:\n  appdata:\n", "services:\n  phpmyadmin:\n    image: z\n")
	if !reflect.DeepEqual(svcs, []string{"app", "phpfpm", "phpmyadmin"}) {
		t.Errorf("services %v", svcs)
	}
	if PickPHPService([]string{"app", "php-fpm-xdebug", "php-fpm"}) != "php-fpm" {
		t.Error("php service pick")
	}
}
