package cloudflareapi

import (
	"context"
	"time"
)

// dnsMetricsQuery uses Cloudflare's DNS Analytics dataset, confirmed via live
// GraphQL introspection against api.cloudflare.com/client/v4/graphql:
// dnsAnalyticsAdaptiveGroups lives under viewer.accounts (not viewer.zones,
// unlike HTTP analytics), and is scoped to specific zones via the
// zoneTag_in filter rather than an outer zones() selection.
const dnsMetricsQuery = `
query DNSMetrics($accountTag: String!, $zoneIDs: [string!], $mintime: Time!, $maxtime: Time!, $limit: uint64!) {
  viewer {
    accounts(filter: { accountTag: $accountTag }) {
      dnsAnalyticsAdaptiveGroups(
        limit: $limit
        filter: { zoneTag_in: $zoneIDs, datetime_geq: $mintime, datetime_lt: $maxtime }
      ) {
        count
        avg {
          sampleInterval
        }
        dimensions {
          zoneTag
          queryType
          responseCode
        }
      }
    }
  }
}`

type dnsMetricsResponse struct {
	Viewer struct {
		Accounts []struct {
			DNSAnalyticsAdaptiveGroups []struct {
				Count float64 `json:"count"`
				Avg   struct {
					SampleInterval float64 `json:"sampleInterval"`
				} `json:"avg"`
				Dimensions struct {
					ZoneTag      string `json:"zoneTag"`
					QueryType    string `json:"queryType"`
					ResponseCode string `json:"responseCode"`
				} `json:"dimensions"`
			} `json:"dnsAnalyticsAdaptiveGroups"`
		} `json:"accounts"`
	} `json:"viewer"`
}

// DNSQueryGroup is one (zone, query type, response code) bucket of DNS query
// volume for the requested window. Count is the sampling-corrected estimate
// of real queries, not the raw sampled records.
type DNSQueryGroup struct {
	ZoneTag      string
	QueryType    string
	ResponseCode string
	Count        float64
}

// FetchDNSMetrics fetches DNS query analytics for zoneIDs (all of which must
// belong to accountID) in a single GraphQL call — the zoneTag_in filter
// covers every zone at once, so this never needs a per-zone fan-out.
func (c *Client) FetchDNSMetrics(ctx context.Context, accountID string, zoneIDs []string, mintime, maxtime time.Time, limit int) ([]DNSQueryGroup, error) {
	if len(zoneIDs) == 0 {
		return nil, nil
	}

	variables := map[string]any{
		"accountTag": accountID,
		"zoneIDs":    zoneIDs,
		"mintime":    mintime.UTC().Format(time.RFC3339),
		"maxtime":    maxtime.UTC().Format(time.RFC3339),
		"limit":      limit,
	}

	var resp dnsMetricsResponse
	if err := c.gql.Query(ctx, dnsMetricsQuery, variables, &resp); err != nil {
		return nil, err
	}

	var out []DNSQueryGroup
	for _, acct := range resp.Viewer.Accounts {
		for _, g := range acct.DNSAnalyticsAdaptiveGroups {
			out = append(out, DNSQueryGroup{
				ZoneTag:      g.Dimensions.ZoneTag,
				QueryType:    g.Dimensions.QueryType,
				ResponseCode: g.Dimensions.ResponseCode,
				Count:        estimatedCount(g.Count, g.Avg.SampleInterval),
			})
		}
	}
	return out, nil
}
