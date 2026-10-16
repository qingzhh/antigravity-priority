package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"antigravity-priority/internal/host"
	"antigravity-priority/internal/provider/antigravity"
)

var antigravityQuotaURLs = []string{
	antigravity.RetrieveUserQuotaSummaryURL,
	"https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:retrieveUserQuotaSummary",
	"https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary",
}

var antigravityAvailableModelsURLs = []string{
	antigravity.FetchAvailableModelsURL,
	"https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:fetchAvailableModels",
	"https://cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels",
}

type antigravityQuotaRequest struct {
	AuthIndex   string
	AccessToken string
	ProjectID   string
	ObservedAt  time.Time
}

type quotaHTTPDoer interface {
	HTTPDoRaw(context.Context, host.HTTPRequest) (host.HTTPResponse, error)
}

func executeAntigravityQuotaRequest(ctx context.Context, doer quotaHTTPDoer, request antigravityQuotaRequest) map[antigravity.ModelGroup]antigravity.ProbeResult {
	var lastStatus int
	var lastErr error
	var results map[antigravity.ModelGroup]antigravity.ProbeResult

	for _, url := range antigravityQuotaURLs {
		response, err := doer.HTTPDoRaw(ctx, host.HTTPRequest{
			AuthIndex: request.AuthIndex,
			Method:    http.MethodPost,
			URL:       url,
			Headers:   antigravityQuotaHeaders(request.AccessToken),
			Body:      antigravityQuotaBody(request.ProjectID),
		})
		if err != nil {
			lastErr = err
			continue
		}
		lastStatus = response.StatusCode
		if response.StatusCode != http.StatusOK {
			continue
		}
		results = antigravity.ParseAllModelGroups(response.Body, request.ObservedAt.UTC())
		for group, result := range results {
			result.AuthIndex = request.AuthIndex
			result.HTTPStatusCode = response.StatusCode
			results[group] = result
		}
		break
	}

	// Google moved Gemini quotas exclusively to fetchAvailableModels, leaving retrieveUserQuotaSummary
	// with only Claude/GPT models. Query available models if summary failed or Gemini quota is missing.
	needsFallback := results == nil || results[antigravity.ModelGroupGemini].Status != antigravity.StatusReady

	if needsFallback {
		for _, url := range antigravityAvailableModelsURLs {
			response, err := doer.HTTPDoRaw(ctx, host.HTTPRequest{
				AuthIndex: request.AuthIndex,
				Method:    http.MethodPost,
				URL:       url,
				Headers:   antigravityQuotaHeaders(request.AccessToken),
				Body:      antigravityQuotaBody(request.ProjectID),
			})
			if err != nil {
				if lastErr == nil {
					lastErr = err
				}
				continue
			}
			lastStatus = response.StatusCode
			if response.StatusCode != http.StatusOK {
				continue
			}
			fallbackResults := antigravity.ParseAllModelGroups(response.Body, request.ObservedAt.UTC())
			if results == nil {
				results = make(map[antigravity.ModelGroup]antigravity.ProbeResult, 2)
			}
			for group, fResult := range fallbackResults {
				fResult.AuthIndex = request.AuthIndex
				fResult.HTTPStatusCode = response.StatusCode
				if existing, exists := results[group]; !exists || existing.Status != antigravity.StatusReady {
					results[group] = fResult
				}
			}
			break
		}
	}

	if results != nil {
		return results
	}
	message := fmt.Sprintf("retrieve quota summary status %d", lastStatus)
	if lastErr != nil && lastStatus == 0 {
		message = "host http do failed"
	}
	return failedAntigravityQuotaResults(request, message, lastStatus)
}

func antigravityQuotaBody(projectID string) []byte {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil
	}
	body, _ := json.Marshal(struct {
		Project string `json:"project"`
	}{Project: projectID})
	return body
}

func antigravityQuotaHeaders(accessToken string) host.Header {
	token := "$TOKEN$"
	if trimmed := strings.TrimSpace(accessToken); trimmed != "" {
		token = trimmed
	}
	return host.Header{
		"Accept":        []string{"application/json"},
		"Authorization": []string{"Bearer " + token},
		"Content-Type":  []string{"application/json"},
		"User-Agent":    []string{"antigravity/cli/1.0.8 darwin/arm64"},
	}
}

func failedAntigravityQuotaResults(request antigravityQuotaRequest, message string, statusCode int) map[antigravity.ModelGroup]antigravity.ProbeResult {
	results := make(map[antigravity.ModelGroup]antigravity.ProbeResult, 2)
	for _, group := range []antigravity.ModelGroup{antigravity.ModelGroupGemini, antigravity.ModelGroupClaudeGPT} {
		result := antigravity.ParseAvailableModels(nil, request.ObservedAt.UTC(), group)
		result.AuthIndex = request.AuthIndex
		result.Error = message
		result.HTTPStatusCode = statusCode
		results[group] = result
	}
	return results
}
