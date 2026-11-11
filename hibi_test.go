package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
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
	req, _ := http.NewRequest("GET", srv.URL+"/entries", nil)
	req.Header.Set("Authorization", "Basic "+testToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Errorf("right token with Basic scheme: got %d, want 401", res.StatusCode)
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
	if hs, err := st.ListHabits("2026-11-10"); err != nil || len(hs) != 1 || hs[0].Habit != "stretch" || hs[0].Value != nil {
		t.Errorf("stored after empty body: got %+v %v, want stretch with null value", hs, err)
	}
}

func TestDBFileIsOwnerOnly(t *testing.T) {
	// A zero umask makes the result depend only on the code, not on the shell running the test.
	defer syscall.Umask(syscall.Umask(0))
	dir := t.TempDir()
	loose := filepath.Join(dir, "old.db")
	if err := os.WriteFile(loose, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(dir, "new.db"), loose} {
		st, err := OpenStore(path)
		if err != nil {
			t.Fatal(err)
		}
		st.Close()
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode: got %o, want 600", filepath.Base(path), fi.Mode().Perm())
		}
	}
}

func TestConcurrentWritesDoNotFail(t *testing.T) {
	srv := setup(t)
	var wg sync.WaitGroup
	codes := make(chan int, 200)
	for i := range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// No t.Fatal here: FailNow must not be called from other goroutines.
			req, _ := http.NewRequest("PUT", fmt.Sprintf("%s/habits/2026-11-10/h%d", srv.URL, i%50), strings.NewReader(fmt.Sprintf(`{"value":%d}`, i)))
			req.Header.Set("Authorization", "Bearer "+testToken)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				codes <- -1
				return
			}
			res.Body.Close()
			codes <- res.StatusCode
		}()
	}
	wg.Wait()
	close(codes)
	failed := 0
	for c := range codes {
		if c != 200 {
			failed++
		}
	}
	if failed != 0 {
		t.Errorf("concurrent PUTs: %d of 200 failed", failed)
	}
}

func TestAPIDetails(t *testing.T) {
	srv := setup(t)
	if code, body := call(t, srv, "GET", "/habits/2026-11-10", testToken, ""); code != 200 || strings.TrimSpace(body) != "[]" {
		t.Errorf("empty habit list: got %d %q, want []", code, body)
	}
	if code, body := call(t, srv, "GET", "/entries", testToken, ""); code != 200 || strings.TrimSpace(body) != "[]" {
		t.Errorf("empty entry list: got %d %q, want []", code, body)
	}

	req, _ := http.NewRequest("PUT", srv.URL+"/entries/2026-11-10", strings.NewReader(`{"body":"lower-case scheme"}`))
	req.Header.Set("Authorization", "bearer  "+testToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/json" {
		t.Errorf("lower-case bearer: got %d %q, want 200 JSON", res.StatusCode, res.Header.Get("Content-Type"))
	}
	if _, body := call(t, srv, "GET", "/entries", testToken, ""); !strings.Contains(body, `"2026-11-10"`) {
		t.Errorf("list without bounds: got %s, want the 2026-11-10 entry", body)
	}
	// Literal 1 MiB (README promises it); `{"body":""}` adds 11 bytes around the text.
	const mib = 1 << 20
	if code, _ := call(t, srv, "PUT", "/entries/2026-11-11", testToken, `{"body":"`+strings.Repeat("a", mib-11)+`"}`); code != 200 {
		t.Errorf("body of exactly 1 MiB: got %d, want 200", code)
	}
	if code, _ := call(t, srv, "PUT", "/entries/2026-11-11", testToken, `{"body":"`+strings.Repeat("a", mib-10)+`"}`); code != 413 {
		t.Errorf("body of 1 MiB + 1: got %d, want 413", code)
	}
	if code, _ := call(t, srv, "PUT", "/entries/2026-11-11", testToken, `{"body":"x"}`+strings.Repeat(" ", mib)); code != 413 {
		t.Errorf("valid JSON padded past 1 MiB: got %d, want 413", code)
	}
	if code, _ := call(t, srv, "DELETE", "/entries/2026-11-10", testToken, ""); code != 204 {
		t.Errorf("delete entry: got %d, want 204", code)
	}
	if code, _ := call(t, srv, "DELETE", "/entries/2026-11-10", testToken, ""); code != 404 {
		t.Errorf("delete missing entry: got %d, want 404", code)
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
	if _, err := cli(t, "", "entry", "put", "2026-11-07", "to delete"); err != nil {
		t.Fatal(err)
	}
	if out, err := cli(t, "", "entry", "delete", "2026-11-07"); err != nil || out != "" {
		t.Fatalf("entry delete: got %q %v", out, err)
	}
	if _, err := cli(t, "", "entry", "get", "2026-11-07"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("after entry delete: got %v, want 404", err)
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

func TestDiaryDayStartsAt5(t *testing.T) {
	jst := time.FixedZone("JST", 9*3600)
	cases := []struct{ at, want string }{
		{"2026-11-11T00:00", "2026-11-10"},
		{"2026-11-11T04:59", "2026-11-10"},
		{"2026-11-11T05:00", "2026-11-11"},
		{"2026-11-11T23:59", "2026-11-11"},
	}
	for _, c := range cases {
		tm, err := time.ParseInLocation("2006-01-02T15:04", c.at, jst)
		if err != nil {
			t.Fatal(err)
		}
		if got := diaryDay(tm); got != c.want {
			t.Errorf("diaryDay(%s): got %s, want %s", c.at, got, c.want)
		}
	}
}

func TestTodayResolvesInCLIZone(t *testing.T) {
	setup(t)
	origZone, origNow := cliZone, cliNow
	defer func() { cliZone, cliNow = origZone, origNow }()
	// 2026-11-10 20:00 UTC is 11-11 10:00 at +14h and 11-10 08:00 at -12h: both past 05:00, different dates.
	cliNow = func() time.Time { return time.Date(2026, 11, 10, 20, 0, 0, 0, time.UTC) }
	for _, c := range []struct {
		off  int
		want string
	}{{14, "2026-11-11"}, {-12, "2026-11-10"}} {
		cliZone = time.FixedZone("test", c.off*3600)
		if _, err := cli(t, "", "entry", "put", "today", fmt.Sprintf("zone %d", c.off)); err != nil {
			t.Fatal(err)
		}
		out, err := cli(t, "", "entry", "get", c.want)
		if err != nil || !strings.Contains(out, fmt.Sprintf(`"body":"zone %d"`, c.off)) {
			t.Errorf("today at UTC%+d: want the entry on %s, got %s %v", c.off, c.want, out, err)
		}
	}
	// An explicit date is used as given, even before 05:00.
	cliNow = func() time.Time { return time.Date(2026, 11, 10, 18, 0, 0, 0, time.UTC) }
	cliZone = time.FixedZone("JST", 9*3600)
	if got := resolveDay("2026-11-11"); got != "2026-11-11" {
		t.Errorf("explicit date at 03:00: got %s, want 2026-11-11", got)
	}
	if got := resolveDay("today"); got != "2026-11-10" {
		t.Errorf("today at 03:00 JST: got %s, want 2026-11-10", got)
	}
}

func TestHabitsViaCLI(t *testing.T) {
	setup(t)
	if _, err := cli(t, "", "habit", "done", "2026-11-10", "walk", "10"); err != nil {
		t.Fatal(err)
	}
	if _, err := cli(t, "", "habit", "done", "2026-11-10", "stretch"); err != nil {
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
	if len(hs) != 2 || hs[0].Habit != "stretch" || hs[0].Value != nil || hs[1].Habit != "walk" || hs[1].Value == nil || *hs[1].Value != 8123 {
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
	for _, v := range []string{"lots", "NaN", "Inf"} {
		if _, err := cli(t, "", "habit", "done", "2026-11-10", "walk", v); err == nil || !strings.Contains(err.Error(), "VALUE must be a finite number") {
			t.Errorf("value %q: got %v, want a client-side number error", v, err)
		}
	}
	if _, err := cli(t, "", "entry", "list", "2026-11-01"); err != errUsage {
		t.Errorf("entry list with a positional arg: got %v, want usage error", err)
	}
	t.Setenv("HIBI_TOKEN", "")
	if _, err := cli(t, "", "entry", "list"); err == nil || !strings.Contains(err.Error(), "HIBI_URL and HIBI_TOKEN are required") {
		t.Errorf("missing HIBI_TOKEN: got %v, want a client-side config error", err)
	}
}

func TestServeStartsAndShutsDownOnSignal(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	t.Setenv("HIBI_TOKEN", testToken)
	t.Setenv("HIBI_DB", filepath.Join(t.TempDir(), "hibi.db"))
	t.Setenv("HIBI_ADDR", addr)

	done := make(chan error, 1)
	go func() { done <- run([]string{"serve"}, strings.NewReader(""), io.Discard) }()
	up := false
	for range 50 {
		if res, err := http.Get("http://" + addr + "/healthz"); err == nil {
			res.Body.Close()
			up = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !up {
		t.Fatal("serve did not answer /healthz within 1s")
	}
	select {
	case err := <-done:
		// Sending SIGINT now would hit the test binary itself.
		t.Fatalf("serve returned before the signal: %v", err)
	default:
	}
	start := time.Now()
	syscall.Kill(os.Getpid(), syscall.SIGINT)
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serve after SIGINT: got %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop within 5s of SIGINT")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("shutdown took %v, want under 2s", d)
	}

	t.Setenv("HIBI_TOKEN", "")
	if err := run([]string{"serve"}, strings.NewReader(""), io.Discard); err == nil || !strings.Contains(err.Error(), "HIBI_TOKEN is required") {
		t.Errorf("serve without token: got %v", err)
	}
}

func TestShutdownLetsInFlightRequestFinish(t *testing.T) {
	st, err := OpenStore(filepath.Join(t.TempDir(), "hibi.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveUntil(ctx, l, NewServer(st, testToken)) }()

	// The body arrives in two halves, so the request is in flight when shutdown starts.
	pr, pw := io.Pipe()
	req, _ := http.NewRequest("PUT", "http://"+l.Addr().String()+"/entries/2026-11-10", pr)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resc := make(chan *http.Response, 1)
	go func() {
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			resc <- nil
			return
		}
		res.Body.Close()
		resc <- res
	}()
	pw.Write([]byte(`{"body":`))
	time.Sleep(100 * time.Millisecond)
	cancel()
	time.Sleep(100 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("serveUntil returned while a request was in flight: %v", err)
	default:
	}
	pw.Write([]byte(`"finished"}`))
	pw.Close()

	select {
	case res := <-resc:
		if res == nil || res.StatusCode != 200 {
			t.Fatalf("in-flight request during shutdown: got %v, want 200", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight request did not complete")
	}
	if err := <-done; err != nil {
		t.Errorf("serveUntil after cancel: got %v, want nil", err)
	}
	if e, err := st.GetEntry("2026-11-10"); err != nil || e.Body != "finished" {
		t.Errorf("entry after shutdown: got %+v %v", e, err)
	}
}

func TestStoreRejectsDSNLikePaths(t *testing.T) {
	dir := t.TempDir()
	// A broken check would create ":memory:" relative to cwd; keep it inside the temp dir.
	t.Chdir(dir)
	for _, p := range []string{filepath.Join(dir, "a?b.db"), "file:" + filepath.Join(dir, "x.db"), ":memory:"} {
		if _, err := OpenStore(p); err == nil || !strings.Contains(err.Error(), "plain file path") {
			t.Errorf("OpenStore(%q): got %v, want rejection", p, err)
		}
	}
}

func TestEmptyTokenNeverAuthenticates(t *testing.T) {
	st, err := OpenStore(filepath.Join(t.TempDir(), "hibi.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := httptest.NewServer(NewServer(st, ""))
	defer srv.Close()
	for _, h := range []string{"Bearer ", "Bearer"} {
		req, _ := http.NewRequest("GET", srv.URL+"/entries", nil)
		req.Header.Set("Authorization", h)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 401 {
			t.Errorf("empty server token with %q: got %d, want 401", h, res.StatusCode)
		}
	}
}

func TestWaitsForOutsideWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hibi.db")
	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// Another process (an sqlite3 .backup, say) holds the write lock for 300ms.
	other, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	conn, err := other.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		conn.ExecContext(context.Background(), "COMMIT")
		conn.Close()
	}()
	if _, err := st.PutEntry("2026-11-10", "after the lock"); err != nil {
		t.Errorf("write while another connection holds the lock: got %v, want it to wait", err)
	}
}
