package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"regexp"
	"time"
)

const maxBody = 1 << 20

var habitName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func validDate(s string) bool {
	_, err := time.Parse(time.DateOnly, s)
	return err == nil
}

func NewServer(st *Store, token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })

	mux.HandleFunc("GET /entries", func(w http.ResponseWriter, r *http.Request) {
		from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
		if from == "" {
			from = "0000-01-01"
		}
		if to == "" {
			to = "9999-12-31"
		}
		if !validDate(from) || !validDate(to) {
			httpErr(w, http.StatusBadRequest, "from/to must be YYYY-MM-DD")
			return
		}
		es, err := st.ListEntries(from, to)
		respond(w, es, err)
	})
	mux.HandleFunc("GET /entries/{date}", withDate(func(w http.ResponseWriter, r *http.Request, date string) {
		e, err := st.GetEntry(date)
		respond(w, e, err)
	}))
	mux.HandleFunc("PUT /entries/{date}", withDate(func(w http.ResponseWriter, r *http.Request, date string) {
		var in struct {
			Body *string `json:"body"`
		}
		if !decode(w, r, &in) {
			return
		}
		if in.Body == nil {
			httpErr(w, http.StatusBadRequest, "body is required")
			return
		}
		e, err := st.PutEntry(date, *in.Body)
		respond(w, e, err)
	}))

	mux.HandleFunc("GET /habits/{date}", withDate(func(w http.ResponseWriter, r *http.Request, date string) {
		hs, err := st.ListHabits(date)
		respond(w, hs, err)
	}))
	mux.HandleFunc("PUT /habits/{date}/{habit}", withHabit(func(w http.ResponseWriter, r *http.Request, date, habit string) {
		var in struct {
			Value *float64 `json:"value"`
		}
		// An empty request body means a plain "done" check, whatever Content-Length says (chunked is -1).
		if !decodeOptional(w, r, &in) {
			return
		}
		h, err := st.PutHabit(date, habit, in.Value)
		respond(w, h, err)
	}))
	mux.HandleFunc("DELETE /habits/{date}/{habit}", withHabit(func(w http.ResponseWriter, r *http.Request, date, habit string) {
		err := st.DeleteHabit(date, habit)
		if err == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		respond(w, nil, err)
	}))

	return auth(token, mux)
}

func auth(token string, next http.Handler) http.Handler {
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="hibi"`)
			httpErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func withDate(h func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		date := r.PathValue("date")
		if !validDate(date) {
			httpErr(w, http.StatusBadRequest, "date must be YYYY-MM-DD")
			return
		}
		h(w, r, date)
	}
}

func withHabit(h func(http.ResponseWriter, *http.Request, string, string)) http.HandlerFunc {
	return withDate(func(w http.ResponseWriter, r *http.Request, date string) {
		habit := r.PathValue("habit")
		if !habitName.MatchString(habit) {
			httpErr(w, http.StatusBadRequest, "habit must match "+habitName.String())
			return
		}
		h(w, r, date, habit)
	})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeBody(w, r, v, false)
}

func decodeOptional(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeBody(w, r, v, true)
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any, allowEmpty bool) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if allowEmpty && errors.Is(err, io.EOF) {
		return true
	}
	if err == nil && dec.Decode(&struct{}{}) != io.EOF {
		err = errors.New("trailing data after JSON value")
	}
	if err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func respond(w http.ResponseWriter, v any, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		httpErr(w, http.StatusNotFound, "not found")
	case err != nil:
		log.Printf("store error: %v", err)
		httpErr(w, http.StatusInternalServerError, "internal error")
	default:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
