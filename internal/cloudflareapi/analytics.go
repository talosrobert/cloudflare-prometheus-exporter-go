package cloudflareapi

import (
	"context"
	"time"
)

// httpMetricsQuery is ported from the original TypeScript exporter's
// HTTPMetricsQuery (src/cloudflare/gql/queries.ts), dropping
// firewallEventsAdaptiveGroups/bot fields — firewall metrics are out of scope
// for this rewrite's core subset.
const httpMetricsQuery = `
query HTTPMetrics($zoneIDs: [string!], $mintime: Time!, $maxtime: Time!, $limit: uint64!) {
  viewer {
    zones(filter: { zoneTag_in: $zoneIDs }) {
      zoneTag
      httpRequests1mGroups(
        limit: $limit
        filter: { datetime_geq: $mintime, datetime_lt: $maxtime }
      ) {
        uniq {
          uniques
        }
        sum {
          browserMap {
            pageViews
            uaBrowserFamily
          }
          bytes
          cachedBytes
          cachedRequests
          contentTypeMap {
            bytes
            requests
            edgeResponseContentTypeName
          }
          countryMap {
            bytes
            clientCountryName
            requests
            threats
          }
          encryptedBytes
          encryptedRequests
          pageViews
          requests
          responseStatusMap {
            edgeResponseStatus
            requests
          }
          threatPathingMap {
            requests
            threatPathingName
          }
          threats
          clientHTTPVersionMap {
            clientHTTPProtocol
            requests
          }
          clientSSLMap {
            clientSSLProtocol
            requests
          }
          ipClassMap {
            ipType
            requests
          }
        }
        dimensions {
          datetime
        }
      }
    }
  }
}`

type httpMetricsResponse struct {
	Viewer struct {
		Zones []struct {
			ZoneTag              string                `json:"zoneTag"`
			HTTPRequests1mGroups []httpRequests1mGroup `json:"httpRequests1mGroups"`
		} `json:"zones"`
	} `json:"viewer"`
}

type httpRequests1mGroup struct {
	Uniq struct {
		Uniques float64 `json:"uniques"`
	} `json:"uniq"`
	Sum struct {
		BrowserMap []struct {
			PageViews       float64 `json:"pageViews"`
			UaBrowserFamily string  `json:"uaBrowserFamily"`
		} `json:"browserMap"`
		Bytes          float64 `json:"bytes"`
		CachedBytes    float64 `json:"cachedBytes"`
		CachedRequests float64 `json:"cachedRequests"`
		ContentTypeMap []struct {
			Bytes                       float64 `json:"bytes"`
			Requests                    float64 `json:"requests"`
			EdgeResponseContentTypeName string  `json:"edgeResponseContentTypeName"`
		} `json:"contentTypeMap"`
		CountryMap []struct {
			Bytes             float64 `json:"bytes"`
			ClientCountryName string  `json:"clientCountryName"`
			Requests          float64 `json:"requests"`
			Threats           float64 `json:"threats"`
		} `json:"countryMap"`
		EncryptedBytes    float64 `json:"encryptedBytes"`
		EncryptedRequests float64 `json:"encryptedRequests"`
		PageViews         float64 `json:"pageViews"`
		Requests          float64 `json:"requests"`
		ResponseStatusMap []struct {
			EdgeResponseStatus int     `json:"edgeResponseStatus"`
			Requests           float64 `json:"requests"`
		} `json:"responseStatusMap"`
		ThreatPathingMap []struct {
			Requests          float64 `json:"requests"`
			ThreatPathingName string  `json:"threatPathingName"`
		} `json:"threatPathingMap"`
		Threats              float64 `json:"threats"`
		ClientHTTPVersionMap []struct {
			ClientHTTPProtocol string  `json:"clientHTTPProtocol"`
			Requests           float64 `json:"requests"`
		} `json:"clientHTTPVersionMap"`
		ClientSSLMap []struct {
			ClientSSLProtocol string  `json:"clientSSLProtocol"`
			Requests          float64 `json:"requests"`
		} `json:"clientSSLMap"`
		IPClassMap []struct {
			IPType   string  `json:"ipType"`
			Requests float64 `json:"requests"`
		} `json:"ipClassMap"`
	} `json:"sum"`
	Dimensions struct {
		Datetime string `json:"datetime"`
	} `json:"dimensions"`
}

// ZoneHTTPMetrics is the per-zone slice of httpMetricsResponse callers use;
// it is exactly one httpRequests1mGroups[0] (the exporter always aggregates
// over a single time window per scrape, matching the original implementation).
type ZoneHTTPMetrics struct {
	ZoneTag string
	Group   httpRequests1mGroup
	HasData bool
}

// FetchHTTPMetrics fetches one time-window's HTTP analytics for zoneIDs in a
// single GraphQL call — Cloudflare's viewer.zones filter accepts the whole
// zone-ID list at once, so this never needs a per-zone fan-out.
func (c *Client) FetchHTTPMetrics(ctx context.Context, zoneIDs []string, mintime, maxtime time.Time, limit int) ([]ZoneHTTPMetrics, error) {
	if len(zoneIDs) == 0 {
		return nil, nil
	}

	variables := map[string]any{
		"zoneIDs": zoneIDs,
		"mintime": mintime.UTC().Format(time.RFC3339),
		"maxtime": maxtime.UTC().Format(time.RFC3339),
		"limit":   limit,
	}

	var resp httpMetricsResponse
	if err := c.gql.Query(ctx, httpMetricsQuery, variables, &resp); err != nil {
		return nil, err
	}

	out := make([]ZoneHTTPMetrics, 0, len(resp.Viewer.Zones))
	for _, z := range resp.Viewer.Zones {
		m := ZoneHTTPMetrics{ZoneTag: z.ZoneTag}
		if len(z.HTTPRequests1mGroups) > 0 {
			m.Group = z.HTTPRequests1mGroups[0]
			m.HasData = true
		}
		out = append(out, m)
	}
	return out, nil
}
