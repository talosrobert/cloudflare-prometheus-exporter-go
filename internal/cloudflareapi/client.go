// Package cloudflareapi wraps the parts of the Cloudflare API the exporter
// needs: account/zone discovery, resource-tag lookup, and (via graphql.go) the
// GraphQL Analytics API, which the official SDK does not cover.
package cloudflareapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	cloudflare "github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/accounts"
	"github.com/cloudflare/cloudflare-go/v7/option"
	"github.com/cloudflare/cloudflare-go/v7/resource_tagging"
	"github.com/cloudflare/cloudflare-go/v7/zones"
)

// Account is a Cloudflare account visible to the API token.
type Account struct {
	ID   string
	Name string
}

// Zone is a Cloudflare zone and the account it belongs to.
type Zone struct {
	ID      string
	Name    string
	Status  string
	Account Account
}

// TagFilter mirrors config.TagFilter without importing the config package,
// keeping this package independently usable/testable.
type TagFilter struct {
	Key    string
	Value  string
	Negate bool
}

func (f TagFilter) queryString() string {
	switch {
	case f.Value == "" && f.Negate:
		return "!" + f.Key
	case f.Value == "":
		return f.Key
	case f.Negate:
		return f.Key + "!=" + f.Value
	default:
		return f.Key + "=" + f.Value
	}
}

// Client talks to both the Cloudflare REST API (via the official SDK) and the
// GraphQL Analytics API (via GraphQLClient) with one API token.
type Client struct {
	api *cloudflare.Client
	gql *GraphQLClient
}

// NewClient builds a Client authenticated with apiToken.
func NewClient(apiToken string) *Client {
	httpClient := &http.Client{Timeout: 30 * time.Second}
	return &Client{
		api: cloudflare.NewClient(
			option.WithAPIToken(apiToken),
			option.WithHTTPClient(httpClient),
		),
		gql: NewGraphQLClient(apiToken, httpClient),
	}
}

// ListAccounts returns every account visible to the API token.
func (c *Client) ListAccounts(ctx context.Context) ([]Account, error) {
	var out []Account
	iter := c.api.Accounts.ListAutoPaging(ctx, accounts.AccountListParams{})
	for iter.Next() {
		a := iter.Current()
		out = append(out, Account{ID: a.ID, Name: a.Name})
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("listing accounts: %w", err)
	}
	return out, nil
}

// ListZones returns every zone belonging to accountID.
func (c *Client) ListZones(ctx context.Context, accountID string) ([]Zone, error) {
	var out []Zone
	params := zones.ZoneListParams{
		Account: cloudflare.F(zones.ZoneListParamsAccount{ID: cloudflare.F(accountID)}),
	}
	iter := c.api.Zones.ListAutoPaging(ctx, params)
	for iter.Next() {
		z := iter.Current()
		out = append(out, Zone{
			ID:      z.ID,
			Name:    z.Name,
			Status:  string(z.Status),
			Account: Account{ID: z.Account.ID, Name: z.Account.Name},
		})
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("listing zones for account %s: %w", accountID, err)
	}
	return out, nil
}

// ZoneTags fetches tags for zone-type resources in accountID matching all of
// filters (AND logic), using Cloudflare's Resource Tagging API server-side
// filter so a single call covers every zone in the account — no per-zone
// fan-out. A nil/empty filters list returns every tagged zone in the account.
//
// Returned map is keyed by zone ID; zones with no tags at all are not present.
func (c *Client) ZoneTags(ctx context.Context, accountID string, filters []TagFilter) (map[string]map[string]string, error) {
	tagQuery := make([]string, 0, len(filters))
	for _, f := range filters {
		tagQuery = append(tagQuery, f.queryString())
	}

	params := resource_tagging.ResourceTaggingListParams{
		AccountID: cloudflare.F(accountID),
		Type:      cloudflare.F([]resource_tagging.ResourceTaggingListParamsType{resource_tagging.ResourceTaggingListParamsTypeZone}),
	}
	if len(tagQuery) > 0 {
		params.Tag = cloudflare.F(tagQuery)
	}

	result := make(map[string]map[string]string)
	iter := c.api.ResourceTagging.ListAutoPaging(ctx, params)
	for iter.Next() {
		item := iter.Current()
		if item.ZoneID == "" {
			continue
		}
		result[item.ZoneID] = tagsOf(item)
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("listing zone tags for account %s: %w", accountID, err)
	}
	return result, nil
}

// tagsOf extracts the tag map from a tagged-resource response. The typed union
// variant is preferred; the fallback handles the raw decoded shape, which the
// SDK produces as map[string]any (not the map[string]string its doc comment
// claims) because interface{} fields are filled straight from gjson.
func tagsOf(item resource_tagging.ResourceTaggingListResponse) map[string]string {
	if z, ok := item.AsUnion().(resource_tagging.ResourceTaggingListResponseResourceTaggingTaggedResourceObjectZone); ok && z.Tags != nil {
		return z.Tags
	}
	raw, ok := item.Tags.(map[string]any)
	if !ok {
		return map[string]string{}
	}
	tags := make(map[string]string, len(raw))
	for k, v := range raw {
		tags[k] = fmt.Sprint(v)
	}
	return tags
}
