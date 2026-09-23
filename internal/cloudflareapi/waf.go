package cloudflareapi

import (
	"context"
	"time"
)

// wafMetricsQuery uses Cloudflare's firewallEventsAdaptiveGroups dataset,
// confirmed via live GraphQL introspection against
// api.cloudflare.com/client/v4/graphql. It lives under viewer.zones (like
// httpRequests1mGroups) and needs no permission beyond what HTTP analytics
// already requires — verified live: an all-permissions-but-bot-management
// token got an empty, error-free result rather than an authz error.
//
// source/kind aren't filtered to a "WAF-only" subset here: their exact values
// (waf, botManagement, rateLimit, l7ddos, ...) could not be confirmed against
// real event data (this account had none in the sampled window), so guessing
// a filter value risks silently returning zero rows. They're exposed as
// labels instead, same as DNS query_type/response_code.
const wafMetricsQuery = `
query WAFMetrics($zoneIDs: [string!], $mintime: Time!, $maxtime: Time!, $limit: uint64!) {
  viewer {
    zones(filter: { zoneTag_in: $zoneIDs }) {
      zoneTag
      firewallEventsAdaptiveGroups(
        limit: $limit
        filter: { datetime_geq: $mintime, datetime_lt: $maxtime }
      ) {
        count
        avg {
          sampleInterval
        }
        dimensions {
          action
          source
          ruleId
          clientCountryName
        }
      }
    }
  }
}`

type wafMetricsResponse struct {
	Viewer struct {
		Zones []struct {
			ZoneTag                      string `json:"zoneTag"`
			FirewallEventsAdaptiveGroups []struct {
				Count float64 `json:"count"`
				Avg   struct {
					SampleInterval float64 `json:"sampleInterval"`
				} `json:"avg"`
				Dimensions struct {
					Action            string `json:"action"`
					Source            string `json:"source"`
					RuleID            string `json:"ruleId"`
					ClientCountryName string `json:"clientCountryName"`
				} `json:"dimensions"`
			} `json:"firewallEventsAdaptiveGroups"`
		} `json:"zones"`
	} `json:"viewer"`
}

// WAFEventGroup is one (zone, action, source, rule, country) bucket of
// firewall/WAF event volume for the requested window. Count is the
// sampling-corrected estimate of real events, not the raw sampled records.
type WAFEventGroup struct {
	ZoneTag string
	Action  string
	Source  string
	RuleID  string
	Country string
	Count   float64
}

// FetchWAFMetrics fetches firewall/WAF event analytics for zoneIDs in a
// single GraphQL call — the zoneTag_in filter (via viewer.zones) covers every
// zone at once, so this never needs a per-zone fan-out.
func (c *Client) FetchWAFMetrics(ctx context.Context, zoneIDs []string, mintime, maxtime time.Time, limit int) ([]WAFEventGroup, error) {
	if len(zoneIDs) == 0 {
		return nil, nil
	}

	variables := map[string]any{
		"zoneIDs": zoneIDs,
		"mintime": mintime.UTC().Format(time.RFC3339),
		"maxtime": maxtime.UTC().Format(time.RFC3339),
		"limit":   limit,
	}

	var resp wafMetricsResponse
	if err := c.gql.Query(ctx, wafMetricsQuery, variables, &resp); err != nil {
		return nil, err
	}

	var out []WAFEventGroup
	for _, z := range resp.Viewer.Zones {
		for _, g := range z.FirewallEventsAdaptiveGroups {
			out = append(out, WAFEventGroup{
				ZoneTag: z.ZoneTag,
				Action:  g.Dimensions.Action,
				Source:  g.Dimensions.Source,
				RuleID:  g.Dimensions.RuleID,
				Country: g.Dimensions.ClientCountryName,
				Count:   estimatedCount(g.Count, g.Avg.SampleInterval),
			})
		}
	}
	return out, nil
}
