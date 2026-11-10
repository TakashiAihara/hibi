package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const usage = `hibi: personal diary and habit tracker

Server:
  hibi serve                         env: HIBI_TOKEN (required), HIBI_DB (hibi.db), HIBI_ADDR (:8080)

Client (env: HIBI_URL, HIBI_TOKEN). DATE is YYYY-MM-DD or "today":
  hibi entry get DATE
  hibi entry put DATE [TEXT]         TEXT defaults to stdin
  hibi entry list [-from DATE] [-to DATE]
  hibi habit list DATE
  hibi habit done DATE HABIT [VALUE]
  hibi habit undo DATE HABIT
`

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "hibi:", err)
		os.Exit(1)
	}
}

var errUsage = errors.New("invalid arguments (run hibi -h)")

func run(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, usage)
		return nil
	}
	if args[0] == "serve" {
		return serve()
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	cmd := strings.Join(args[:min(2, len(args))], " ")
	rest := args[min(2, len(args)):]
	switch {
	case cmd == "entry get" && len(rest) == 1:
		return c.do("GET", "/entries/"+day(rest[0]), nil, stdout)
	case cmd == "entry put" && (len(rest) == 1 || len(rest) == 2):
		text := ""
		if len(rest) == 2 {
			text = rest[1]
		} else {
			b, err := io.ReadAll(stdin)
			if err != nil {
				return err
			}
			text = string(b)
		}
		return c.do("PUT", "/entries/"+day(rest[0]), map[string]string{"body": text}, stdout)
	case cmd == "entry list":
		fs := flag.NewFlagSet("entry list", flag.ContinueOnError)
		from, to := fs.String("from", "", "first date"), fs.String("to", "", "last date")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		q := url.Values{}
		if *from != "" {
			q.Set("from", day(*from))
		}
		if *to != "" {
			q.Set("to", day(*to))
		}
		return c.do("GET", "/entries?"+q.Encode(), nil, stdout)
	case cmd == "habit list" && len(rest) == 1:
		return c.do("GET", "/habits/"+day(rest[0]), nil, stdout)
	case cmd == "habit done" && (len(rest) == 2 || len(rest) == 3):
		var body any
		if len(rest) == 3 {
			v, err := strconv.ParseFloat(rest[2], 64)
			if err != nil {
				return fmt.Errorf("VALUE must be a number: %w", err)
			}
			body = map[string]float64{"value": v}
		}
		return c.do("PUT", "/habits/"+day(rest[0])+"/"+url.PathEscape(rest[1]), body, stdout)
	case cmd == "habit undo" && len(rest) == 2:
		return c.do("DELETE", "/habits/"+day(rest[0])+"/"+url.PathEscape(rest[1]), nil, stdout)
	}
	return errUsage
}

func day(s string) string {
	if s == "today" {
		return time.Now().Format(time.DateOnly)
	}
	return url.PathEscape(s)
}

func serve() error {
	token := os.Getenv("HIBI_TOKEN")
	if token == "" {
		return errors.New("HIBI_TOKEN is required")
	}
	st, err := OpenStore(envOr("HIBI_DB", "hibi.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	addr := envOr("HIBI_ADDR", ":8080")
	log.Printf("listening on %s", addr)
	srv := &http.Server{Addr: addr, Handler: NewServer(st, token),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute}
	return srv.ListenAndServe()
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

type client struct {
	base, token string
	http        *http.Client
}

func newClient() (*client, error) {
	base, token := os.Getenv("HIBI_URL"), os.Getenv("HIBI_TOKEN")
	if base == "" || token == "" {
		return nil, errors.New("HIBI_URL and HIBI_TOKEN are required")
	}
	return &client{base: strings.TrimRight(base, "/"), token: token, http: &http.Client{Timeout: 30 * time.Second}}, nil
}

func (c *client) do(method, path string, body any, out io.Writer) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %s %s", method, path, res.Status, strings.TrimSpace(string(b)))
	}
	_, err = out.Write(b)
	return err
}
