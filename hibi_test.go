package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"time"
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
	for _, m := range []string{"PUT", "DELETE"} {
		req, _ := http.NewRequest(m, srv.URL+"/habits/2026-11-10/walk", nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 401 || res.Header.Get("WWW-Authenticate") != `Bearer realm="hibi"` {
			t.Errorf("%s without token: got %d %q, want 401 with WWW-Authenticate", m, res.StatusCode, res.Header.Get("WWW-Authenticate"))
		}
	}
}

// A proxy that re-chunks an empty body hands the server Content-Length -1.
func TestEmptyBodyOfUnknownLengthIsPlainCheck(t *testing.T) {
	st, err := OpenStore(filepath.Join(t.TempDir(), "hibi.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	req := httptest.NewRequest("PUT", "/habits/2026-11-10/stretch", io.NopCloser(strings.NewReader("")))
	req.ContentLength = -1
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	NewServer(st, testToken).ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("empty body with unknown length: got %d %s, want 200", rec.Code, rec.Body)
	}
}

func TestDBFileIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hibi.db")
	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("db mode: got %o, want 600", fi.Mode().Perm())
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
		{"GET", "/entries?to=tomorrow", ""},
		{"PUT", "/entries/2026-11-11", `{"body":"a"}{"body":"b"}`},
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
	if _, err := cli(t, "first draft\n", "entry", "put", "2026-11-10"); err != nil {
		t.Fatal(err)
	}
	if out, _ := cli(t, "", "entry", "get", "2026-11-10"); !strings.Contains(out, `"body":"first draft\n"`) {
		t.Fatalf("stdin put: got %s, want body read from stdin verbatim", out)
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

	for _, d := range []string{"2026-11-09", "2026-11-11", "2026-11-12"} {
		if _, err := cli(t, "", "entry", "put", d, "day "+d); err != nil {
			t.Fatal(err)
		}
	}
	out, err = cli(t, "", "entry", "list", "-from", "2026-11-09", "-to", "2026-11-11")
	if err != nil {
		t.Fatal(err)
	}
	var es []Entry
	if err := json.Unmarshal([]byte(out), &es); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range es {
		got = append(got, e.Date)
	}
	if strings.Join(got, ",") != "2026-11-11,2026-11-10,2026-11-09" {
		t.Errorf("list 2026-11-09..2026-11-11: got %v, want both bounds included, newest first", got)
	}
}

func TestTodayUsesLocalDate(t *testing.T) {
	setup(t)
	if _, err := cli(t, "", "entry", "put", "today", "now"); err != nil {
		t.Fatal(err)
	}
	out, err := cli(t, "", "entry", "get", time.Now().Format(time.DateOnly))
	if err != nil || !strings.Contains(out, `"body":"now"`) {
		t.Fatalf("today: got %s %v", out, err)
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
	if err := json.Unmarshal([]byte(out), &hs); err != nil {
		t.Fatal(err)
	}
	if len(hs) != 2 || hs[0].Habit != "stretch" || hs[0].Value != nil || hs[1].Value == nil || *hs[1].Value != 8123 {
		t.Fatalf("habit list: got %s", out)
	}

	if out, err := cli(t, "", "habit", "undo", "2026-11-10", "stretch"); err != nil || out != "" {
		t.Fatalf("undo: got %q %v, want no output", out, err)
	}
	if _, err := cli(t, "", "habit", "undo", "2026-11-10", "stretch"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("second undo: got %v, want 404", err)
	}
	out, err = cli(t, "", "habit", "list", "2026-11-10")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"walk"`) || strings.Contains(out, `"stretch"`) {
		t.Errorf("after undo: got %s", out)
	}
}

func TestCLIUsage(t *testing.T) {
	setup(t)
	if _, err := cli(t, "", "entry", "frobnicate"); err != errUsage {
		t.Errorf("unknown command: got %v, want usage error", err)
	}
	if _, err := cli(t, "", "habit", "done", "2026-11-10", "walk", "lots"); err == nil || !strings.Contains(err.Error(), "VALUE must be a number") {
		t.Errorf("non-numeric value: got %v, want a client-side number error", err)
	}
	t.Setenv("HIBI_TOKEN", "")
	if _, err := cli(t, "", "entry", "list"); err == nil || !strings.Contains(err.Error(), "HIBI_URL and HIBI_TOKEN are required") {
		t.Errorf("missing HIBI_TOKEN: got %v, want a client-side config error", err)
	}
}
