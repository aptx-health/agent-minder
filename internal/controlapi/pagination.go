package controlapi

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
)

const DefaultLimit = 50
const MaxLimit = 200

const (
	EndpointDeployments  = "deployments"
	EndpointAutomations  = "automations"
	EndpointJobs         = "jobs"
	EndpointRuns         = "runs"
	EndpointDeliverables = "deliverables"
)

type Pagination struct {
	Cursor string
	Limit  int
}

type CursorPosition struct {
	SortKey string
	FinalID string
}

type cursorPayload struct {
	Version  int    `json:"v"`
	Endpoint string `json:"endpoint"`
	SortKey  string `json:"sort_key"`
	FinalID  string `json:"final_id"`
}

func EncodeCursor(endpoint string, position CursorPosition) (string, error) {
	if endpoint == "" || position.SortKey == "" || position.FinalID == "" {
		return "", &ContractError{Code: ErrorInvalidCursor, Message: "cursor position is incomplete"}
	}
	data, err := json.Marshal(cursorPayload{Version: 1, Endpoint: endpoint, SortKey: position.SortKey, FinalID: position.FinalID})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func DecodeCursor(encoded, endpoint string) (CursorPosition, error) {
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return CursorPosition{}, &ContractError{Code: ErrorInvalidCursor, Message: "cursor is malformed"}
	}
	var payload cursorPayload
	if err := json.Unmarshal(data, &payload); err != nil || payload.Version != 1 ||
		payload.Endpoint != endpoint || payload.SortKey == "" || payload.FinalID == "" {
		return CursorPosition{}, &ContractError{Code: ErrorInvalidCursor, Message: "cursor is malformed or belongs to another endpoint"}
	}
	return CursorPosition{SortKey: payload.SortKey, FinalID: payload.FinalID}, nil
}

func ParsePagination(values url.Values) (Pagination, error) {
	params := Pagination{Cursor: values.Get("cursor"), Limit: DefaultLimit}
	if raw := values.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > MaxLimit {
			return Pagination{}, &ContractError{Code: ErrorInvalidLimit, Message: "limit must be between 1 and 200"}
		}
		params.Limit = limit
	}
	return params, nil
}

// Paginate sorts a copy of items and applies an opaque keyset cursor. The
// cursor names both the endpoint and the final returned resource, preventing
// accidental reuse across collection routes.
func Paginate[T any](items []T, endpoint string, params Pagination, less func(a, b T) bool, position func(T) CursorPosition) ([]T, Page, error) {
	sorted := append([]T(nil), items...)
	sort.SliceStable(sorted, func(i, j int) bool { return less(sorted[i], sorted[j]) })
	start := 0
	if params.Cursor != "" {
		cursor, err := DecodeCursor(params.Cursor, endpoint)
		if err != nil {
			return nil, Page{}, err
		}
		found := false
		for i, item := range sorted {
			if position(item) == cursor {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			return nil, Page{}, &ContractError{Code: ErrorInvalidCursor, Message: "cursor does not identify an item in this collection"}
		}
	}
	limit := params.Limit
	if limit == 0 {
		limit = DefaultLimit
	}
	if limit < 1 || limit > MaxLimit {
		return nil, Page{}, &ContractError{Code: ErrorInvalidLimit, Message: "limit must be between 1 and 200"}
	}
	end := start + limit
	if end > len(sorted) {
		end = len(sorted)
	}
	pageItems := sorted[start:end]
	page := Page{HasMore: end < len(sorted)}
	if page.HasMore && len(pageItems) > 0 {
		next, err := EncodeCursor(endpoint, position(pageItems[len(pageItems)-1]))
		if err != nil {
			return nil, Page{}, err
		}
		page.NextCursor = &next
	}
	return pageItems, page, nil
}

// PaginateDeployments freezes deployment order by ascending deployment ID.
// The typed pagination helpers keep endpoint order out of individual handlers.
func PaginateDeployments(items []Deployment, params Pagination) ([]Deployment, Page, error) {
	return Paginate(items, EndpointDeployments, params,
		func(a, b Deployment) bool { return a.ID < b.ID },
		func(item Deployment) CursorPosition { return CursorPosition{SortKey: item.ID, FinalID: item.ID} })
}

func PaginateAutomations(items []Automation, params Pagination) ([]Automation, Page, error) {
	return Paginate(items, EndpointAutomations, params,
		func(a, b Automation) bool {
			if a.Kind != b.Kind {
				return a.Kind < b.Kind
			}
			if a.Name != b.Name {
				return a.Name < b.Name
			}
			return a.ID < b.ID
		},
		func(item Automation) CursorPosition {
			return CursorPosition{SortKey: item.Kind + "\x00" + item.Name, FinalID: item.ID}
		})
}

func PaginateJobs(items []Job, params Pagination) ([]Job, Page, error) {
	return Paginate(items, EndpointJobs, params,
		func(a, b Job) bool { return a.ID < b.ID },
		func(item Job) CursorPosition {
			id := strconv.FormatInt(item.ID, 10)
			return CursorPosition{SortKey: id, FinalID: id}
		})
}

func PaginateRuns(items []AgentRun, params Pagination) ([]AgentRun, Page, error) {
	return Paginate(items, EndpointRuns, params,
		func(a, b AgentRun) bool { return a.ID < b.ID },
		func(item AgentRun) CursorPosition {
			id := strconv.FormatInt(item.ID, 10)
			return CursorPosition{SortKey: id, FinalID: id}
		})
}

func PaginateDeliverables(items []Deliverable, params Pagination) ([]Deliverable, Page, error) {
	return Paginate(items, EndpointDeliverables, params,
		func(a, b Deliverable) bool { return a.ID < b.ID },
		func(item Deliverable) CursorPosition {
			id := strconv.FormatInt(item.ID, 10)
			return CursorPosition{SortKey: id, FinalID: id}
		})
}
