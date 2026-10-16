package status

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// Source supplies a coherent read-only journal snapshot.
type Source interface {
	Journals(context.Context, []store.QueueKey) ([]store.Journal, error)
}

// ListenAddress makes a port-only address loopback-only. Without a token every
// resolved address must be loopback; the returned address pins that resolution.
// An empty address disables the API.
func ListenAddress(address, token string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return listenAddress(ctx, address, token, net.DefaultResolver.LookupIPAddr)
}

func listenAddress(ctx context.Context, address, token string, lookup func(context.Context, string) ([]net.IPAddr, error)) (string, error) {
	if address == "" {
		return "", nil
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", fmt.Errorf("API listen requires host:port or :port: %w", err)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return "", errors.New("API listen port must be between 0 and 65535")
	}
	if host == "" {
		host = "127.0.0.1"
	}
	if token == "" {
		if ip, err := netip.ParseAddr(host); err == nil {
			if !ip.IsLoopback() {
				return "", errors.New("non-loopback API listen requires a bearer token")
			}
		} else {
			ips, err := lookup(ctx, host)
			if err != nil || len(ips) == 0 {
				return "", errors.New("API listen hostname must resolve only to loopback without a bearer token")
			}
			for _, ip := range ips {
				if !ip.IP.IsLoopback() {
					return "", errors.New("non-loopback API listen requires a bearer token")
				}
			}
			host = ips[0].String()
		}
	}
	return net.JoinHostPort(host, port), nil
}

// ReadToken accepts an environment value or a file, never both. File newlines
// are trimmed for secret mounts; an explicitly configured empty file is invalid.
func ReadToken(value, file string) (string, error) {
	if file != "" {
		if value != "" {
			return "", errors.New("set only one of OPENMERGE_API_TOKEN and OPENMERGE_API_TOKEN_FILE/--api-token-file")
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("read API token file: %w", err)
		}
		value = strings.TrimSpace(string(data))
		if value == "" {
			return "", errors.New("API token file is empty")
		}
	}
	for _, c := range value {
		if c < 33 || c > 126 {
			return "", errors.New("API bearer token must contain visible ASCII without spaces")
		}
	}
	return value, nil
}

// Handler exposes GET only and confines reads to this server's configured keys.
func Handler(source Source, keys []store.QueueKey, token string, sanitizer *diagnostic.Sanitizer) http.Handler {
	if sanitizer == nil {
		sanitizer = diagnostic.NewSanitizer()
	}
	sanitizer.AddSecrets(token)
	keys = slices.Clone(keys)
	slices.SortFunc(keys, func(a, b store.QueueKey) int {
		return strings.Compare(a.Repository+"\x00"+a.BaseBranch+"\x00"+a.Installation+"\x00"+a.Lane, b.Repository+"\x00"+b.BaseBranch+"\x00"+b.Installation+"\x00"+b.Lane)
	})
	want := sha256.Sum256([]byte("Bearer " + token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		if token != "" {
			got := sha256.Sum256([]byte(r.Header.Get("Authorization")))
			if subtle.ConstantTimeCompare(want[:], got[:]) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="openmerge"`)
				fail(w, http.StatusUnauthorized, "unauthorized", "valid bearer token required")
				return
			}
		}
		const maxRequestBody = 4 << 10
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				fail(w, http.StatusRequestEntityTooLarge, "request-too-large", "request body exceeds 4096 bytes")
			} else {
				fail(w, http.StatusBadRequest, "invalid-request", "request body could not be read")
			}
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			fail(w, http.StatusMethodNotAllowed, "method-not-allowed", "only GET is supported")
			return
		}
		selected, pr, limit, err := route(r, keys)
		if err != nil {
			fail(w, http.StatusBadRequest, "invalid-request", err.Error())
			return
		}
		if selected == nil {
			fail(w, http.StatusNotFound, "not-found", "queue or endpoint not found")
			return
		}
		list := r.URL.Path == "/v1/queues"
		if !list && len(selected) > 1 {
			fail(w, http.StatusConflict, "ambiguous-queue", "multiple installations or lanes match this queue")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		journals, err := source.Journals(ctx, selected)
		if err != nil {
			fail(w, http.StatusServiceUnavailable, "unavailable", "queue history could not be read")
			return
		}
		if list {
			queues := make([]Queue, 0, len(journals))
			for _, journal := range journals {
				q, _, _ := Project(journal, 0, 0, sanitizer)
				queues = append(queues, q)
			}
			writeJSON(w, struct {
				Version int     `json:"version"`
				Queues  []Queue `json:"queues"`
			}{Version, queues})
			return
		}
		if len(journals) == 0 {
			fail(w, http.StatusNotFound, "not-found", "queue not found")
			return
		}
		q, entry, why := Project(journals[0], pr, limit, sanitizer)
		if pr > 0 && entry == nil {
			fail(w, http.StatusNotFound, "not-found", "PR has no recorded queue history")
			return
		}
		writeJSON(w, Response{Version: Version, Queue: &q, PR: entry, Why: why})
	})
}

func route(r *http.Request, keys []store.QueueKey) ([]store.QueueKey, int, int, error) {
	parts := strings.Split(strings.TrimPrefix(r.URL.EscapedPath(), "/"), "/")
	if len(parts) != 2 && len(parts) != 5 && len(parts) != 7 || parts[0] != "v1" || parts[1] != "queues" {
		return nil, 0, 0, nil
	}
	for i := 2; i < len(parts); i++ {
		part, err := url.PathUnescape(parts[i])
		if err != nil || part == "" || part == "." || part == ".." {
			return nil, 0, 0, errors.New("invalid path component")
		}
		parts[i] = part
	}
	pr, limit := 0, 20
	if len(parts) == 7 {
		var err error
		pr, err = strconv.Atoi(parts[6])
		if parts[5] != "prs" || err != nil || pr <= 0 {
			return nil, 0, 0, errors.New("PR must be a positive integer")
		}
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, 0, 0, errors.New("invalid query")
	}
	for name, values := range values {
		if pr == 0 || name != "events" || len(values) != 1 {
			return nil, 0, 0, errors.New("only PR detail accepts ?events=N")
		}
		limit, err = strconv.Atoi(values[0])
		if err != nil || limit < 1 || limit > 100 {
			return nil, 0, 0, errors.New("events must be between 1 and 100")
		}
	}
	selected := make([]store.QueueKey, 0)
	for _, key := range keys {
		if len(parts) == 2 || key.Repository == parts[2]+"/"+parts[3] && key.BaseBranch == parts[4] {
			selected = append(selected, key)
		}
	}
	if len(selected) == 0 && len(parts) > 2 {
		selected = nil
	}
	return selected, pr, limit, nil
}

func fail(w http.ResponseWriter, code int, name, message string) {
	w.WriteHeader(code)
	writeJSON(w, struct {
		Version int    `json:"version"`
		Error   string `json:"error"`
		Message string `json:"message"`
	}{Version, name, message})
}

func writeJSON(w http.ResponseWriter, value any) {
	// DTOs contain only JSON-safe values. A disconnected reader cannot change
	// committed state and must not cause a second response to be written.
	_ = json.NewEncoder(w).Encode(value)
}
