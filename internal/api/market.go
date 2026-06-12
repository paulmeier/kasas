package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/market"
)

// MarketSeriesDTO is one configured market series with its cache freshness, for
// the read API. as_of is the newest cached close date; fresh reports whether the
// cache is within the provider TTL (clients show "as of <date>" honestly).
type MarketSeriesDTO struct {
	ID        string `json:"id"`
	Symbol    string `json:"symbol"`
	Kind      string `json:"kind"`
	Currency  string `json:"currency"`
	Adjusted  bool   `json:"adjusted"`
	Name      string `json:"name,omitempty"`
	Provider  string `json:"provider"`
	AsOf      string `json:"as_of,omitempty"`
	Points    int    `json:"points"`
	FetchedAt int64  `json:"fetched_at,omitempty"`
	Fresh     bool   `json:"fresh"`
}

// MarketPointDTO is one daily close: an ISO date and a decimal-string value.
type MarketPointDTO struct {
	Date  string `json:"date"`
	Value string `json:"value"`
}

func toMarketSeriesDTO(s market.Series) MarketSeriesDTO {
	return MarketSeriesDTO{
		ID:        s.ID,
		Symbol:    s.Symbol,
		Kind:      string(s.Kind),
		Currency:  s.Currency,
		Adjusted:  s.Adjusted,
		Name:      s.Name,
		Provider:  s.Provider,
		AsOf:      s.AsOf,
		Points:    s.Points,
		FetchedAt: s.FetchedAt,
		Fresh:     s.Fresh,
	}
}

func toMarketSeriesDTOs(in []market.Series) []MarketSeriesDTO {
	out := make([]MarketSeriesDTO, len(in))
	for i, s := range in {
		out[i] = toMarketSeriesDTO(s)
	}
	return out
}

func toMarketPointDTOs(in []market.Point) []MarketPointDTO {
	out := make([]MarketPointDTO, len(in))
	for i, p := range in {
		out[i] = MarketPointDTO{Date: p.Date, Value: p.Value}
	}
	return out
}

// handleListMarketSeries lists every configured series with its cache freshness.
// Registered even when market data is unavailable so the dashboard gets a clean
// {enabled:false} response, not a routing 404.
func (s *Server) handleListMarketSeries(w http.ResponseWriter, r *http.Request) {
	if s.market == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "series": []MarketSeriesDTO{}})
		return
	}
	series, err := s.market.ListSeries(r.Context())
	if err != nil {
		s.serverError(w, "list market series", err)
		return
	}
	configured, err := s.market.Configured(r.Context())
	if err != nil {
		s.logger.Warn("read market provider status", "error", err)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"enabled":    true,
		"provider":   s.market.ProviderName(),
		"configured": configured,
		"series":     toMarketSeriesDTOs(series),
	})
}

// handleGetMarketPoints serves a series' daily closes through the read-through
// cache (cold → fetch under a timeout; stale → serve + background refresh; fresh →
// cache hit), bounded by optional since/until ISO dates.
func (s *Server) handleGetMarketPoints(w http.ResponseWriter, r *http.Request) {
	if s.market == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "points": []MarketPointDTO{}})
		return
	}
	id := chi.URLParam(r, "id")
	since := strings.TrimSpace(r.URL.Query().Get("since"))
	until := strings.TrimSpace(r.URL.Query().Get("until"))

	res, err := s.market.Points(r.Context(), id, since, until)
	if err != nil {
		switch {
		case errors.Is(err, market.ErrUnknownSeries):
			s.writeError(w, http.StatusNotFound, "unknown market series "+id)
		case errors.Is(err, market.ErrNotConfigured):
			s.writeError(w, http.StatusConflict, err.Error())
		default:
			// A cold fetch failure is almost always an upstream provider error (a
			// bad symbol, a rate-limit notice); surface it as a bad gateway.
			s.logger.Warn("fetch market points", "series", id, "error", err)
			s.writeError(w, http.StatusBadGateway, "could not fetch series: "+err.Error())
		}
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"provider": res.Provider,
		"as_of":    res.AsOf,
		"fresh":    res.Fresh,
		"points":   toMarketPointDTOs(res.Points),
	})
}

// marketSeriesInput is the body to define/add a series (admin).
type marketSeriesInput struct {
	ID       string `json:"id"`
	Symbol   string `json:"symbol"`
	Kind     string `json:"kind,omitempty"`
	Currency string `json:"currency,omitempty"`
	Adjusted bool   `json:"adjusted,omitempty"`
	Name     string `json:"name,omitempty"`
}

func (in marketSeriesInput) spec() market.SeriesSpec {
	return market.SeriesSpec{
		ID:       in.ID,
		Symbol:   in.Symbol,
		Kind:     market.Kind(strings.TrimSpace(in.Kind)),
		Currency: in.Currency,
		Adjusted: in.Adjusted,
		Name:     in.Name,
	}
}

// handleAddMarketSeries defines a new series (admin). It validates the spec,
// persists the configured list, and applies it live (no restart).
func (s *Server) handleAddMarketSeries(w http.ResponseWriter, r *http.Request) {
	if s.market == nil {
		s.writeError(w, http.StatusServiceUnavailable, "market data is not available")
		return
	}
	var in marketSeriesInput
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	if err := dec.Decode(&in); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	dto, err := s.addMarketSeries(r.Context(), in.spec())
	switch {
	case isValidationError(err):
		s.writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, market.ErrDuplicateID):
		s.writeError(w, http.StatusConflict, err.Error())
	case err != nil:
		s.serverError(w, "add market series", err)
	default:
		s.writeJSON(w, http.StatusCreated, dto)
	}
}

// handleRemoveMarketSeries deletes a configured series and its cache (admin).
func (s *Server) handleRemoveMarketSeries(w http.ResponseWriter, r *http.Request) {
	if s.market == nil {
		s.writeError(w, http.StatusServiceUnavailable, "market data is not available")
		return
	}
	id := chi.URLParam(r, "id")
	if err := s.removeMarketSeries(r.Context(), id); err != nil {
		if errors.Is(err, market.ErrUnknownSeries) {
			s.writeError(w, http.StatusNotFound, "unknown market series "+id)
			return
		}
		s.serverError(w, "remove market series", err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
}

// --- shared service layer (REST + MCP) ----------------------------------------

// addMarketSeries normalizes and appends a series to the configured list,
// persisting the new list and applying it live. Validation failures are wrapped as
// validationError (mapped to 400); a duplicate id returns market.ErrDuplicateID.
func (s *Server) addMarketSeries(ctx context.Context, spec market.SeriesSpec) (MarketSeriesDTO, error) {
	spec, err := market.NormalizeSpec(spec)
	if err != nil {
		return MarketSeriesDTO{}, validationError{err}
	}
	current := s.market.Specs()
	for _, sp := range current {
		if sp.ID == spec.ID {
			return MarketSeriesDTO{}, market.ErrDuplicateID
		}
	}
	next := append(current, spec)
	if err := s.persistMarketSpecs(ctx, next); err != nil {
		return MarketSeriesDTO{}, err
	}
	s.market.SetSpecs(next)
	return toMarketSeriesDTO(market.Series{SeriesSpec: spec, Provider: s.market.ProviderName()}), nil
}

// removeMarketSeries drops a series from the configured list and clears its cache.
func (s *Server) removeMarketSeries(ctx context.Context, id string) error {
	current := s.market.Specs()
	next := make([]market.SeriesSpec, 0, len(current))
	found := false
	for _, sp := range current {
		if sp.ID == id {
			found = true
			continue
		}
		next = append(next, sp)
	}
	if !found {
		return market.ErrUnknownSeries
	}
	if err := s.persistMarketSpecs(ctx, next); err != nil {
		return err
	}
	s.market.SetSpecs(next)
	if err := s.market.DeleteCached(ctx, id); err != nil {
		s.logger.Warn("clear market series cache", "series", id, "error", err)
	}
	return nil
}

// persistMarketSpecs writes the configured series list to the settings table under
// the market.series key. It is stored directly (not as a restart-tracked settings
// Definition) because the change applies live, so it must not trip the restart
// banner; the boot loader reads it back from the same key.
func (s *Server) persistMarketSpecs(ctx context.Context, specs []market.SeriesSpec) error {
	raw, err := market.MarshalSpecs(specs)
	if err != nil {
		return err
	}
	return s.store.UpsertSetting(ctx, db.UpsertSettingParams{
		Key:       market.SeriesSettingKey,
		Value:     raw,
		UpdatedAt: time.Now().Unix(),
	})
}

// --- MCP tool input/output types + handlers (registered in mcp.go) ---

type listMarketSeriesOutput struct {
	Provider   string            `json:"provider"`
	Configured bool              `json:"configured"`
	Series     []MarketSeriesDTO `json:"series"`
}

type getMarketPointsInput struct {
	ID    string `json:"id" jsonschema:"the series id (see list_market_series)"`
	Since string `json:"since,omitempty" jsonschema:"optional ISO date lower bound (inclusive), e.g. 2024-01-01"`
	Until string `json:"until,omitempty" jsonschema:"optional ISO date upper bound (inclusive)"`
}

type getMarketPointsOutput struct {
	Provider string           `json:"provider"`
	AsOf     string           `json:"as_of"`
	Fresh    bool             `json:"fresh"`
	Points   []MarketPointDTO `json:"points"`
}

type removeMarketSeriesInput struct {
	ID string `json:"id" jsonschema:"the id of the series to remove"`
}

type removeMarketSeriesOutput struct {
	ID      string `json:"id"`
	Deleted bool   `json:"deleted"`
}

func (s *Server) mcpListMarketSeries(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, listMarketSeriesOutput, error) {
	series, err := s.market.ListSeries(ctx)
	if err != nil {
		return nil, listMarketSeriesOutput{}, err
	}
	configured, _ := s.market.Configured(ctx)
	return &mcp.CallToolResult{}, listMarketSeriesOutput{
		Provider:   s.market.ProviderName(),
		Configured: configured,
		Series:     toMarketSeriesDTOs(series),
	}, nil
}

func (s *Server) mcpGetMarketPoints(ctx context.Context, _ *mcp.CallToolRequest, in getMarketPointsInput) (*mcp.CallToolResult, getMarketPointsOutput, error) {
	res, err := s.market.Points(ctx, strings.TrimSpace(in.ID), strings.TrimSpace(in.Since), strings.TrimSpace(in.Until))
	if err != nil {
		return nil, getMarketPointsOutput{}, err
	}
	return &mcp.CallToolResult{}, getMarketPointsOutput{
		Provider: res.Provider,
		AsOf:     res.AsOf,
		Fresh:    res.Fresh,
		Points:   toMarketPointDTOs(res.Points),
	}, nil
}

func (s *Server) mcpAddMarketSeries(ctx context.Context, _ *mcp.CallToolRequest, in marketSeriesInput) (*mcp.CallToolResult, MarketSeriesDTO, error) {
	dto, err := s.addMarketSeries(ctx, in.spec())
	if err != nil {
		return nil, MarketSeriesDTO{}, err
	}
	return &mcp.CallToolResult{}, dto, nil
}

func (s *Server) mcpRemoveMarketSeries(ctx context.Context, _ *mcp.CallToolRequest, in removeMarketSeriesInput) (*mcp.CallToolResult, removeMarketSeriesOutput, error) {
	if err := s.removeMarketSeries(ctx, strings.TrimSpace(in.ID)); err != nil {
		return nil, removeMarketSeriesOutput{}, err
	}
	return &mcp.CallToolResult{}, removeMarketSeriesOutput{ID: in.ID, Deleted: true}, nil
}
