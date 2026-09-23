package cloudflareapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const graphqlEndpoint = "https://api.cloudflare.com/client/v4/graphql"

// GraphQLClient is a minimal client for Cloudflare's GraphQL Analytics API,
// which the official cloudflare-go SDK does not expose (it only wraps the
// REST API; see docs/decisions.md).
type GraphQLClient struct {
	apiToken   string
	httpClient *http.Client
}

func NewGraphQLClient(apiToken string, httpClient *http.Client) *GraphQLClient {
	return &GraphQLClient{apiToken: apiToken, httpClient: httpClient}
}

type graphqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

type graphqlError struct {
	Message string `json:"message"`
}

type graphqlResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []graphqlError  `json:"errors"`
}

// Query executes a GraphQL query/variables pair and decodes the "data" field
// of the response into out.
func (c *GraphQLClient) Query(ctx context.Context, query string, variables map[string]any, out any) error {
	body, err := json.Marshal(graphqlRequest{Query: query, Variables: variables})
	if err != nil {
		return fmt.Errorf("encoding graphql request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, graphqlEndpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building graphql request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("calling graphql endpoint: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading graphql response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("graphql endpoint returned %s: %s", resp.Status, string(respBody))
	}

	var parsed graphqlResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return fmt.Errorf("decoding graphql response: %w", err)
	}
	if len(parsed.Errors) > 0 {
		return fmt.Errorf("graphql errors: %s", parsed.Errors[0].Message)
	}
	if err := json.Unmarshal(parsed.Data, out); err != nil {
		return fmt.Errorf("decoding graphql data: %w", err)
	}
	return nil
}
