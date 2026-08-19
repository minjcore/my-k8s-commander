package helmpolicy

import "strings"

import "testing"

func args(s string) []string { return strings.Fields(s) }

func TestWrites(t *testing.T) {
	reads := []string{
		"list", "list -n airbyte", "ls", "status airbyte", "history airbyte",
		"version", "get values airbyte", "show values airbyte/airbyte",
		"search repo airbyte",
		// repo add/update chỉ ghi cache cục bộ, không đụng cụm.
		"repo add airbyte https://airbytehq.github.io/helm-charts",
		"repo update", "repo list",
	}
	writes := []string{
		"install airbyte airbyte/airbyte -n airbyte --create-namespace",
		"upgrade airbyte airbyte/airbyte", "uninstall airbyte -n airbyte",
		"rollback airbyte 1", "repo remove airbyte", "repo rm airbyte",
	}
	// Ngoài allowlist: phải ok=false để k8s-worker chặn, và phía duyệt coi là ghi.
	rejected := []string{
		"", "template x y", "plugin install evil", "dependency build",
		"package .", "repo", "repo bogus", "lint .",
	}

	for _, c := range reads {
		w, ok := Writes(args(c))
		if !ok || w {
			t.Errorf("%q: muốn (đọc, hợp lệ), nhận (writes=%v, ok=%v)", c, w, ok)
		}
	}
	for _, c := range writes {
		w, ok := Writes(args(c))
		if !ok || !w {
			t.Errorf("%q: muốn (ghi, hợp lệ), nhận (writes=%v, ok=%v)", c, w, ok)
		}
	}
	for _, c := range rejected {
		if _, ok := Writes(args(c)); ok {
			t.Errorf("%q: phải bị từ chối", c)
		}
	}
}

func TestWritesKhongPhanBietHoaThuong(t *testing.T) {
	if w, ok := Writes(args("INSTALL x y")); !ok || !w {
		t.Errorf("INSTALL -> (%v,%v)", w, ok)
	}
	if w, ok := Writes(args("Repo Add x url")); !ok || w {
		t.Errorf("Repo Add -> (%v,%v)", w, ok)
	}
}

func TestBanned(t *testing.T) {
	cases := map[string]string{
		"install x y":                         "",
		"install x y --set a=b -n ns":         "",
		"install x y --post-renderer /bin/sh": "--post-renderer",
		"install x y --post-renderer=/bin/sh": "--post-renderer",
		"list --kubeconfig /tmp/evil":         "--kubeconfig",
		"install x y --wait":                  "--wait",
		"upgrade x y --atomic --timeout 10m":  "--atomic",
	}
	for cmd, want := range cases {
		if got := Banned(args(cmd)); got != want {
			t.Errorf("%q -> %q, muốn %q", cmd, got, want)
		}
	}
}
