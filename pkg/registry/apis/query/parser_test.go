package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"testing"

	data "github.com/grafana/grafana-plugin-sdk-go/experimental/apis/data/v0alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	query "github.com/grafana/grafana/pkg/apis/query/v0alpha1"
	"github.com/grafana/grafana/pkg/expr"
	"github.com/grafana/grafana/pkg/infra/log"
	"github.com/grafana/grafana/pkg/infra/tracing"
	"github.com/grafana/grafana/pkg/services/featuremgmt"
)

type parserTestObject struct {
	Description string                 `json:"description,omitempty"`
	Request     query.QueryDataRequest `json:"input"`
	Expect      parsedRequestInfo      `json:"expect"`
	Error       string                 `json:"error,omitempty"`
}

func TestQuerySplitting(t *testing.T) {
	ctx := context.Background()
	parser := newQueryParser(expr.NewExpressionQueryReader(featuremgmt.WithFeatures()),
		&legacyDataSourceRetriever{}, tracing.InitializeTracerForTest(), log.NewNopLogger())

	t.Run("missing datasource flavors", func(t *testing.T) {
		split, err := parser.parseRequest(ctx, &query.QueryDataRequest{
			QueryDataRequest: data.QueryDataRequest{
				Queries: []data.DataQuery{{
					CommonQueryProperties: data.CommonQueryProperties{
						RefID: "A",
					},
				}},
			},
		})
		require.Error(t, err) // Missing datasource
		require.Empty(t, split.Requests)
	})

	t.Run("applies zero time range if time range is missing", func(t *testing.T) {
		split, err := parser.parseRequest(ctx, &query.QueryDataRequest{
			QueryDataRequest: data.QueryDataRequest{
				TimeRange: data.TimeRange{}, // missing
				Queries: []data.DataQuery{{
					CommonQueryProperties: data.CommonQueryProperties{
						RefID: "A",
						Datasource: &data.DataSourceRef{
							Type: "x",
							UID:  "abc",
						},
					},
				}},
			},
		})
		require.NoError(t, err)
		require.Len(t, split.Requests, 1)
		require.Equal(t, "0", split.Requests[0].Request.From)
		require.Equal(t, "0", split.Requests[0].Request.To)
	})

	t.Run("forbid duplicate refId", func(t *testing.T) {
		_, err := parser.parseRequest(ctx, &query.QueryDataRequest{
			QueryDataRequest: data.QueryDataRequest{
				TimeRange: data.TimeRange{},
				Queries: []data.DataQuery{
					{
						CommonQueryProperties: data.CommonQueryProperties{
							RefID: "A",
							Datasource: &data.DataSourceRef{
								Type: "x",
								UID:  "abc",
							},
						},
					},
					{
						CommonQueryProperties: data.CommonQueryProperties{
							RefID: "A",
							Datasource: &data.DataSourceRef{
								Type: "x",
								UID:  "abc",
							},
						},
					},
				},
			},
		})
		require.Error(t, err)
	})
	t.Run("forbid duplicate refId, when refId=''", func(t *testing.T) {
		_, err := parser.parseRequest(ctx, &query.QueryDataRequest{
			QueryDataRequest: data.QueryDataRequest{
				TimeRange: data.TimeRange{},
				Queries: []data.DataQuery{
					{
						CommonQueryProperties: data.CommonQueryProperties{
							RefID: "",
							Datasource: &data.DataSourceRef{
								Type: "x",
								UID:  "abc",
							},
						},
					},
					{
						CommonQueryProperties: data.CommonQueryProperties{
							RefID: "",
							Datasource: &data.DataSourceRef{
								Type: "x",
								UID:  "abc",
							},
						},
					},
				},
			},
		})
		require.Error(t, err)
	})
	t.Run("allow empty refId", func(t *testing.T) {
		_, err := parser.parseRequest(ctx, &query.QueryDataRequest{
			QueryDataRequest: data.QueryDataRequest{
				TimeRange: data.TimeRange{},
				Queries: []data.DataQuery{
					{
						CommonQueryProperties: data.CommonQueryProperties{
							RefID: "",
							Datasource: &data.DataSourceRef{
								Type: "x",
								UID:  "abc",
							},
						},
					},
					{
						CommonQueryProperties: data.CommonQueryProperties{
							RefID: "B",
							Datasource: &data.DataSourceRef{
								Type: "x",
								UID:  "abc",
							},
						},
					},
				},
			},
		})
		require.NoError(t, err)
	})
	t.Run("applies query time range if present", func(t *testing.T) {
		split, err := parser.parseRequest(ctx, &query.QueryDataRequest{
			QueryDataRequest: data.QueryDataRequest{
				TimeRange: data.TimeRange{}, // missing
				Queries: []data.DataQuery{{
					CommonQueryProperties: data.CommonQueryProperties{
						RefID: "A",
						Datasource: &data.DataSourceRef{
							Type: "x",
							UID:  "abc",
						},
						TimeRange: &data.TimeRange{
							From: "now-1d",
							To:   "now",
						},
					},
				}},
			},
		})
		require.NoError(t, err)
		require.Len(t, split.Requests, 1)
		require.Equal(t, "now-1d", split.Requests[0].Request.From)
		require.Equal(t, "now", split.Requests[0].Request.To)
	})

	t.Run("applies query time range if all time ranges are present", func(t *testing.T) {
		split, err := parser.parseRequest(ctx, &query.QueryDataRequest{
			QueryDataRequest: data.QueryDataRequest{
				TimeRange: data.TimeRange{
					From: "now-1h",
					To:   "now",
				},
				Queries: []data.DataQuery{{
					CommonQueryProperties: data.CommonQueryProperties{
						RefID: "A",
						Datasource: &data.DataSourceRef{
							Type: "x",
							UID:  "abc",
						},
						TimeRange: &data.TimeRange{
							From: "now-1d",
							To:   "now",
						},
					},
				}},
			},
		})
		require.NoError(t, err)
		require.Len(t, split.Requests, 1)
		require.Equal(t, "now-1d", split.Requests[0].Request.From)
		require.Equal(t, "now", split.Requests[0].Request.To)
	})
	t.Run("verify tests", func(t *testing.T) {
		files, err := os.ReadDir("testdata")
		require.NoError(t, err)

		for _, file := range files {
			if !strings.HasSuffix(file.Name(), ".json") {
				continue
			}

			t.Run(file.Name(), func(t *testing.T) {
				fpath := path.Join("testdata", file.Name())
				// nolint:gosec
				body, err := os.ReadFile(fpath)
				require.NoError(t, err)
				harness := &parserTestObject{}
				err = json.Unmarshal(body, harness)
				require.NoError(t, err)

				changed := false
				parsed, err := parser.parseRequest(ctx, &harness.Request)
				if err != nil {
					if !assert.Equal(t, harness.Error, err.Error(), "File %s", file) {
						changed = true
					}
				} else {
					x, _ := json.Marshal(parsed)
					y, _ := json.Marshal(harness.Expect)
					if !assert.JSONEq(t, string(y), string(x), "File %s", file) {
						changed = true
					}
				}

				if changed {
					harness.Error = ""
					harness.Expect = parsed
					if err != nil {
						harness.Error = err.Error()
					}
					jj, err := json.MarshalIndent(harness, "", "  ")
					require.NoError(t, err)
					err = os.WriteFile(fpath, jj, 0600)
					require.NoError(t, err)
				}
			})
		}
	})
}

func TestSqlInputs(t *testing.T) {
	parser := newQueryParser(
		expr.NewExpressionQueryReader(featuremgmt.WithFeatures(featuremgmt.FlagSqlExpressions)),
		nil,
		tracing.InitializeTracerForTest(),
		log.NewNopLogger(),
	)

	parsedRequestInfo, err := parser.parseRequest(context.Background(), &query.QueryDataRequest{
		QueryDataRequest: data.QueryDataRequest{
			Queries: []data.DataQuery{
				data.NewDataQuery(map[string]any{
					"refId": "A",
					"datasource": &data.DataSourceRef{
						Type: "prometheus",
						UID:  "local-prom",
					},
				}),
				data.NewDataQuery(map[string]any{
					"refId": "B",
					"datasource": &data.DataSourceRef{
						Type: "__expr__",
						UID:  "__expr__",
					},
					"type":       "sql",
					"expression": "Select time, value + 10 from A",
				}),
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, parsedRequestInfo.SqlInputs["B"], struct{}{})
}

func TestSqlCTE(t *testing.T) {
	parser := newQueryParser(
		expr.NewExpressionQueryReader(featuremgmt.WithFeatures(featuremgmt.FlagSqlExpressions)),
		nil,
		tracing.InitializeTracerForTest(),
		log.NewNopLogger(),
	)

	parsedRequestInfo, err := parser.parseRequest(context.Background(), &query.QueryDataRequest{
		QueryDataRequest: data.QueryDataRequest{
			Queries: []data.DataQuery{
				data.NewDataQuery(map[string]any{
					"refId": "A",
					"datasource": &data.DataSourceRef{
						Type: "prometheus",
						UID:  "local-prom",
					},
				}),
				data.NewDataQuery(map[string]any{
					"refId": "B",
					"datasource": &data.DataSourceRef{
						Type: "__expr__",
						UID:  "__expr__",
					},
					"type": "sql",
					"expression": `WITH CTE AS (
						SELECT
							Month
						FROM A
						)

						SELECT * FROM CTE`,
				}),
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, parsedRequestInfo.SqlInputs["B"], struct{}{})
}

func TestGrafanaDS(t *testing.T) {
	ctx := context.Background()
	parser := newQueryParser(expr.NewExpressionQueryReader(featuremgmt.WithFeatures()),
		&noLegacyRetriever{}, tracing.InitializeTracerForTest(), log.NewNopLogger())

	t.Run("grafana ds without type", func(t *testing.T) {
		parsed, err := parser.parseRequest(ctx, &query.QueryDataRequest{
			QueryDataRequest: data.QueryDataRequest{
				Queries: []data.DataQuery{{
					CommonQueryProperties: data.CommonQueryProperties{
						RefID: "A",
						Datasource: &data.DataSourceRef{
							UID: "grafana",
						},
					},
				}},
			},
		})
		require.NoError(t, err)
		require.Len(t, parsed.Requests, 1)
		require.Equal(t, "grafana", parsed.Requests[0].PluginId)
		require.Equal(t, "grafana", parsed.Requests[0].UID)
	})

	t.Run("grafana ds with different type", func(t *testing.T) {
		parsed, err := parser.parseRequest(ctx, &query.QueryDataRequest{
			QueryDataRequest: data.QueryDataRequest{
				Queries: []data.DataQuery{{
					CommonQueryProperties: data.CommonQueryProperties{
						RefID: "A",
						Datasource: &data.DataSourceRef{
							UID:  "grafana",
							Type: "datasource",
						},
					},
				}},
			},
		})
		require.NoError(t, err)
		require.Len(t, parsed.Requests, 1)
		require.Equal(t, "grafana", parsed.Requests[0].PluginId)
		require.Equal(t, "grafana", parsed.Requests[0].UID)
	})
}

type noLegacyRetriever struct{}

var errNoLegacy = errors.New("legacy dds retriever reached, it should not")

func (s *noLegacyRetriever) GetDataSourceFromDeprecatedFields(ctx context.Context, name string, id int64) (*data.DataSourceRef, error) {
	return nil, errNoLegacy
}

type legacyDataSourceRetriever struct{}

func (s *legacyDataSourceRetriever) GetDataSourceFromDeprecatedFields(ctx context.Context, name string, id int64) (*data.DataSourceRef, error) {
	if id == 100 {
		return &data.DataSourceRef{
			Type: "plugin-aaaa",
			UID:  "AAA",
		}, nil
	}
	if name != "" {
		return &data.DataSourceRef{
			Type: "plugin-bbb",
			UID:  name,
		}, nil
	}
	return nil, fmt.Errorf("missing parameter")
}
