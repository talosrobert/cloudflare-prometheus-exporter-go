package cloudflareapi

import (
	"context"
	"time"
)

// errorMetricsQuery uses Cloudflare's httpRequestsAdaptiveGroups dataset,
// confirmed via live GraphQL introspection against
// api.cloudflare.com/client/v4/graphql — the only dataset that carries
// originResponseStatus/originResponseDurationMs and lets edgeResponseStatus be
// cross-tabulated with country and host; httpRequests1mGroups (used by
// analytics.go) only exposes single-dimension maps and has no origin fields
// at all. It lives under viewer.zones, needs no permission beyond what HTTP
// analytics already requires — verified live against a real zone.
//
// originResponseStatus is 0 for requests the origin was never contacted for
// (edge cache hit, edge block, redirect, ...), not a successful status; those
// rows must be excluded from origin-side aggregates or they'd dilute the
// origin error ratio and duration average with non-origin traffic.
//
// sum.edgeRequestBytes/edgeResponseBytes ride along on this same query (no
// extra API call) to back the by-host request/bandwidth metrics — this is the
// only dataset with a host dimension at all, since httpRequests1mGroups has
// none.
//
// wafAttackScoreClass/botManagementDecision/verifiedBotCategory likewise ride
// along to back the security-analytics metrics. Values confirmed live against
// a real zone: each is a small, bounded set of categories (4-8 distinct
// values), not a per-request field, so labeling by them directly is safe —
// unlike wafAttackScore itself (a raw 0-100 score), which would blow up
// cardinality if used as a label instead of the pre-bucketed *Class field.
const errorMetricsQuery = `
query ErrorMetrics($zoneIDs: [string!], $mintime: Time!, $maxtime: Time!, $limit: uint64!) {
  viewer {
    zones(filter: { zoneTag_in: $zoneIDs }) {
      zoneTag
      httpRequestsAdaptiveGroups(
        limit: $limit
        filter: { datetime_geq: $mintime, datetime_lt: $maxtime }
      ) {
        count
        avg {
          originResponseDurationMs
          sampleInterval
        }
        sum {
          edgeRequestBytes
          edgeResponseBytes
        }
        dimensions {
          edgeResponseStatus
          originResponseStatus
          clientCountryName
          clientRequestHTTPHost
          wafAttackScoreClass
          botManagementDecision
          verifiedBotCategory
        }
      }
    }
  }
}`

type errorMetricsResponse struct {
	Viewer struct {
		Zones []struct {
			ZoneTag                  string `json:"zoneTag"`
			HTTPRequestsAdaptiveRows []struct {
				Count float64 `json:"count"`
				Avg   struct {
					OriginResponseDurationMs float64 `json:"originResponseDurationMs"`
					SampleInterval           float64 `json:"sampleInterval"`
				} `json:"avg"`
				Sum struct {
					EdgeRequestBytes  float64 `json:"edgeRequestBytes"`
					EdgeResponseBytes float64 `json:"edgeResponseBytes"`
				} `json:"sum"`
				Dimensions struct {
					EdgeResponseStatus    int    `json:"edgeResponseStatus"`
					OriginResponseStatus  int    `json:"originResponseStatus"`
					ClientCountryName     string `json:"clientCountryName"`
					ClientRequestHost     string `json:"clientRequestHTTPHost"`
					WAFAttackScoreClass   string `json:"wafAttackScoreClass"`
					BotManagementDecision string `json:"botManagementDecision"`
					VerifiedBotCategory   string `json:"verifiedBotCategory"`
				} `json:"dimensions"`
			} `json:"httpRequestsAdaptiveGroups"`
		} `json:"zones"`
	} `json:"viewer"`
}

// ErrorGroup is one (zone, edge status, origin status, country, host) bucket
// of request volume for the requested window. OriginStatus is 0 when the
// origin was never contacted for that row. Count, EdgeRequestBytes, and
// EdgeResponseBytes are sampling-corrected estimates, not raw sampled sums.
type ErrorGroup struct {
	ZoneTag               string
	EdgeStatus            int
	OriginStatus          int
	Country               string
	Host                  string
	Count                 float64
	AvgOriginDurationMs   float64
	EdgeRequestBytes      float64
	EdgeResponseBytes     float64
	WAFAttackScoreClass   string
	BotManagementDecision string
	VerifiedBotCategory   string
}

// FetchErrorMetrics fetches per-request error/latency analytics for zoneIDs in
// a single GraphQL call — the zoneTag_in filter (via viewer.zones) covers
// every zone at once, so this never needs a per-zone fan-out.
func (c *Client) FetchErrorMetrics(ctx context.Context, zoneIDs []string, mintime, maxtime time.Time, limit int) ([]ErrorGroup, error) {
	if len(zoneIDs) == 0 {
		return nil, nil
	}

	variables := map[string]any{
		"zoneIDs": zoneIDs,
		"mintime": mintime.UTC().Format(time.RFC3339),
		"maxtime": maxtime.UTC().Format(time.RFC3339),
		"limit":   limit,
	}

	var resp errorMetricsResponse
	if err := c.gql.Query(ctx, errorMetricsQuery, variables, &resp); err != nil {
		return nil, err
	}

	var out []ErrorGroup
	for _, z := range resp.Viewer.Zones {
		for _, g := range z.HTTPRequestsAdaptiveRows {
			out = append(out, ErrorGroup{
				ZoneTag:               z.ZoneTag,
				EdgeStatus:            g.Dimensions.EdgeResponseStatus,
				OriginStatus:          g.Dimensions.OriginResponseStatus,
				Country:               g.Dimensions.ClientCountryName,
				Host:                  g.Dimensions.ClientRequestHost,
				Count:                 estimatedCount(g.Count, g.Avg.SampleInterval),
				AvgOriginDurationMs:   g.Avg.OriginResponseDurationMs,
				EdgeRequestBytes:      estimatedCount(g.Sum.EdgeRequestBytes, g.Avg.SampleInterval),
				EdgeResponseBytes:     estimatedCount(g.Sum.EdgeResponseBytes, g.Avg.SampleInterval),
				WAFAttackScoreClass:   g.Dimensions.WAFAttackScoreClass,
				BotManagementDecision: g.Dimensions.BotManagementDecision,
				VerifiedBotCategory:   g.Dimensions.VerifiedBotCategory,
			})
		}
	}
	return out, nil
}
