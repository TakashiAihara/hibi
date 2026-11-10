package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

const testToken = "s3cret"

func setup(t *testing.T) *httptest.Server {
	t.Helper()
	st, err := OpenStore(filepath.Join(t.TempDir(), "hibi.db"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewServer(st, testToken))
	t.Cleanup(func() { srv.Close(); st.Close() })
	t.Setenv("HIBI_URL", srv.URL)
	t.Setenv("HIBI_TOKEN", testToken)
	return srv
}

func call(t *testing.T, srv *httptest.Server, method, path, token, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var b bytes.Buffer
	b.ReadFrom(res.Body)
	return res.StatusCode, b.String()
}

func cli(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := run(args, strings.NewReader(stdin), &out)
	return out.String(), err
}

func TestAuth(t *testing.T) {
	srv := setup(t)
	if code, _ := call(t, srv, "GET", "/entries", "", ""); code != 401 {
		t.Errorf("no token: got %d, want 401", code)
	}
	if code, _ := call(t, srv, "GET", "/entries", "wrong", ""); code != 401 {
		t.Errorf("wrong token: got %d, want 401", code)
	}
	if code, _ := call(t, srv, "GET", "/healthz", "", ""); code != 200 {
		t.Errorf("healthz without token: got %d, want 200", code)
	}
	if code, _ := call(t, srv, "GET", "/entries", testToken, ""); code != 200 {
		t.Errorf("valid token: got %d, want 200", code)
	}
}

func TestValidation(t *testing.T) {
	srv := setup(t)
	cases := []struct{ method, path, body string }{
		{"GET", "/entries/2026-13-01", ""},
		{"PUT", "/entries/2026-11-11", `{}`},
		{"PUT", "/entries/2026-11-11", `{"body":"x","extra":1}`},
		{"PUT", "/habits/2026-11-11/Bad%20Name", ""},
		{"PUT", "/habits/2026-11-11/walk", `{"value":"ten"}`},
		{"GET", "/entries?from=yesterday", ""},
	}
	for _, c := range cases {
		if code, body := call(t, srv, c.method, c.path, testToken, c.body); code != 400 {
			t.Errorf("%s %s %s: got %d %s, want 400", c.method, c.path, c.body, code, body)
		}
	}
}

func TestEntryRoundTripViaCLI(t *testing.T) {
	setup(t)
	if _, err := cli(t, "", "entry", "get", "2026-11-10"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("missing entry: got %v, want 404", err)
	}
	if _, err := cli(t, "first draft", "entry", "put", "2026-11-10"); err != nil {
		t.Fatal(err)
	}
	if _, err := cli(t, "", "entry", "put", "2026-11-10", "rewritten"); err != nil {
		t.Fatal(err)
	}
	if _, err := cli(t, "", "entry", "put", "2026-11-08", "older"); err != nil {
		t.Fatal(err)
	}
	out, err := cli(t, "", "entry", "get", "2026-11-10")
	if err != nil {
		t.Fatal(err)
	}
	var e Entry
	json.Unmarshal([]byte(out), &e)
	if e.Body != "rewritten" {
		t.Errorf("body: got %q, want rewritten", e.Body)
	}

	out, err = cli(t, "", "entry", "list", "-from", "2026-11-09")
	if err != nil {
		t.Fatal(err)
	}
	var es []Entry
	json.Unmarshal([]byte(out), &es)
	if len(es) != 1 || es[0].Date != "2026-11-10" {
		t.Errorf("list from 2026-11-09: got %+v, want only 2026-11-10", es)
	}
}

func TestHabitsViaCLI(t *testing.T) {
	setup(t)
	if _, err := cli(t, "", "habit", "done", "2026-11-10", "stretch"); err != nil {
		t.Fatal(err)
	}
	if _, err := cli(t, "", "habit", "done", "2026-11-10", "walk", "10"); err != nil {
		t.Fatal(err)
	}
	if _, err := cli(t, "", "habit", "done", "2026-11-10", "walk", "8123"); err != nil {
		t.Fatal(err)
	}
	if _, err := cli(t, "", "habit", "done", "2026-11-11", "walk", "50"); err != nil {
		t.Fatal(err)
	}
	out, err := cli(t, "", "habit", "list", "2026-11-10")
	if err != nil {
		t.Fatal(err)
	}
	var hs []HabitLog
	json.Unmarshal([]byte(out), &hs)
	if len(hs) != 2 || hs[0].Habit != "stretch" || hs[0].Value != nil || hs[1].Value == nil || *hs[1].Value != 8123 {
		t.Fatalf("habit list: got %s", out)
	}

	if _, err := cli(t, "", "habit", "undo", "2026-11-10", "stretch"); err != nil {
		t.Fatal(err)
	}
	if _, err := cli(t, "", "habit", "undo", "2026-11-10", "stretch"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("second undo: got %v, want 404", err)
	}
	out, _ = cli(t, "", "habit", "list", "2026-11-10")
	if !strings.Contains(out, `"walk"`) || strings.Contains(out, `"stretch"`) {
		t.Errorf("after undo: got %s", out)
	}
}

func TestCLIUsage(t *testing.T) {
	setup(t)
	if _, err := cli(t, "", "entry", "frobnicate"); err != errUsage {
		t.Errorf("unknown command: got %v, want usage error", err)
	}
	if _, err := cli(t, "", "habit", "done", "2026-11-10", "walk", "lots"); err == nil {
		t.Error("non-numeric value: want error")
	}
	t.Setenv("HIBI_TOKEN", "")
	if _, err := cli(t, "", "entry", "list"); err == nil {
		t.Error("missing HIBI_TOKEN: want error")
	}
}
