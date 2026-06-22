package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const defaultQuery = `{service_name="syslog"}`

type config struct {
	LokiURL  string
	Query    string
	StartRaw string
	EndRaw   string
	Out      string
	Last     time.Duration
	Limit    int
	Timeout  time.Duration
	TenantID string
	Bearer   string
}

type lokiResponse struct {
	Status    string `json:"status"`
	ErrorType string `json:"errorType,omitempty"`
	Error     string `json:"error,omitempty"`
	Data      struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Stream map[string]string `json:"stream"`
			Values [][2]string       `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

type entry struct {
	TS   int64
	Line string
}

func main() {
	cfg := parseConfig()

	end, err := parseEnd(cfg.EndRaw)
	if err != nil {
		log.Fatalf("invalid end time: %v", err)
	}

	start, err := parseStart(cfg.StartRaw, end, cfg.Last)
	if err != nil {
		log.Fatalf("invalid start time: %v", err)
	}

	if !start.Before(end) {
		log.Fatalf("start must be before end: start=%s end=%s", start.Format(time.RFC3339), end.Format(time.RFC3339))
	}

	startNS := start.UnixNano()
	endNS := end.UnixNano()

	if err := os.MkdirAll(filepath.Dir(cfg.Out), 0o755); err != nil && filepath.Dir(cfg.Out) != "." {
		log.Fatalf("create output directory: %v", err)
	}

	client := &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			MaxIdleConns:          10,
			MaxIdleConnsPerHost:   2,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
	defer client.CloseIdleConnections()

	out, err := os.Create(cfg.Out)
	if err != nil {
		log.Fatalf("create output file: %v", err)
	}
	defer out.Close()

	log.Printf("exporting query=%q start=%s end=%s out=%s limit=%d", cfg.Query, start.Format(time.RFC3339), end.Format(time.RFC3339), cfg.Out, cfg.Limit)

	total := 0
	for startNS < endNS {
		values, err := queryRange(client, cfg, startNS, endNS)
		if err != nil {
			log.Fatalf("query failed: %v", err)
		}

		if len(values) == 0 {
			break
		}

		for _, v := range values {
			if _, err := fmt.Fprintf(out, "%s %s\n", time.Unix(0, v.TS).UTC().Format(time.RFC3339Nano), v.Line); err != nil {
				log.Fatalf("write output: %v", err)
			}
		}

		lastTS := values[len(values)-1].TS
		if lastTS < startNS {
			log.Fatalf("pagination did not move forward: last timestamp %d is before start %d", lastTS, startNS)
		}

		startNS = lastTS + 1
		total += len(values)
		log.Printf("exported %d entries, total=%d, next_start=%d", len(values), total, startNS)
	}

	log.Printf("done, exported %d entries to %s", total, cfg.Out)
}

func parseConfig() config {
	cfg := config{}

	flag.StringVar(&cfg.LokiURL, "url", env("LOKI_URL", "http://localhost:3100"), "Base Loki URL, for example http://localhost:3100")
	flag.StringVar(&cfg.Query, "query", env("LOKI_QUERY", defaultQuery), "LogQL query")
	flag.StringVar(&cfg.StartRaw, "start", env("START", ""), "Start time: RFC3339, 'YYYY-MM-DD', or 'YYYY-MM-DD HH:MM:SS' in UTC")
	flag.StringVar(&cfg.EndRaw, "end", env("END", ""), "End time: RFC3339, 'YYYY-MM-DD', or 'YYYY-MM-DD HH:MM:SS' in UTC; default now")
	flag.StringVar(&cfg.Out, "out", env("OUT", "syslog_last_24h.log"), "Output file")
	flag.DurationVar(&cfg.Last, "last", envDuration("LAST", 24*time.Hour), "Relative range used when start is empty, for example 24h, 6h, 7d")
	flag.IntVar(&cfg.Limit, "limit", envInt("LIMIT", 5000), "Maximum log entries per Loki query_range request")
	flag.DurationVar(&cfg.Timeout, "timeout", envDuration("TIMEOUT", 5*time.Minute), "Timeout per HTTP request")
	flag.StringVar(&cfg.TenantID, "tenant-id", env("TENANT_ID", ""), "Optional Loki tenant ID sent as X-Scope-OrgID")
	flag.StringVar(&cfg.Bearer, "bearer", env("LOKI_BEARER_TOKEN", ""), "Optional bearer token for Loki")

	flag.Parse()

	cfg.LokiURL = strings.TrimRight(cfg.LokiURL, "/")
	if cfg.Limit <= 0 {
		log.Fatalf("limit must be > 0")
	}
	if cfg.Timeout <= 0 {
		log.Fatalf("timeout must be > 0")
	}
	if cfg.Last <= 0 {
		log.Fatalf("last must be > 0")
	}

	return cfg
}

func queryRange(client *http.Client, cfg config, startNS, endNS int64) ([]entry, error) {
	u, err := url.Parse(cfg.LokiURL + "/loki/api/v1/query_range")
	if err != nil {
		return nil, err
	}

	q := u.Query()
	q.Set("query", cfg.Query)
	q.Set("start", strconv.FormatInt(startNS, 10))
	q.Set("end", strconv.FormatInt(endNS, 10))
	q.Set("direction", "forward")
	q.Set("limit", strconv.Itoa(cfg.Limit))
	u.RawQuery = q.Encode()

	var lastErr error
	for attempt := 1; attempt <= 5; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			cancel()
			return nil, err
		}

		req.Header.Set("Accept", "application/json")
		if cfg.TenantID != "" {
			req.Header.Set("X-Scope-OrgID", cfg.TenantID)
		}
		if cfg.Bearer != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.Bearer)
		}

		resp, err := client.Do(req)
		if err != nil {
			cancel()
			lastErr = err
			if attempt < 5 {
				time.Sleep(backoff(attempt))
				continue
			}
			return nil, lastErr
		}

		body, readErr := io.ReadAll(resp.Body)
		closeErr := resp.Body.Close()
		cancel()

		if readErr != nil {
			lastErr = readErr
			if attempt < 5 {
				time.Sleep(backoff(attempt))
				continue
			}
			return nil, lastErr
		}
		if closeErr != nil {
			lastErr = closeErr
			if attempt < 5 {
				time.Sleep(backoff(attempt))
				continue
			}
			return nil, lastErr
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("temporary HTTP status %d: %s", resp.StatusCode, truncate(string(body), 500))
			if attempt < 5 {
				time.Sleep(backoff(attempt))
				continue
			}
			return nil, lastErr
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("HTTP status %d: %s", resp.StatusCode, truncate(string(body), 1000))
		}

		var lr lokiResponse
		if err := json.Unmarshal(body, &lr); err != nil {
			return nil, err
		}
		if lr.Status != "success" {
			if lr.Error != "" {
				return nil, fmt.Errorf("loki status=%s errorType=%s error=%s", lr.Status, lr.ErrorType, lr.Error)
			}
			return nil, fmt.Errorf("loki status=%s", lr.Status)
		}

		values := make([]entry, 0)
		for _, stream := range lr.Data.Result {
			for _, pair := range stream.Values {
				ts, err := strconv.ParseInt(pair[0], 10, 64)
				if err != nil {
					return nil, fmt.Errorf("parse timestamp %q: %w", pair[0], err)
				}
				values = append(values, entry{TS: ts, Line: pair[1]})
			}
		}

		sort.Slice(values, func(i, j int) bool { return values[i].TS < values[j].TS })
		return values, nil
	}

	if lastErr == nil {
		lastErr = errors.New("query failed")
	}
	return nil, lastErr
}

func parseStart(raw string, end time.Time, last time.Duration) (time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return end.Add(-last), nil
	}
	return parseTime(raw)
}

func parseEnd(raw string) (time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return time.Now().UTC(), nil
	}
	return parseTime(raw)
}

func parseTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		"2006-01-02",
	}

	for _, layout := range layouts {
		t, err := time.Parse(layout, raw)
		if err == nil {
			return t.UTC(), nil
		}
	}

	return time.Time{}, fmt.Errorf("%q; supported formats: RFC3339, YYYY-MM-DD, YYYY-MM-DD HH:MM:SS", raw)
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		log.Fatalf("invalid %s=%q: %v", key, value, err)
	}
	return parsed
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := parseDuration(value)
	if err != nil {
		log.Fatalf("invalid %s=%q: %v", key, value, err)
	}
	return parsed
}

func parseDuration(raw string) (time.Duration, error) {
	if strings.HasSuffix(raw, "d") {
		daysRaw := strings.TrimSuffix(raw, "d")
		days, err := strconv.Atoi(daysRaw)
		if err != nil {
			return 0, err
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(raw)
}

func backoff(attempt int) time.Duration {
	return time.Duration(attempt*attempt) * time.Second
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
