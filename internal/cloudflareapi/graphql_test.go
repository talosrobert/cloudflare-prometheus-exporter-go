package cloudflareapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestGraphQL(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	gql := NewGraphQLClient("test-token", srv.Client())
	gql.endpoint = srv.URL
	return &Client{gql: gql}
}

func TestGraphQLClient_Query(t *testing.T) {
	c := newTestGraphQL(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		var req graphqlRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(req.Query, "httpRequests1mGroups") {
			t.Error("expected HTTP metrics query body")
		}
		if ids, _ := req.Variables["zoneIDs"].([]any); len(ids) != 1 || ids[0] != "zone-1" {
			t.Errorf("zoneIDs variable = %v", req.Variables["zoneIDs"])
		}
		_, _ = w.Write([]byte(`{"data":{"viewer":{"zones":[{"zoneTag":"zone-1","httpRequests1mGroups":[{"uniq":{"uniques":7},"sum":{"requests":42,"cachedRequests":21,"responseStatusMap":[{"edgeResponseStatus":200,"requests":40}]},"dimensions":{"datetime":"2026-09-23T10:00:00Z"}}]}]}}}`))
	})

	now := time.Now()
	got, err := c.FetchHTTPMetrics(t.Context(), []string{"zone-1"}, now.Add(-time.Minute), now, 100)
	if err != nil {
		t.Fatalf("FetchHTTPMetrics() error = %v", err)
	}
	if len(got) != 1 || !got[0].HasData {
		t.Fatalf("got %+v, want one zone with data", got)
	}
	if got[0].Group.Sum.Requests != 42 || got[0].Group.Uniq.Uniques != 7 {
		t.Errorf("unexpected values: %+v", got[0].Group)
	}
	if len(got[0].Group.Sum.ResponseStatusMap) != 1 || got[0].Group.Sum.ResponseStatusMap[0].EdgeResponseStatus != 200 {
		t.Errorf("unexpected status map: %+v", got[0].Group.Sum.ResponseStatusMap)
	}
}

func TestGraphQLClient_Errors(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{"graphql errors array", http.StatusOK, `{"data":null,"errors":[{"message":"zone not found"}]}`, "zone not found"},
		{"non-200", http.StatusBadGateway, `upstream down`, "502"},
		{"malformed json", http.StatusOK, `{not json`, "decoding graphql response"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestGraphQL(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			_, err := c.FetchHTTPMetrics(t.Context(), []string{"z"}, time.Now(), time.Now(), 1)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestClient_FetchDNSMetrics(t *testing.T) {
	c := newTestGraphQL(t, func(w http.ResponseWriter, r *http.Request) {
		var req graphqlRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(req.Query, "dnsAnalyticsAdaptiveGroups") {
			t.Error("expected DNS analytics query body")
		}
		if req.Variables["accountTag"] != "acct-1" {
			t.Errorf("accountTag variable = %v", req.Variables["accountTag"])
		}
		_, _ = w.Write([]byte(`{"data":{"viewer":{"accounts":[{"dnsAnalyticsAdaptiveGroups":[
			{"count":10,"dimensions":{"zoneTag":"zone-1","queryType":"A","responseCode":"NOERROR"}},
			{"count":1,"dimensions":{"zoneTag":"zone-1","queryType":"A","responseCode":"NXDOMAIN"}}
		]}]}}}`))
	})

	got, err := c.FetchDNSMetrics(t.Context(), "acct-1", []string{"zone-1"}, time.Now().Add(-time.Minute), time.Now(), 100)
	if err != nil {
		t.Fatalf("FetchDNSMetrics() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d groups, want 2: %+v", len(got), got)
	}
	if got[0].Count != 10 || got[0].QueryType != "A" || got[0].ResponseCode != "NOERROR" {
		t.Errorf("unexpected first group: %+v", got[0])
	}
}

func TestClient_FetchWAFMetrics(t *testing.T) {
	c := newTestGraphQL(t, func(w http.ResponseWriter, r *http.Request) {
		var req graphqlRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(req.Query, "firewallEventsAdaptiveGroups") {
			t.Error("expected WAF events query body")
		}
		_, _ = w.Write([]byte(`{"data":{"viewer":{"zones":[{"zoneTag":"zone-1","firewallEventsAdaptiveGroups":[
			{"count":5,"dimensions":{"action":"block","source":"waf","ruleId":"abc123","clientCountryName":"US"}},
			{"count":2,"dimensions":{"action":"challenge","source":"botManagement","ruleId":"","clientCountryName":"DE"}}
		]}]}}}`))
	})

	got, err := c.FetchWAFMetrics(t.Context(), []string{"zone-1"}, time.Now().Add(-time.Minute), time.Now(), 100)
	if err != nil {
		t.Fatalf("FetchWAFMetrics() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d groups, want 2: %+v", len(got), got)
	}
	if got[0].Count != 5 || got[0].Action != "block" || got[0].Source != "waf" || got[0].RuleID != "abc123" || got[0].Country != "US" {
		t.Errorf("unexpected first group: %+v", got[0])
	}
}

func TestFetchWAFMetrics_NoZones(t *testing.T) {
	c := newTestGraphQL(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("no request expected for empty zone list")
	})
	got, err := c.FetchWAFMetrics(t.Context(), nil, time.Now(), time.Now(), 1)
	if err != nil || got != nil {
		t.Fatalf("got %v, %v; want nil, nil", got, err)
	}
}

func TestFetchDNSMetrics_NoZones(t *testing.T) {
	c := newTestGraphQL(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("no request expected for empty zone list")
	})
	got, err := c.FetchDNSMetrics(t.Context(), "acct-1", nil, time.Now(), time.Now(), 1)
	if err != nil || got != nil {
		t.Fatalf("got %v, %v; want nil, nil", got, err)
	}
}

func TestFetchHTTPMetrics_NoZones(t *testing.T) {
	c := newTestGraphQL(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("no request expected for empty zone list")
	})
	got, err := c.FetchHTTPMetrics(t.Context(), nil, time.Now(), time.Now(), 1)
	if err != nil || got != nil {
		t.Fatalf("got %v, %v; want nil, nil", got, err)
	}
}
